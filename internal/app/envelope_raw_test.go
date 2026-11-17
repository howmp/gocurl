package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJSONEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "alpha", Path: "/"})
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			w.Header().Set("X-Test", "yes")
			fmt.Fprint(w, "hello")
		}
	}))
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sS", "--json-envelope", "-L", srv.URL+"/start")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("output is not JSON: %q: %v", out, err)
	}
	response := env["response"].(map[string]any)
	if response["status"] != float64(200) {
		t.Fatalf("response = %#v", response)
	}
	body := response["body"].(map[string]any)
	if body["text"] != "hello" || body["encoding"] != "text" {
		t.Fatalf("body = %#v", body)
	}
	if !strings.Contains(response["effective_url"].(string), "/final") {
		t.Fatalf("effective_url = %v", response["effective_url"])
	}
	redirects := response["redirects"].([]any)
	if len(redirects) < 2 {
		t.Fatalf("redirects = %#v", redirects)
	}
	if _, ok := env["timing"].(map[string]any); !ok {
		t.Fatalf("missing timing: %#v", env)
	}
}

func TestJSONEnvelopeRedactsURLUserinfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("alice", "s3cret")

	code, out, stderr := runGocurl(t, "-sS", "--json-envelope", u.String())
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if strings.Contains(out, "s3cret") {
		t.Fatalf("envelope leaked password: %q", out)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if got := env["url"].(string); !strings.Contains(got, "REDACTED") || strings.Contains(got, "alice") {
		t.Fatalf("url = %q", got)
	}
	request := env["request"].(map[string]any)
	if got := request["url"].(string); strings.Contains(got, "s3cret") {
		t.Fatalf("request.url = %q", got)
	}
	response := env["response"].(map[string]any)
	if got := response["effective_url"].(string); strings.Contains(got, "s3cret") {
		t.Fatalf("effective_url = %q", got)
	}

	code, out, stderr = runGocurl(t, "-sS", "--json-envelope", "--show-secrets", u.String())
	if code != 0 || !strings.Contains(out, "s3cret") {
		t.Fatalf("show-secrets code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestWriteOutRedactsURLUserinfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("alice", "s3cret")

	code, out, stderr := runGocurl(t, "-sS", "-w", "%{url_effective}", u.String())
	if code != 0 || strings.Contains(out, "s3cret") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	code, out, stderr = runGocurl(t, "-sS", "--show-secrets", "-w", "%{url_effective}", u.String())
	if code != 0 || !strings.Contains(out, "s3cret") {
		t.Fatalf("show-secrets code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestJSONEnvelopeFileBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "file-body")
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "body.bin")
	code, out, stderr := runGocurl(t, "-sS", "--json-envelope", "-o", path, srv.URL)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("output is not JSON: %q: %v", out, err)
	}
	response := env["response"].(map[string]any)
	body := response["body"].(map[string]any)
	if body["encoding"] != "file" || body["file"] != path {
		t.Fatalf("body = %#v", body)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "file-body" {
		t.Fatalf("file body = %q, err=%v", data, err)
	}
}

func TestJSONEnvelopeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer srv.Close()
	code, out, stderr := runGocurl(t, "-sS", "--json-envelope", "-f", srv.URL)
	if code != 22 {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("output is not JSON: %q: %v", out, err)
	}
	response := env["response"].(map[string]any)
	if response["status"] != float64(404) {
		t.Fatalf("response = %#v", response)
	}
	errObj := env["error"].(map[string]any)
	if errObj["code"] != float64(22) || errObj["kind"] != "http_status" {
		t.Fatalf("error = %#v", errObj)
	}
}

func TestRawRequestFile(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	gotCh := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		var got strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			got.WriteString(line)
			if line == "\r\n" {
				break
			}
		}
		gotCh <- got.String()
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"))
	}()

	request := "GET /a/../b HTTP/1.1\r\nHost: raw.test\r\nX-Mixed-Case: yes\r\nX-Dup: one\r\nX-Dup: two\r\n\r\n"
	path := filepath.Join(t.TempDir(), "request.txt")
	if err := os.WriteFile(path, []byte(request), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runGocurl(t, "-sS", "--raw-request", path,
		"http://"+ln.Addr().String()+"/")
	if code != 0 || out != "ok" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	select {
	case got := <-gotCh:
		if got != request {
			t.Fatalf("raw request mismatch:\n got %q\nwant %q", got, request)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for raw request")
	}
}

func TestStreamingFormUpload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer file.Close()
		n, _ := io.Copy(io.Discard, file)
		fmt.Fprintf(w, "%d", n)
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "large.bin")
	data := strings.Repeat("z", 2<<20)
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runGocurl(t, "-sS", "-F", "file=@"+path, srv.URL)
	if code != 0 || out != fmt.Sprint(len(data)) {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestJSONEnvelopeRedactsSecrets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "sid=secret")
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	for _, args := range [][]string{
		{"-sS", "--json-envelope", "-H", "Authorization: Bearer secret", "-H", "Cookie: sid=secret", srv.URL},
		{"-sS", "--json-envelope", "--show-secrets", "-H", "Authorization: Bearer secret", srv.URL},
	} {
		code, out, stderr := runGocurl(t, args...)
		if code != 0 {
			t.Fatalf("args=%q code=%d stderr=%q", args, code, stderr)
		}
		show := false
		for _, arg := range args {
			if arg == "--show-secrets" {
				show = true
			}
		}
		leaked := strings.Contains(out, "Bearer secret") || strings.Contains(out, "sid=secret")
		if show && !strings.Contains(out, "Bearer secret") {
			t.Fatalf("show-secrets did not expose: %s", out)
		}
		if !show && leaked {
			t.Fatalf("secret leaked for args=%q: %s", args, out)
		}
	}
}

func TestRawHTTPFramesContentLength(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "data", args: []string{"-d", "hello"}, want: "Content-Length: 5\r\n"},
		{name: "json", args: []string{"--json", `{"a":1}`}, want: "Content-Length: 7\r\n"},
		{name: "empty", args: []string{"-X", "POST"}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			gotCh := make(chan string, 1)
			go func() {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				reader := bufio.NewReader(conn)
				var head strings.Builder
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						break
					}
					head.WriteString(line)
					if line == "\r\n" {
						break
					}
				}
				gotCh <- head.String()
				_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"))
			}()

			args := append([]string{"-sS", "--raw-http"}, tc.args...)
			args = append(args, "http://"+ln.Addr().String()+"/")
			code, out, stderr := runGocurl(t, args...)
			if code != 0 || out != "ok" {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			head := <-gotCh
			if tc.want == "" {
				if strings.Contains(head, "Content-Length") {
					t.Fatalf("unexpected Content-Length: %q", head)
				}
				return
			}
			if !strings.Contains(head, tc.want) {
				t.Fatalf("missing %q in request:\n%s", tc.want, head)
			}
		})
	}
}
