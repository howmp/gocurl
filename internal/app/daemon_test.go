package app

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gocurl/internal/daemon"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--daemon" {
		os.Exit(daemon.Run(os.Args[2:]))
	}
	os.Exit(m.Run())
}

func setupDaemonTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	_ = os.Chmod(dir, 0o700)
	socket := filepath.Join(dir, "daemon.sock")
	t.Setenv("GOCURL_SOCKET", socket)
	t.Setenv("GOCURL_DAEMON_LOG", filepath.Join(dir, "daemon.log"))
	t.Setenv("GOCURL_DAEMON_IDLE", "30s")
	t.Setenv("GOCURL_NO_DAEMON", "")
	t.Cleanup(func() { _ = daemon.Stop(socket) })
	return socket
}

func TestDaemonConnectionReuse(t *testing.T) {
	socket := setupDaemonTest(t)
	var mu sync.Mutex
	newConnections := 0
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.URL.Path)
	}))
	srv.EnableHTTP2 = true
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			mu.Lock()
			newConnections++
			mu.Unlock()
		}
	}
	srv.StartTLS()
	defer srv.Close()

	dir := t.TempDir()
	code, _, stderr := runGocurlMode(t, true, "-sk", "-o", filepath.Join(dir, "one"), srv.URL+"/one")
	if code != 0 {
		t.Fatalf("first exit=%d stderr=%q", code, stderr)
	}
	code, _, stderr = runGocurlMode(t, true, "-sk", "-o", filepath.Join(dir, "two"), srv.URL+"/two")
	if code != 0 {
		t.Fatalf("second exit=%d stderr=%q", code, stderr)
	}
	mu.Lock()
	count := newConnections
	mu.Unlock()
	if count != 1 {
		t.Fatalf("upstream connections = %d, want 1 (socket=%s)", count, socket)
	}
}

func TestDaemonCookiePersistence(t *testing.T) {
	setupDaemonTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/set":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc", Path: "/"})
			fmt.Fprint(w, "set")
		case "/check":
			fmt.Fprint(w, r.Header.Get("Cookie"))
		}
	}))
	defer srv.Close()

	code, _, stderr := runGocurlMode(t, true, "-sS", "-o", filepath.Join(t.TempDir(), "set.out"), srv.URL+"/set")
	if code != 0 {
		t.Fatalf("set exit=%d stderr=%q", code, stderr)
	}
	code, out, stderr := runGocurlMode(t, true, "-sS", srv.URL+"/check")
	if code != 0 || !strings.Contains(out, "session=abc") {
		t.Fatalf("check exit=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestDaemonCookieDump(t *testing.T) {
	setupDaemonTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc", Path: "/"})
		fmt.Fprint(w, "body")
	}))
	defer srv.Close()

	code, out, stderr := runGocurlMode(t, true, "-sS", "-c", "-", "-o", filepath.Join(t.TempDir(), "out"), srv.URL)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(out, "# Netscape HTTP Cookie File") || !strings.Contains(out, "session") {
		t.Fatalf("cookie dump = %q", out)
	}
}

func TestDaemonParallel(t *testing.T) {
	setupDaemonTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "body-%s", strings.TrimPrefix(r.URL.Path, "/"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	code, _, stderr := runGocurlMode(t, true, "-sS", "-Z", "-o", a, "-o", b, srv.URL+"/a", srv.URL+"/b")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	for file, want := range map[string]string{a: "body-a", b: "body-b"} {
		data, err := os.ReadFile(file)
		if err != nil || string(data) != want {
			t.Fatalf("%s: %v %q", file, err, data)
		}
	}
}

func TestNoPersistDoesNotStartDaemon(t *testing.T) {
	socket := setupDaemonTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sS", srv.URL)
	if code != 0 || out != "ok" {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
	if _, err := os.Stat(socket); err == nil {
		t.Fatalf("daemon socket was created despite --no-persist: %s", socket)
	}
}

func TestDaemonProxy(t *testing.T) {
	setupDaemonTest(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "target")
	}))
	defer target.Close()
	proxy, seen, _ := newConnectProxy(t)
	defer proxy.Close()

	code, out, stderr := runGocurlMode(t, true, "-sS", "-x", proxy.URL, target.URL)
	if code != 0 || out != "target" {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
	if !seen.Load() {
		t.Fatal("proxy did not see CONNECT")
	}
}

func TestDaemonClientCertificate(t *testing.T) {
	setupDaemonTest(t)
	certPEM, keyPEM := makeClientCert(t)
	dir := t.TempDir()
	certFile := filepath.Join(dir, "client.pem")
	keyFile := filepath.Join(dir, "client.key")
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "certs=%d", len(r.TLS.PeerCertificates))
	}))
	srv.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert}
	srv.StartTLS()
	defer srv.Close()

	code, out, stderr := runGocurlMode(t, true, "-sk", "--cert", certFile, "--key", keyFile, srv.URL)
	if code != 0 || out != "certs=1" {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestDaemonIdleExit(t *testing.T) {
	socket := setupDaemonTest(t)
	t.Setenv("GOCURL_DAEMON_IDLE", "1s")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	if code, _, stderr := runGocurlMode(t, true, "-sS", srv.URL); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("daemon did not exit or remove socket: %s", socket)
}

func TestDaemonStopCommand(t *testing.T) {
	socket := setupDaemonTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	if code, _, stderr := runGocurlMode(t, true, "-sS", srv.URL); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if err := daemon.Stop(socket); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("socket still exists after stop: %s", socket)
}

func TestDaemonDetectsChunkedTruncation(t *testing.T) {
	setupDaemonTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("hijacking unsupported")
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n")
		_ = buf.Flush()
	}))
	defer srv.Close()

	code, out, stderr := runGocurlMode(t, true, "-sS", srv.URL)
	if code != 18 {
		t.Fatalf("exit=%d stderr=%q out=%q", code, stderr, out)
	}
	if out != "hello" {
		t.Fatalf("out=%q", out)
	}
}

func TestDaemonCookieFileIsNotShared(t *testing.T) {
	setupDaemonTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.Header.Get("Cookie"))
	}))
	defer srv.Close()

	host, _, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	jar := filepath.Join(t.TempDir(), "cookies.txt")
	if err := os.WriteFile(jar, []byte(host+"\tFALSE\t/\tFALSE\t0\tsid\tSECRET\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, out, stderr := runGocurlMode(t, true, "-sS", "-b", jar, srv.URL)
	if code != 0 || !strings.Contains(out, "sid=SECRET") {
		t.Fatalf("first run: exit=%d out=%q stderr=%q", code, out, stderr)
	}
	code, out, stderr = runGocurlMode(t, true, "-sS", srv.URL)
	if code != 0 || out != "" {
		t.Fatalf("cookie file leaked into daemon jar: exit=%d out=%q stderr=%q", code, out, stderr)
	}
}
