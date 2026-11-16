package app

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"gocurl/internal/config"
	"gocurl/internal/exitcode"
	"gocurl/internal/output"

	fhttp "github.com/bogdanfinn/fhttp"
)

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) { return 0, syscall.EPIPE }

func TestEmitResponseKeepsWriteErrorAfterDeadline(t *testing.T) {
	o := config.NewOptions()
	o.MaxTime = time.Second
	o.NoBuffer = true
	u, _ := url.Parse("http://example.test/")
	resp := &fhttp.Response{
		StatusCode:    200,
		ContentLength: 7,
		Body:          io.NopCloser(strings.NewReader("payload")),
		Request:       &fhttp.Request{URL: u},
	}
	info := &output.ResponseInfo{}
	err := emitResponse(o, o, u, 0, errorWriter{}, resp, info, context.Background(), time.Now().Add(-2*time.Second))
	if code := exitcode.From(err); code != exitcode.WriteFailure {
		t.Fatalf("exit code = %d, err = %v", code, err)
	}
}

func TestDaemonConcurrentBodies(t *testing.T) {
	setupDaemonTest(t)
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		count.Add(1)
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	body := filepath.Join(t.TempDir(), "body.bin")
	if err := os.WriteFile(body, make([]byte, 2<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"-sS", "-Z", "-d", "@" + body}
	for i := 0; i < 8; i++ {
		args = append(args, srv.URL)
	}
	code, _, stderr := runGocurlMode(t, true, args...)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if got := count.Load(); got != 8 {
		t.Fatalf("requests = %d, want 8", got)
	}
}

func TestDaemonTLSInsecureVerifyResult(t *testing.T) {
	setupDaemonTest(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	code, out, stderr := runGocurlMode(t, true, "-sS", "-k", "-w", "%{ssl_verify_result}", srv.URL)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	_, directOut, _ := runGocurl(t, "-sS", "-k", "-w", "%{ssl_verify_result}", srv.URL)
	if out != "ok18" || directOut != "ok18" {
		t.Fatalf("daemon ssl_verify_result = %q, direct = %q, want ok18", out, directOut)
	}
}

func TestDaemonEnvelopeRedirects(t *testing.T) {
	setupDaemonTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/one":
			http.Redirect(w, r, "/two", http.StatusFound)
		case "/two":
			http.Redirect(w, r, "/three", http.StatusFound)
		default:
			fmt.Fprint(w, "ok")
		}
	}))
	defer srv.Close()

	code, out, stderr := runGocurlMode(t, true, "-sS", "-L", "--json-envelope", srv.URL+"/one")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	response := env["response"].(map[string]any)
	redirects, ok := response["redirects"].([]any)
	if !ok || len(redirects) != 3 {
		t.Fatalf("redirects = %#v", response["redirects"])
	}
	if !strings.HasSuffix(redirects[2].(string), "/three") {
		t.Fatalf("redirects = %#v", redirects)
	}
}

func TestUploadResumeUsesContentRange(t *testing.T) {
	var body string
	var contentRange string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		body = string(data)
		contentRange = r.Header.Get("Content-Range")
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	file := filepath.Join(t.TempDir(), "upload.bin")
	if err := os.WriteFile(file, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runGocurl(t, "-sS", "-C", "4", "-T", file, srv.URL)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if body != "456789" {
		t.Fatalf("body = %q", body)
	}
	if contentRange != "bytes 4-9/10" {
		t.Fatalf("Content-Range = %q", contentRange)
	}
}

func TestUploadResumeDashRejected(t *testing.T) {
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
	}))
	defer srv.Close()

	file := filepath.Join(t.TempDir(), "upload.bin")
	if err := os.WriteFile(file, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, _ := runGocurl(t, "-sS", "-C", "-", "-T", file, srv.URL)
	if code == 0 {
		t.Fatal("expected -C - upload to fail")
	}
	if count.Load() != 0 {
		t.Fatalf("requests = %d, want 0", count.Load())
	}
}

func TestEnvelopeHeadWithOutputDoesNotTruncate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "1")
	}))
	defer srv.Close()

	target := filepath.Join(t.TempDir(), "keep.txt")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, _ := runGocurl(t, "-sS", "--json-envelope", "-I", "-o", target, srv.URL)
	if code == 0 {
		t.Fatal("expected envelope HEAD with -o to fail")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatalf("file = %q", data)
	}
}

func TestParallelManyURLsCreatesTempsLazily(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "x")
	}))
	defer srv.Close()

	args := []string{"-sS", "-Z", "--parallel-max", "20"}
	for i := 0; i < 200; i++ {
		args = append(args, srv.URL)
	}
	code, out, stderr := runGocurl(t, args...)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if len(out) != 200 {
		t.Fatalf("output length = %d, want 200", len(out))
	}
}

func TestEmptyURLReturnsThree(t *testing.T) {
	code, _, _ := runGocurl(t, "")
	if code != 3 {
		t.Fatalf("exit = %d, want 3", code)
	}
}

func TestTLSStatePopulatedDirectAndDaemon(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(ca, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	code, out, stderr := runGocurl(t, "-sS", "--cacert", ca, "-w", "%{ssl_verify_result}", srv.URL)
	if code != 0 || out != "ok0" {
		t.Fatalf("direct: exit=%d out=%q stderr=%q", code, out, stderr)
	}
	code, out, stderr = runGocurlMode(t, true, "-sS", "--cacert", ca, "-w", "%{ssl_verify_result}", srv.URL)
	if code != 0 || out != "ok0" {
		t.Fatalf("daemon: exit=%d out=%q stderr=%q", code, out, stderr)
	}

	code, out, stderr = runGocurl(t, "-sS", "--cacert", ca, "-w", "%{tls_version}", srv.URL)
	if code != 0 || !strings.HasPrefix(out, "okTLS") {
		t.Fatalf("direct TLS version: exit=%d out=%q stderr=%q", code, out, stderr)
	}
	code, out, stderr = runGocurlMode(t, true, "-sS", "--cacert", ca, "-w", "%{tls_version}", srv.URL)
	if code != 0 || !strings.HasPrefix(out, "okTLS") {
		t.Fatalf("daemon TLS version: exit=%d out=%q stderr=%q", code, out, stderr)
	}

	code, out, stderr = runGocurlMode(t, true, "-sS", "--cacert", ca, "--json-envelope", srv.URL)
	if code != 0 {
		t.Fatalf("envelope: exit=%d stderr=%q", code, stderr)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	response := env["response"].(map[string]any)
	tlsInfo, ok := response["tls"].(map[string]any)
	if !ok {
		t.Fatalf("missing TLS info: %#v", response)
	}
	if tlsInfo["verified"] != true {
		t.Fatalf("TLS not verified: %#v", tlsInfo)
	}
	certs, ok := tlsInfo["peer_certificates"].([]any)
	if !ok || len(certs) == 0 {
		t.Fatalf("missing peer certificates: %#v", tlsInfo)
	}
}
