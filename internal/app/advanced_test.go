package app

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"gocurl/internal/config"
	"gocurl/internal/daemon"
	"gocurl/internal/httpx"
	"gocurl/internal/transport"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	fhttp "github.com/bogdanfinn/fhttp"
)

func TestRequestTargetAndMethodCase(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	lineCh := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		line, _ := reader.ReadString('\n')
		lineCh <- strings.TrimSpace(line)
		for {
			header, err := reader.ReadString('\n')
			if err != nil || header == "\r\n" {
				break
			}
		}
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"))
	}()

	code, out, stderr := runGocurl(t, "-sS", "-X", "get", "--request-target", "*",
		"http://"+ln.Addr().String()+"/")
	if code != 0 || out != "ok" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	select {
	case line := <-lineCh:
		if line != "get * HTTP/1.1" {
			t.Fatalf("request line = %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for request")
	}
}

func TestRawHTTP10(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	lineCh := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		line, _ := reader.ReadString('\n')
		lineCh <- strings.TrimSpace(line)
		for {
			header, err := reader.ReadString('\n')
			if err != nil || header == "\r\n" {
				break
			}
		}
		_, _ = conn.Write([]byte("HTTP/1.0 200 OK\r\nContent-Length: 2\r\n\r\nok"))
	}()

	code, out, stderr := runGocurl(t, "-sS", "--http1.0", "http://"+ln.Addr().String()+"/")
	if code != 0 || out != "ok" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	line := <-lineCh
	if !strings.HasSuffix(line, " HTTP/1.0") {
		t.Fatalf("request line = %q", line)
	}
}

func TestJSONWriteOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "hello")
	}))
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sS", "-w", "%{json}", srv.URL)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out[len("hello"):]), &got); err != nil {
		t.Fatalf("invalid json %q: %v", out, err)
	}
	if got["http_code"] != float64(200) || got["content_type"] != "text/plain" {
		t.Fatalf("json = %#v", got)
	}
}

func TestContinueAt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=3-" {
			http.Error(w, "bad range", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Range", "bytes 3-5/6")
		w.WriteHeader(http.StatusPartialContent)
		fmt.Fprint(w, "def")
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "download.txt")
	if err := os.WriteFile(path, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runGocurl(t, "-sS", "-C", "-", "-o", path, srv.URL)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "abcdef" {
		t.Fatalf("data=%q", data)
	}
}

func TestConnectTo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.Host)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	_, port, _ := net.SplitHostPort(addr)
	target := "http://connect.test:80/"
	code, out, stderr := runGocurl(t, "-sS",
		"--connect-to", "connect.test:80:127.0.0.1:"+port, target)
	if code != 0 || out != "connect.test:80" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	setupDaemonTest(t)
	code, out, stderr = runGocurlMode(t, true, "-sS",
		"--connect-to", "connect.test:80:127.0.0.1:"+port, target)
	if code != 0 || out != "connect.test:80" {
		t.Fatalf("daemon code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestUnixSocket(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "http.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s|%s", r.Host, r.URL.Path)
	})}
	go srv.Serve(ln)
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sS", "--unix-socket", socket, "http://unix.test/hello")
	if code != 0 || out != "unix.test|/hello" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	setupDaemonTest(t)
	code, out, stderr = runGocurlMode(t, true, "-sS", "--unix-socket", socket, "http://unix.test/daemon")
	if code != 0 || out != "unix.test|/daemon" {
		t.Fatalf("daemon code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestInterface(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	code, out, stderr := runGocurl(t, "-sS", "--interface", "lo", srv.URL)
	if code != 0 || out != "ok" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://ignored.example:8080")
	t.Setenv("http_proxy", "http://proxy.example:8080")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("NO_PROXY", "")
	o := config.NewOptions()
	u, _ := url.Parse("http://example.test/path")
	got, err := transport.ProxyURLFor(o, u)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "proxy.example:8080") || strings.Contains(got, "ignored.example") {
		t.Fatalf("proxy = %q", got)
	}
	t.Setenv("NO_PROXY", "example.test")
	got, err = transport.ProxyURLFor(o, u)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("NO_PROXY not honored: %q", got)
	}

	proxySrv, seen, _ := newConnectProxy(t)
	defer proxySrv.Close()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "via-env-proxy")
	}))
	defer target.Close()
	t.Setenv("http_proxy", proxySrv.URL)
	t.Setenv("NO_PROXY", "")
	code, out, stderr := runGocurl(t, "-sS", target.URL)
	if code != 0 || out != "via-env-proxy" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if !seen.Load() {
		t.Fatal("environment proxy was not used for loopback target")
	}
}

