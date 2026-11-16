package app

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gocurl/internal/config"
	"gocurl/internal/output"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

func runGocurl(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	return runGocurlMode(t, false, args...)
}

func runGocurlMode(t *testing.T, persist bool, args ...string) (int, string, string) {
	t.Helper()
	if !persist {
		args = append([]string{"--no-persist"}, args...)
	}
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestUnsupportedProtocol(t *testing.T) {
	code, _, stderr := runGocurl(t, "gopher://example.com")
	if code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(stderr, `Protocol "gopher" not supported`) {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestHTTPGetAndHeaderOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s|%s|%s|%s", r.Method, r.URL.Path,
			r.Header.Get("X-Test"), r.Header.Get("Accept"))
	}))
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sS", "-H", "X-Test: yes",
		"-H", "Accept: application/json", srv.URL+"/hello")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if out != "GET|/hello|yes|application/json" {
		t.Fatalf("out = %q", out)
	}
}

func TestPOSTData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "%s|%s|%s", r.Method, body, r.Header.Get("Content-Type"))
	}))
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sS", "-d", "a=1", "-d", "b=2", srv.URL)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if out != "POST|a=1&b=2|application/x-www-form-urlencoded" {
		t.Fatalf("out = %q", out)
	}
}

func TestHTTPSHTTP2(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%d", r.ProtoMajor)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sk", srv.URL)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if out != "2" {
		t.Fatalf("proto = %q, want HTTP/2", out)
	}
}

func TestHTTPSHTTP1Fallback(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%d", r.ProtoMajor)
	}))
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sk", srv.URL)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if out != "1" {
		t.Fatalf("proto = %q, want HTTP/1.1", out)
	}
}

func TestForcedHTTP1(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%d", r.ProtoMajor)
	}))
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sk", "--http1.1", srv.URL)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if out != "1" {
		t.Fatalf("proto = %q, want HTTP/1.1", out)
	}
}

func TestCompressedResponses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := []byte("compressed-body")
		switch r.URL.Path {
		case "/gzip":
			w.Header().Set("Content-Encoding", "gzip")
			gw := gzip.NewWriter(w)
			_, _ = gw.Write(body)
			_ = gw.Close()
		case "/br":
			w.Header().Set("Content-Encoding", "br")
			bw := brotli.NewWriter(w)
			_, _ = bw.Write(body)
			_ = bw.Close()
		case "/zstd":
			w.Header().Set("Content-Encoding", "zstd")
			zw, _ := zstd.NewWriter(w)
			_, _ = zw.Write(body)
			_ = zw.Close()
		}
	}))
	defer srv.Close()

	for _, path := range []string{"/gzip", "/br", "/zstd"} {
		code, out, stderr := runGocurl(t, "-sS", srv.URL+path)
		if code != 0 || out != "compressed-body" {
			t.Fatalf("%s: exit=%d out=%q stderr=%q", path, code, out, stderr)
		}
	}
}

func TestHead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Method", r.Method)
		fmt.Fprint(w, "body")
	}))
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sS", "-I", srv.URL)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(out, "HTTP/1.1 200") || !strings.Contains(out, "X-Method: HEAD") {
		t.Fatalf("out = %q", out)
	}
	if strings.Contains(out, "body") {
		t.Fatalf("HEAD output contains body: %q", out)
	}
}

func TestOutputFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "file-body")
	}))
	defer srv.Close()

	file := filepath.Join(t.TempDir(), "out.txt")
	code, out, stderr := runGocurl(t, "-sS", "-o", file, srv.URL)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if out != "" {
		t.Fatalf("stdout = %q", out)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "file-body" {
		t.Fatalf("file = %q", data)
	}
}

func TestRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		fmt.Fprint(w, "final")
	}))
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sS", "-L", srv.URL+"/redirect")
	if code != 0 || out != "final" {
		t.Fatalf("follow: exit=%d out=%q stderr=%q", code, out, stderr)
	}

	code, out, stderr = runGocurl(t, "-sS", "-o", filepath.Join(t.TempDir(), "redirect.out"),
		"-w", "%{http_code}", srv.URL+"/redirect")
	if code != 0 || out != "302" {
		t.Fatalf("no-follow: exit=%d out=%q stderr=%q", code, out, stderr)
	}

	code, _, _ = runGocurl(t, "-sS", "-L", "--max-redirs", "0", srv.URL+"/redirect")
	if code != 47 {
		t.Fatalf("max-redirs exit = %d, want 47", code)
	}
}

func TestFailOnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	code, _, stderr := runGocurl(t, "-sS", "-f", srv.URL)
	if code != 22 {
		t.Fatalf("exit = %d, want 22; stderr=%q", code, stderr)
	}
	code, out, _ := runGocurl(t, "-sS", srv.URL)
	if code != 0 || !strings.Contains(out, "nope") {
		t.Fatalf("without -f: exit=%d out=%q", code, out)
	}
}

func TestBasicAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		fmt.Fprintf(w, "%v|%s|%s", ok, user, pass)
	}))
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sS", "-u", "alice:secret", srv.URL)
	if code != 0 || out != "true|alice|secret" {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestHeaderRemoval(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "accept=%q", r.Header.Get("Accept"))
	}))
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sS", "-H", "Accept:", srv.URL)
	if code != 0 || out != `accept=""` {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestEmptyDataImpliesPOST(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s|%d", r.Method, r.ContentLength)
	}))
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sS", "-d", "", srv.URL)
	if code != 0 || out != "POST|0" {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "%s|%s|%s|%s", r.Method, body,
			r.Header.Get("Content-Type"), r.Header.Get("Accept"))
	}))
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sS", "--json", `{"a":1}`, srv.URL)
	if code != 0 || out != `POST|{"a":1}|application/json|application/json` {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestGetData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s|%s", r.Method, r.URL.RawQuery)
	}))
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sS", "-G", "-d", "a=1", "-d", "b=2", srv.URL)
	if code != 0 || out != "GET|a=1&b=2" {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestMaxTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		fmt.Fprint(w, "late")
	}))
	defer srv.Close()

	code, _, _ := runGocurl(t, "-sS", "--max-time", "0.05", srv.URL)
	if code != 28 {
		t.Fatalf("exit = %d, want 28", code)
	}
}

