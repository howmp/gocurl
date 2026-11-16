package output

import (
	"context"
	"errors"
	"gocurl/internal/config"
	"gocurl/internal/exitcode"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
)

func TestRemoteNameRejectsEncodedTraversal(t *testing.T) {
	u, err := url.Parse("http://evil/%252e%252e%252f%252e%252e%252fhome%252fadmin%252f.bashrc")
	if err != nil {
		t.Fatal(err)
	}
	name, err := RemoteName(u)
	if err != nil {
		t.Fatal(err)
	}
	if name != ".bashrc" {
		t.Fatalf("name = %q", name)
	}
	if filepath.Base(name) != name {
		t.Fatalf("unsafe name = %q", name)
	}
}

func TestOutputWriterRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	o := config.NewOptions()
	o.Outputs = []string{link}
	if _, closeOut, err := OutputWriter(o, &url.URL{Scheme: "https", Host: "example.test"}, 0, io.Discard); err == nil {
		_ = closeOut()
		t.Fatal("expected symlink output to be rejected")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "keep" {
		t.Fatalf("target changed: %q err=%v", data, err)
	}
}

func TestWriteFileRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := WriteFile(link, []byte("overwrite")); err == nil {
		t.Fatal("expected symlink write to be rejected")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "keep" {
		t.Fatalf("target changed: %q err=%v", data, err)
	}
}

type failingReadCloser struct{ err error }

func (f failingReadCloser) Read([]byte) (int, error) { return 0, f.err }
func (f failingReadCloser) Close() error             { return nil }

func TestEncodingReadCloserPassesThroughTransportErrors(t *testing.T) {
	for _, want := range []error{io.ErrUnexpectedEOF, context.DeadlineExceeded, syscall.ECONNRESET, syscall.EPIPE} {
		rc := &encodingReadCloser{ReadCloser: failingReadCloser{err: want}, encoding: "gzip"}
		_, err := rc.Read(make([]byte, 1))
		if !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
		var encErr *ContentEncodingError
		if errors.As(err, &encErr) {
			t.Fatalf("transport error was wrapped as encoding error: %v", err)
		}
	}
}

func TestEncodingReadCloserWrapsDecodeErrors(t *testing.T) {
	rc := &encodingReadCloser{ReadCloser: failingReadCloser{err: errors.New("invalid gzip header")}, encoding: "gzip"}
	_, err := rc.Read(make([]byte, 1))
	var encErr *ContentEncodingError
	if !errors.As(err, &encErr) {
		t.Fatalf("err = %v, want ContentEncodingError", err)
	}
	if encErr.ExitCode() != 61 {
		t.Fatalf("exit code = %d, want 61", encErr.ExitCode())
	}
	if !errors.Is(err, encErr.Err) {
		t.Fatalf("Unwrap broken: %v", err)
	}
}

func TestRateReaderHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := NewRateReader(ctx, strings.NewReader("hello"), 1)
	start := time.Now()
	if _, err := r.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("rate limit ignored context cancellation")
	}
}

type closeTrackingReader struct {
	closed bool
}

func (r *closeTrackingReader) Read([]byte) (int, error) { return 0, io.EOF }
func (r *closeTrackingReader) Close() error             { r.closed = true; return nil }

func TestRateReaderClosesInner(t *testing.T) {
	inner := &closeTrackingReader{}
	r := NewRateReader(context.Background(), inner, 0)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if !inner.closed {
		t.Fatal("inner reader was not closed")
	}
}

type failingWriteCloser struct{}

func (failingWriteCloser) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestStdoutFlushErrorIsWriteError(t *testing.T) {
	o := config.NewOptions()
	w, closeOut, err := OutputWriter(o, &url.URL{Scheme: "https", Host: "example.test"}, 0, failingWriteCloser{})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("data"))
	if err := closeOut(); err == nil || exitcode.From(err) != 23 {
		t.Fatalf("err=%v, want exit code 23", err)
	}
}

func TestWriteFileForcesPrivateMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jar")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestOutputWriterAllowsDevFd(t *testing.T) {
	if _, err := os.Stat("/dev/fd/1"); err != nil {
		t.Skip("/dev/fd not available")
	}
	o := config.NewOptions()
	o.Outputs = []string{"/dev/fd/1"}
	_, closeOut, err := OutputWriter(o, &url.URL{Scheme: "https", Host: "example.test"}, 0, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	_ = closeOut()
}

func TestOutputWriterAllowsDevStdout(t *testing.T) {
	if _, err := os.Stat("/dev/stdout"); err != nil {
		t.Skip("/dev/stdout not available")
	}
	o := config.NewOptions()
	o.Outputs = []string{"/dev/stdout"}
	_, closeOut, err := OutputWriter(o, &url.URL{Scheme: "https", Host: "example.test"}, 0, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	_ = closeOut()
}

func TestVerboseUsesExplicitHTTPVersion(t *testing.T) {
	o := config.NewOptions()
	o.HttpVersion = "1.0"
	req, err := fhttp.NewRequest("GET", "https://example.test/", nil)
	if err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	PrintRequestVerbose(&buf, req, o)
	if !strings.HasPrefix(buf.String(), "> GET / HTTP/1.0") {
		t.Fatalf("verbose = %q", buf.String())
	}
}