func TestDigestAuth(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Digest ") {
			w.Header().Set("WWW-Authenticate", `Digest realm="test", nonce="abc123", qop="auth", algorithm=MD5`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, "digest-ok")
	}))
	defer srv.Close()
	code, out, stderr := runGocurl(t, "-sS", "--digest", "-u", "user:pass", srv.URL)
	if code != 0 || out != "digest-ok" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("attempts=%d, want 2", got)
	}
}

func TestDigestAuthRetryRejectsCrossOriginChallenge(t *testing.T) {
	var leaked atomic.Value
	var sourceHits, targetHits int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&targetHits, 1)
		w.Header().Set("WWW-Authenticate", `Digest realm="target", nonce="abc123", qop="auth", algorithm=MD5`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&sourceHits, 1)
		if auth := r.Header.Get("Authorization"); auth != "" {
			leaked.Store(auth)
		}
		http.Redirect(w, r, target.URL+"/protected", http.StatusFound)
	}))
	defer source.Close()

	code, _, stderr := runGocurl(t, "-sS", "-L", "--digest", "-u", "user:pass", source.URL+"/start")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if got := leaked.Load(); got != nil {
		t.Fatalf("cross-origin credentials leaked to source: %v", got)
	}
	if got := atomic.LoadInt32(&sourceHits); got != 1 {
		t.Fatalf("source attempts=%d, want 1", got)
	}
	if got := atomic.LoadInt32(&targetHits); got != 1 {
		t.Fatalf("target attempts=%d, want 1", got)
	}
}

func TestDigestAuthRetrySameOriginRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			if r.Header.Get("Authorization") == "" {
				http.Redirect(w, r, "/protected", http.StatusFound)
				return
			}
			fmt.Fprint(w, "same-origin-ok")
		case "/protected":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Digest ") {
				w.Header().Set("WWW-Authenticate", `Digest realm="test", nonce="abc123", qop="auth", algorithm=MD5`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, "protected")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sS", "-L", "--digest", "-u", "user:pass", srv.URL+"/start")
	if code != 0 || out != "same-origin-ok" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestAnyAuthBasic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Basic ") {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, "any-auth-ok")
	}))
	defer srv.Close()
	code, out, stderr := runGocurl(t, "-sS", "--anyauth", "-u", "user:pass", srv.URL)
	if code != 0 || out != "any-auth-ok" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestNetrcAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		fmt.Fprintf(w, "%v|%s|%s", ok, user, pass)
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "netrc")
	if err := os.WriteFile(path, []byte("machine 127.0.0.1 login alice password secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runGocurl(t, "-sS", "--netrc-file", path, srv.URL)
	if code != 0 || out != "true|alice|secret" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestJSONOutputFlag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "body")
	}))
	defer srv.Close()
	code, out, stderr := runGocurl(t, "-sS", "--json-output", srv.URL)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	idx := strings.Index(out, "{")
	if idx < 0 {
		t.Fatalf("no json in %q", out)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out[idx:]), &got); err != nil {
		t.Fatalf("json = %q: %v", out[idx:], err)
	}
	if got["http_code"] != float64(200) {
		t.Fatalf("json = %#v", got)
	}
}

func TestMaxFilesize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, strings.Repeat("x", 100))
	}))
	defer srv.Close()
	code, _, stderr := runGocurl(t, "-sS", "--max-filesize", "10", srv.URL)
	if code != 63 || !strings.Contains(stderr, "Maximum filesize exceeded") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestSessionSocketIsolation(t *testing.T) {
	a := daemon.SocketPathFor("session-a")
	b := daemon.SocketPathFor("session-b")
	if a == b {
		t.Fatalf("session sockets are identical: %s", a)
	}
}