func TestSilentSuppressesError(t *testing.T) {
	code, _, stderr := runGocurl(t, "-s", "gopher://example.com")
	if code != 1 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	code, _, stderr = runGocurl(t, "-sS", "gopher://example.com")
	if code != 1 || !strings.Contains(stderr, "not supported") {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
}

func TestHTTPUpload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "%s|%s", r.Method, body)
	}))
	defer srv.Close()

	file := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(file, []byte("upload-http"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runGocurl(t, "-sS", "-T", file, srv.URL)
	if code != 0 || out != "PUT|upload-http" {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestDumpHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Dump", "yes")
		fmt.Fprint(w, "body")
	}))
	defer srv.Close()

	file := filepath.Join(t.TempDir(), "headers.txt")
	code, out, stderr := runGocurl(t, "-sS", "-D", file, srv.URL)
	if code != 0 || out != "body" {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "X-Dump: yes") {
		t.Fatalf("headers = %q", data)
	}
}

func TestWriteOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "body")
	}))
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sS", "-o", filepath.Join(t.TempDir(), "out"),
		"-w", "%{http_code}|%{http_version}|%{content_type}|%{remote_ip}", srv.URL)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if out != "200|1.1|text/plain|127.0.0.1" {
		t.Fatalf("write-out = %q", out)
	}
}

func TestCertificateError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	code, _, stderr := runGocurl(t, "-sS", srv.URL)
	if code != 60 {
		t.Fatalf("exit = %d, want 60; stderr=%q", code, stderr)
	}
}

func TestMultipleOutputs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "body-%s", strings.TrimPrefix(r.URL.Path, "/"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	code, _, stderr := runGocurl(t, "-sS", "-o", a, "-o", b, srv.URL+"/a", srv.URL+"/b")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	for file, want := range map[string]string{a: "body-a", b: "body-b"} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != want {
			t.Fatalf("%s = %q, want %q", file, data, want)
		}
	}
}

func TestInclude(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Include", "yes")
		fmt.Fprint(w, "body")
	}))
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sS", "-i", srv.URL)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(out, "HTTP/1.1 200 OK") ||
		!strings.Contains(out, "X-Include: yes") ||
		!strings.HasSuffix(out, "\r\n\r\nbody") {
		t.Fatalf("out = %q", out)
	}
}

func TestRemoteName(t *testing.T) {
	u, err := url.Parse("https://example.com/path/file%20name.txt?x=1")
	if err != nil {
		t.Fatal(err)
	}
	name, err := output.RemoteName(u)
	if err != nil {
		t.Fatal(err)
	}
	if name != "file name.txt" {
		t.Fatalf("name = %q", name)
	}
}

func TestRejectConflictingRequestHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	for _, args := range [][]string{
		{"-sS", "-H", "Content-Length: 99", "-d", "abc", srv.URL},
		{"-sS", "-H", "Transfer-Encoding: chunked", "-d", "abc", srv.URL},
	} {
		code, _, stderr := runGocurl(t, args...)
		if code == 0 || !strings.Contains(stderr, "header") {
			t.Fatalf("args=%q code=%d stderr=%q", args, code, stderr)
		}
	}
	code, _, stderr := runGocurl(t, "-sS", "--request-target", "/a b", srv.URL)
	if code == 0 || !strings.Contains(stderr, "request target") {
		t.Fatalf("request target code=%d stderr=%q", code, stderr)
	}
}

func TestRawRejectsHeaderInjection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	code, _, stderr := runGocurl(t, "-sS", "--raw-http", "-H", "X-Test: one\r\nInjected: yes", srv.URL)
	if code == 0 || (!strings.Contains(stderr, "raw mode") && !strings.Contains(stderr, "invalid value")) {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestWriteErrorExitCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "body")
	}))
	defer srv.Close()
	dir := t.TempDir()
	code, _, stderr := runGocurl(t, "-sS", "-o", dir, srv.URL)
	if code != 23 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestInvalidMaxTimeValues(t *testing.T) {
	for _, value := range []string{"nan", "inf", "1e18"} {
		code, _, stderr := runGocurl(t, "-sS", "--max-time", value, "https://example.test/")
		if code != 2 || stderr == "" {
			t.Fatalf("value=%s code=%d stderr=%q", value, code, stderr)
		}
	}
}

func TestUnsupportedContentEncodingExitCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "bogus")
		fmt.Fprint(w, "body")
	}))
	defer srv.Close()
	code, _, stderr := runGocurl(t, "-sS", srv.URL)
	if code != 61 || !strings.Contains(stderr, "content encoding") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestHelpCoversOptionTable(t *testing.T) {
	var buf bytes.Buffer
	printHelp(&buf)
	help := buf.String()
	allow := map[string]bool{"globoff": true, "path-as-is": true, "tcp-nodelay": true, "no-progress-meter": true, "data-ascii": true}
	for _, name := range config.OptionNames() {
		if strings.HasPrefix(name, "no-") || allow[name] {
			continue
		}
		if !strings.Contains(help, "--"+name) {
			t.Errorf("option --%s missing from help", name)
		}
	}
}
