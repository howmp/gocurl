package app

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"gocurl/internal/config"
	"gocurl/internal/httpx"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
)

func TestProxyHTTP(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "target-body")
	}))
	defer target.Close()
	proxy, seen, _ := newConnectProxy(t)
	defer proxy.Close()

	code, out, stderr := runGocurl(t, "-sS", "-x", proxy.URL, target.URL)
	if code != 0 || out != "target-body" {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
	if !seen.Load() {
		t.Fatal("proxy did not see CONNECT")
	}
}

func TestProxyBypass(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "direct")
	}))
	defer target.Close()
	proxy, seen, _ := newConnectProxy(t)
	defer proxy.Close()

	code, out, stderr := runGocurl(t, "-sS", "-x", proxy.URL, "--noproxy", "127.0.0.1", target.URL)
	if code != 0 || out != "direct" {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
	if seen.Load() {
		t.Fatal("proxy was used despite --noproxy")
	}
}

func TestProxyAuth(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer target.Close()
	proxy, _, auth := newConnectProxy(t)
	defer proxy.Close()

	code, out, stderr := runGocurl(t, "-sS", "-x", proxy.URL, "-U", "user:pass", target.URL)
	if code != 0 || out != "ok" {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
	if v, _ := auth.Load().(string); v == "" {
		t.Fatal("proxy did not receive Proxy-Authorization")
	}
}

func TestProxyHTTPSConnect(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "secure")
	}))
	defer target.Close()
	proxy, seen, _ := newConnectProxy(t)
	defer proxy.Close()

	code, out, stderr := runGocurl(t, "-sk", "-x", proxy.URL, target.URL)
	if code != 0 || out != "secure" {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
	if !seen.Load() {
		t.Fatal("proxy did not see CONNECT")
	}
}

func newConnectProxy(t *testing.T) (*httptest.Server, *atomic.Bool, *atomic.Value) {
	t.Helper()
	var seen atomic.Bool
	var auth atomic.Value
	auth.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		seen.Store(true)
		if v := r.Header.Get("Proxy-Authorization"); v != "" {
			auth.Store(v)
		}
		dest, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijack unsupported", http.StatusInternalServerError)
			return
		}
		client, _, err := hj.Hijack()
		if err != nil {
			return
		}
		_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		go func() {
			_, _ = io.Copy(dest, client)
			_ = dest.Close()
		}()
		_, _ = io.Copy(client, dest)
		_ = client.Close()
	}))
	return srv, &seen, &auth
}