func TestCAPath(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ca-path-ok")
	}))
	defer srv.Close()
	dir := t.TempDir()
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runGocurl(t, "-sS", "--capath", dir, srv.URL)
	if code != 0 || out != "ca-path-ok" {
		t.Fatalf("capath code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestStreamingUpload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		fmt.Fprintf(w, "%d", n)
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "upload.bin")
	data := strings.Repeat("x", 2<<20)
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runGocurl(t, "-sS", "-T", path, srv.URL)
	if code != 0 || out != fmt.Sprint(len(data)) {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestFormMultipleFiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		files := r.MultipartForm.File["files"]
		fmt.Fprintf(w, "%d", len(files))
	}))
	defer srv.Close()
	dir := t.TempDir()
	one := filepath.Join(dir, "one.txt")
	two := filepath.Join(dir, "two.txt")
	_ = os.WriteFile(one, []byte("one"), 0o644)
	_ = os.WriteFile(two, []byte("two"), 0o644)
	code, out, stderr := runGocurl(t, "-sS", "-F", "files=@"+one+","+two, srv.URL)
	if code != 0 || out != "2" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestFormHeadersAndEncoder(t *testing.T) {
	var raw atomic.Value
	raw.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		raw.Store(string(data))
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "content.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runGocurl(t, "-sS", "-F",
		"text=<"+path+";type=text/plain;headers=X-Custom: yes;encoder=base64", srv.URL)
	if code != 0 || out != "ok" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	body := raw.Load().(string)
	if !strings.Contains(body, "X-Custom: yes") || !strings.Contains(body, "aGVsbG8=") {
		t.Fatalf("multipart body missing header/encoder: %q", body)
	}
}

func TestRawDisablesDecode(t *testing.T) {
	var compressed bytes.Buffer
	gw := gzip.NewWriter(&compressed)
	_, _ = gw.Write([]byte("hello"))
	_ = gw.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("TE") != "gzip" && r.URL.Path == "/te" {
			http.Error(w, "TE missing", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(compressed.Bytes())
	}))
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sS", srv.URL)
	if code != 0 || out != "hello" {
		t.Fatalf("decoded code=%d out=%q stderr=%q", code, out, stderr)
	}
	code, out, stderr = runGocurl(t, "-sS", "--raw", srv.URL)
	if code != 0 || out == "hello" {
		t.Fatalf("raw code=%d out=%q stderr=%q", code, out, stderr)
	}
	code, _, stderr = runGocurl(t, "-sS", "--tr-encoding", srv.URL+"/te")
	if code != 0 {
		t.Fatalf("tr-encoding code=%d stderr=%q", code, stderr)
	}
}

func TestHeaderOrder(t *testing.T) {
	o := config.NewOptions()
	o.HeaderOrder = []string{"x-test", "user-agent", "accept"}
	o.Headers = []config.HeaderArg{{Name: "X-Test", Value: "yes"}}
	req, err := fhttp.NewRequest("GET", "https://example.test/", nil)
	if err != nil {
		t.Fatal(err)
	}
	httpx.ApplyHeaders(req, o, "", nil)
	order := req.Header[fhttp.HeaderOrderKey]
	if len(order) < 3 || order[0] != "x-test" || order[1] != "user-agent" || order[2] != "accept" {
		t.Fatalf("order = %v", order)
	}
}

func TestLocationTrusted(t *testing.T) {
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.Header.Get("Authorization"))
	}))
	defer dest.Close()
	start := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dest.URL, http.StatusFound)
	}))
	defer start.Close()

	code, out, stderr := runGocurl(t, "-sS", "-L", "--location-trusted",
		"-H", "Authorization: Bearer token", start.URL)
	if code != 0 || out != "Bearer token" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestConnectToWithProxy(t *testing.T) {
	proxySrv, seen, _ := newConnectProxy(t)
	defer proxySrv.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.Host)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	_, port, _ := net.SplitHostPort(addr)
	target := "http://connect-proxy.test:80/"
	code, out, stderr := runGocurl(t, "-sS",
		"--connect-to", "connect-proxy.test:80:127.0.0.1:"+port,
		"-x", proxySrv.URL, target)
	if code != 0 || out != "connect-proxy.test:80" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if !seen.Load() {
		t.Fatal("proxy was not used")
	}
}

