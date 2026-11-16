package app

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFormMultipart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		file, header, err := r.FormFile("upload")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer file.Close()
		data := make([]byte, 64)
		n, _ := file.Read(data)
		fmt.Fprintf(w, "%s|%s|%s|%s", r.FormValue("field"), header.Filename, data[:n], r.Header.Get("Content-Type"))
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("file-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runGocurl(t, "-sS",
		"-F", "field=value",
		"-F", "upload=@"+path+";type=text/plain",
		srv.URL,
	)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(out, "value|sample.txt|file-content|multipart/form-data; boundary=") {
		t.Fatalf("unexpected response: %q", out)
	}
}

func TestFormString(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		fmt.Fprint(w, r.FormValue("raw"))
	}))
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sS", "--form-string", "raw=@not-a-file", srv.URL)
	if code != 0 || out != "@not-a-file" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestResolveHTTPAndSNI(t *testing.T) {
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.Host)
	}))
	defer httpSrv.Close()
	httpAddr := strings.TrimPrefix(httpSrv.URL, "http://")
	_, httpPort, _ := net.SplitHostPort(httpAddr)
	httpURL := "http://example.test:" + httpPort + "/"
	resolve := "example.test:" + httpPort + ":127.0.0.1"
	code, out, stderr := runGocurl(t, "-sS", "--resolve", resolve, httpURL)
	if code != 0 || out != "example.test:"+httpPort {
		t.Fatalf("http resolve code=%d out=%q stderr=%q", code, out, stderr)
	}

	tlsSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s|%s|%d", r.Host, r.TLS.ServerName, r.ProtoMajor)
	}))
	tlsSrv.EnableHTTP2 = true
	tlsSrv.StartTLS()
	defer tlsSrv.Close()
	tlsAddr := strings.TrimPrefix(tlsSrv.URL, "https://")
	_, tlsPort, _ := net.SplitHostPort(tlsAddr)
	tlsURL := "https://example.test:" + tlsPort + "/"
	resolve = "example.test:" + tlsPort + ":127.0.0.1"
	code, out, stderr = runGocurl(t, "-sS", "-k", "--resolve", resolve, tlsURL)
	if code != 0 || out != "example.test:"+tlsPort+"|example.test|2" {
		t.Fatalf("tls resolve code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestResolveDaemon(t *testing.T) {
	setupDaemonTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.Host)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	_, port, _ := net.SplitHostPort(addr)
	target := "http://daemon-test.test:" + port + "/"
	code, out, stderr := runGocurlMode(t, true, "-sS", "--resolve", "daemon-test.test:"+port+":127.0.0.1", target)
	if code != 0 || out != "daemon-test.test:"+port {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestFailWithBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing-body", http.StatusNotFound)
	}))
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sS", "--fail-with-body",
		"-w", "\ncode=%{http_code} size=%{size_download}\n", srv.URL)
	if code != 22 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if out != "missing-body\n\ncode=404 size=13\n" {
		t.Fatalf("out=%q", out)
	}

	code, out, _ = runGocurl(t, "-sS", "-f", "-i", srv.URL)
	if code != 22 || !strings.Contains(out, "HTTP/1.1 404") || strings.Contains(out, "missing-body") {
		t.Fatalf("fail headers code=%d out=%q", code, out)
	}

	code, out, _ = runGocurl(t, "-sS", "-f", "-w", "\ncode=%{http_code} size=%{size_download}\n", srv.URL)
	if code != 22 || out != "\ncode=404 size=0\n" {
		t.Fatalf("fail write-out code=%d out=%q", code, out)
	}

	code, _, stderr = runGocurl(t, "-sS", "-f", "--fail-with-body", srv.URL)
	if code != 2 || !strings.Contains(stderr, "badly used") {
		t.Fatalf("conflict code=%d stderr=%q", code, stderr)
	}
}

func TestRetryStatusAndBodyReplay(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		body := make([]byte, 32)
		read, _ := r.Body.Read(body)
		if n < 3 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintf(w, "ok:%s", body[:read])
	}))
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sS", "--retry", "2", "--retry-delay", "0.01", "-d", "payload", srv.URL)
	if code != 0 || out != "ok:payload" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("attempts=%d, want 3", got)
	}

	atomic.StoreInt32(&attempts, 0)
	setupDaemonTest(t)
	code, out, stderr = runGocurlMode(t, true, "-sS", "--retry", "2", "--retry-delay", "0.01", "-d", "payload", srv.URL)
	if code != 0 || out != "ok:payload" {
		t.Fatalf("daemon retry code=%d out=%q stderr=%q", code, out, stderr)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("daemon attempts=%d, want 3", got)
	}
}

func TestResolveWithProxyAllowed(t *testing.T) {
	code, _, stderr := runGocurl(t, "-sS",
		"--resolve", "example.test:80:127.0.0.1",
		"-x", "http://127.0.0.1:1",
		"http://example.test/",
	)
	if code != 7 || strings.Contains(stderr, "cannot be combined") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestHTTPOnlyOptionsRejectedForFTP(t *testing.T) {
	code, _, stderr := runGocurl(t, "-sS", "--retry", "1", "ftp://example.test/")
	if code != 1 || !strings.Contains(stderr, "only supported for HTTP/HTTPS") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestParseErrorsRedactSensitiveValues(t *testing.T) {
	code, _, stderr := runGocurl(t, "-sS", "-H", "Authorization: Bearer SECRET\r\nX: 1", "http://127.0.0.1:1/")
	if code == 0 || strings.Contains(stderr, "SECRET") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	code, _, stderr = runGocurl(t, "-sS", "-F", `a=v;filename="x\r\nY: SECRET"`, "http://127.0.0.1:1/")
	if code == 0 || strings.Contains(stderr, "SECRET") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestMaxFilesizeRejectedForFTP(t *testing.T) {
	code, _, stderr := runGocurl(t, "-sS", "--max-filesize", "1k", "ftp://example.test/")
	if code != 1 || !strings.Contains(stderr, "not supported for FTP") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestRange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=2-5" {
			http.Error(w, "range missing", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusPartialContent)
		fmt.Fprint(w, "3456")
	}))
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sS", "-r", "2-5", srv.URL)
	if code != 0 || out != "3456" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	code, _, _ = runGocurl(t, "-sS", "--range", "bad", srv.URL)
	if code != 2 {
		t.Fatalf("invalid range code=%d, want 2", code)
	}
}