func TestCookieJar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/set":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc", Path: "/"})
			http.Redirect(w, r, "/check", http.StatusFound)
		case "/check":
			fmt.Fprint(w, r.Header.Get("Cookie"))
		}
	}))
	defer srv.Close()

	jarFile := filepath.Join(t.TempDir(), "cookies.txt")
	code, out, stderr := runGocurl(t, "-sS", "-L", "-c", jarFile, srv.URL+"/set")
	if code != 0 || !strings.Contains(out, "session=abc") {
		t.Fatalf("set: exit=%d out=%q stderr=%q", code, out, stderr)
	}
	data, err := os.ReadFile(jarFile)
	if err != nil || !strings.Contains(string(data), "session") {
		t.Fatalf("jar file: %v %q", err, data)
	}

	code, out, stderr = runGocurl(t, "-sS", "-b", jarFile, srv.URL+"/check")
	if code != 0 || !strings.Contains(out, "session=abc") {
		t.Fatalf("load: exit=%d out=%q stderr=%q", code, out, stderr)
	}

	code, out, stderr = runGocurl(t, "-sS", "-b", "a=1; b=2", srv.URL+"/check")
	if code != 0 || !strings.Contains(out, "a=1") || !strings.Contains(out, "b=2") {
		t.Fatalf("cookie string: exit=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestClientCertificate(t *testing.T) {
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

	code, out, stderr := runGocurl(t, "-sk", "--cert", certFile, "--key", keyFile, srv.URL)
	if code != 0 || out != "certs=1" {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestConfigFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.Header.Get("X-Config"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	file := filepath.Join(dir, "curlrc")
	content := fmt.Sprintf("silent\nheader = \"X-Config: yes\"\nurl = %q\n", srv.URL)
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runGocurl(t, "--config", file)
	if code != 0 || out != "yes" {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
	code, out, stderr = runGocurl(t, "-K", file)
	if code != 0 || out != "yes" {
		t.Fatalf("-K: exit=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestParallel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		fmt.Fprintf(w, "body-%s", strings.TrimPrefix(r.URL.Path, "/"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	code, _, stderr := runGocurl(t, "-sS", "--parallel", "--parallel-max", "2",
		"-o", a, "-o", b, srv.URL+"/a", srv.URL+"/b")
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

func TestHeaderOrderHTTP1(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	got := make(chan []string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		if _, err := reader.ReadString('\n'); err != nil {
			return
		}
		var names []string
		for {
			line, err := reader.ReadString('\n')
			if err != nil || line == "\r\n" {
				break
			}
			name, _, ok := strings.Cut(line, ":")
			if ok {
				names = append(names, strings.ToLower(strings.TrimSpace(name)))
			}
		}
		got <- names
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"))
	}()

	code, out, stderr := runGocurl(t, "-sS", "http://"+ln.Addr().String()+"/")
	if code != 0 || out != "ok" {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
	names := <-got
	want := []string{
		"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform",
		"upgrade-insecure-requests", "user-agent", "accept",
		"sec-fetch-site", "sec-fetch-mode", "sec-fetch-user",
		"sec-fetch-dest", "accept-encoding", "accept-language",
		"priority", "cache-control", "host",
	}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("header order:\n got %v\nwant %v", names, want)
	}
}

func TestHeaderOrderWithReferer(t *testing.T) {
	o := config.NewOptions()
	o.Headers = []config.HeaderArg{{Name: "Referer", Value: "https://example.com/"}}
	req, err := fhttp.NewRequest("GET", "https://example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	httpx.ApplyHeaders(req, o, "", nil)
	order := req.Header[fhttp.HeaderOrderKey]
	refIdx, encIdx := -1, -1
	for i, name := range order {
		if name == "referer" {
			refIdx = i
		}
		if name == "accept-encoding" {
			encIdx = i
		}
	}
	if refIdx < 0 || encIdx < 0 || refIdx > encIdx {
		t.Fatalf("order = %v", order)
	}
	orderMap := make(map[string]int, len(order))
	for i, name := range order {
		orderMap[name] = i
	}
	kvs, _ := req.Header.SortedKeyValuesBy(orderMap, nil)
	var got []string
	for _, kv := range kvs {
		got = append(got, kv.Key)
	}
	gotIndex := make(map[string]int, len(got))
	for i, name := range got {
		gotIndex[strings.ToLower(name)] = i
	}
	refIdx, ok := gotIndex["referer"]
	if !ok {
		t.Fatalf("sorted order missing referer: %v", got)
	}
	encIdx, ok = gotIndex["accept-encoding"]
	if !ok || refIdx > encIdx {
		t.Fatalf("sorted order = %v", got)
	}
}

func makeClientCert(t *testing.T) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "gocurl-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

func TestCookieJarExportsRedirectTargetCookies(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "target", Value: "1", Path: "/"})
		fmt.Fprint(w, "ok")
	}))
	defer backend.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(backend.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://target.test:"+port+"/set", http.StatusFound)
	}))
	defer redirector.Close()

	jar := filepath.Join(t.TempDir(), "cookies.txt")
	code, _, stderr := runGocurl(t, "-sS", "-L",
		"--resolve", "target.test:"+port+":127.0.0.1",
		"-c", jar, redirector.URL)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	data, err := os.ReadFile(jar)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "target.test") {
		t.Fatalf("redirect target cookie missing: %q", data)
	}
}

func TestCookieJarResavePreservesUnvisitedHosts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	jar := filepath.Join(t.TempDir(), "cookies.txt")
	input := "other.test\tFALSE\t/\tFALSE\t0\tother\t1\n"
	if err := os.WriteFile(jar, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runGocurl(t, "-sS", "-b", jar, "-c", jar, srv.URL)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	data, err := os.ReadFile(jar)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "other.test") {
		t.Fatalf("unvisited host cookie was dropped: %q", data)
	}
}