func TestResolveWithProxy(t *testing.T) {
	proxySrv, seen, _ := newConnectProxy(t)
	defer proxySrv.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.Host)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	_, port, _ := net.SplitHostPort(addr)
	target := "http://resolve-proxy.test:" + port + "/"
	code, out, stderr := runGocurl(t, "-sS",
		"--resolve", "resolve-proxy.test:"+port+":127.0.0.1",
		"-x", proxySrv.URL, target)
	if code != 0 || out != "resolve-proxy.test:"+port {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if !seen.Load() {
		t.Fatal("proxy was not used")
	}
}

func TestProxyHeader(t *testing.T) {
	var got atomic.Value
	got.Store("")
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		got.Store(r.Header.Get("X-Proxy-Test"))
		dest, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		hj := w.(http.Hijacker)
		client, _, _ := hj.Hijack()
		_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		go func() { _, _ = io.Copy(dest, client); _ = dest.Close() }()
		_, _ = io.Copy(client, dest)
		_ = client.Close()
	}))
	defer proxySrv.Close()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer target.Close()

	code, _, stderr := runGocurl(t, "-sS", "-x", proxySrv.URL,
		"--proxy-header", "X-Proxy-Test: present", target.URL)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if got.Load() != "present" {
		t.Fatalf("proxy header = %q", got.Load())
	}
}

func TestProxyInsecure(t *testing.T) {
	proxySrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		dest, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		hj := w.(http.Hijacker)
		client, _, _ := hj.Hijack()
		_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		go func() { _, _ = io.Copy(dest, client); _ = dest.Close() }()
		_, _ = io.Copy(client, dest)
		_ = client.Close()
	}))
	defer proxySrv.Close()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "proxy-insecure-ok")
	}))
	defer target.Close()

	code, out, stderr := runGocurl(t, "-sS", "-x", proxySrv.URL, "--proxy-insecure", target.URL)
	if code != 0 || out != "proxy-insecure-ok" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestProxySelectedPerRedirectHop(t *testing.T) {
	proxied := make(chan string, 8)
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		proxied <- r.Host
		dest, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		hj := w.(http.Hijacker)
		client, _, _ := hj.Hijack()
		_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		go func() { _, _ = io.Copy(dest, client); _ = dest.Close() }()
		_, _ = io.Copy(client, dest)
		_ = client.Close()
	}))
	defer proxySrv.Close()

	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "direct")
	}))
	defer final.Close()
	start := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, http.StatusFound)
	}))
	defer start.Close()

	finalURL, err := url.Parse(final.URL)
	if err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runGocurl(t, "-sS", "-L", "-x", proxySrv.URL,
		"--noproxy", "127.0.0.1:"+finalURL.Port(), start.URL)
	if code != 0 || out != "direct" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	var seen []string
collect:
	for {
		select {
		case host := <-proxied:
			seen = append(seen, host)
		default:
			break collect
		}
	}
	startURL, err := url.Parse(start.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != startURL.Host {
		t.Fatalf("proxy connections = %v, want only %q", seen, startURL.Host)
	}
}

func TestPreProxy(t *testing.T) {
	newProxy := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodConnect {
				http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
				return
			}
			dest, err := net.Dial("tcp", r.Host)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			hj := w.(http.Hijacker)
			client, _, _ := hj.Hijack()
			_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
			go func() { _, _ = io.Copy(dest, client); _ = dest.Close() }()
			_, _ = io.Copy(client, dest)
			_ = client.Close()
		}))
	}
	finalProxy := newProxy()
	defer finalProxy.Close()
	preProxy := newProxy()
	defer preProxy.Close()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "preproxy-ok")
	}))
	defer target.Close()

	code, out, stderr := runGocurl(t, "-sS", "-x", finalProxy.URL, "--preproxy", preProxy.URL, target.URL)
	if code != 0 || out != "preproxy-ok" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestSessionCookiesIsolated(t *testing.T) {
	dir := t.TempDir()
	_ = os.Chmod(dir, 0o700)
	t.Setenv("GOCURL_SOCKET", "")
	t.Setenv("GOCURL_DAEMON_DIR", dir)
	t.Setenv("GOCURL_NO_DAEMON", "")
	t.Setenv("GOCURL_DAEMON_IDLE", "30s")
	t.Cleanup(func() {
		_ = daemon.Stop(daemon.SocketPathFor("a"))
		_ = daemon.Stop(daemon.SocketPathFor("b"))
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/set":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "alpha", Path: "/"})
			fmt.Fprint(w, "set")
		case "/get":
			fmt.Fprint(w, r.Header.Get("Cookie"))
		}
	}))
	defer srv.Close()

	if code, _, stderr := runGocurlMode(t, true, "-sS", "--session", "a", srv.URL+"/set"); code != 0 {
		t.Fatalf("set code=%d stderr=%q", code, stderr)
	}
	code, out, stderr := runGocurlMode(t, true, "-sS", "--session", "b", srv.URL+"/get")
	if code != 0 || out != "" {
		t.Fatalf("session b code=%d out=%q stderr=%q", code, out, stderr)
	}
	code, out, stderr = runGocurlMode(t, true, "-sS", "--session", "a", srv.URL+"/get")
	if code != 0 || out != "session=alpha" {
		t.Fatalf("session a code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestH2CPriorKnowledge(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s|%s", r.Proto, r.URL.Path)
	})
	srv := httptest.NewServer(h2c.NewHandler(handler, &http2.Server{}))
	defer srv.Close()
	code, out, stderr := runGocurl(t, "-sS", "--http2-prior-knowledge", srv.URL+"/h2c")
	if code != 0 || out != "HTTP/2.0|/h2c" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestH2CPriorKnowledgeRejectsHTTPSRedirect(t *testing.T) {
	var tlsHits int32
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tlsHits, 1)
	}))
	defer tlsSrv.Close()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, tlsSrv.URL+"/secure", http.StatusFound)
	})
	srv := httptest.NewServer(h2c.NewHandler(handler, &http2.Server{}))
	defer srv.Close()

	code, _, stderr := runGocurl(t, "-sS", "-k", "-L", "--http2-prior-knowledge", srv.URL+"/start")
	if code == 0 || !strings.Contains(stderr, "h2c") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if got := atomic.LoadInt32(&tlsHits); got != 0 {
		t.Fatalf("TLS redirect target received %d requests", got)
	}
}

func TestRedirectPost302(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/end", http.StatusFound)
		case "/end":
			body, _ := io.ReadAll(r.Body)
			fmt.Fprintf(w, "%s|%s", r.Method, body)
		}
	}))
	defer srv.Close()
	code, out, stderr := runGocurl(t, "-sS", "-L", "--post302", "-d", "payload", srv.URL+"/start")
	if code != 0 || out != "POST|payload" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestContinueAtRejectsIgnoredRange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "abcdef")
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "download.txt")
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runGocurl(t, "-sS", "-C", "-", "-o", path, srv.URL)
	if code != 33 || !strings.Contains(stderr, "ignored resume") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "abc" {
		t.Fatalf("file was modified: %q", data)
	}
}

func TestContinueAtRejectsWrongContentRange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-2/6")
		w.WriteHeader(http.StatusPartialContent)
		fmt.Fprint(w, "abc")
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "download.txt")
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runGocurl(t, "-sS", "-C", "-", "-o", path, srv.URL)
	if code != 33 || !strings.Contains(stderr, "Content-Range") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "abc" {
		t.Fatalf("file was modified: %q", data)
	}
}

func TestParallelContinueAt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 3-5/6")
		w.WriteHeader(http.StatusPartialContent)
		fmt.Fprint(w, "def")
	}))
	defer srv.Close()
	dir := t.TempDir()
	one := filepath.Join(dir, "one")
	two := filepath.Join(dir, "two")
	code, _, stderr := runGocurl(t, "-sS", "-Z", "-C", "3", "-o", one, "-o", two,
		srv.URL+"/one", srv.URL+"/two")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	for _, path := range []string{one, two} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "def" {
			t.Fatalf("%s = %q err=%v", path, data, err)
		}
	}
}
