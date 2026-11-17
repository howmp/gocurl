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
	"runtime"
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

func TestWriteFileAtomicReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jar")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

func TestOutputWriterKeepsExistingMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := config.NewOptions()
	o.Outputs = []string{path}
	w, closeOut, err := OutputWriter(o, &url.URL{Scheme: "https", Host: "example.test"}, 0, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("new")); err != nil {
		t.Fatal(err)
	}
	if err := closeOut(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %o, want 644", info.Mode().Perm())
	}
}

func TestOutputWriterResumeRejectsChangedSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := config.NewOptions()
	o.Outputs = []string{path}
	o.ContinueAtSet = true
	o.ResumeOffset = 3
	o.AppendOutput = true
	if err := os.WriteFile(path, []byte("abcd"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, closeOut, err := OutputWriter(o, &url.URL{Scheme: "https", Host: "example.test"}, 0, io.Discard)
	if err == nil {
		_ = closeOut()
		t.Fatal("expected resume size mismatch")
	}
	if code := exitcode.From(err); code != exitcode.RangeError {
		t.Fatalf("exit code = %d, err = %v", code, err)
	}
}

func TestOutputWriterRefusesParentSymlink(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("parent symlink rejection is enforced with openat2 on Linux")
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	o := config.NewOptions()
	o.Outputs = []string{filepath.Join(link, "out")}
	if _, closeOut, err := OutputWriter(o, &url.URL{Scheme: "https", Host: "example.test"}, 0, io.Discard); err == nil {
		_ = closeOut()
		t.Fatal("expected parent symlink rejection")
	}
	if _, err := os.Stat(filepath.Join(real, "out")); !os.IsNotExist(err) {
		t.Fatalf("symlinked parent was written through: %v", err)
	}
}

func TestResolveResumeOffsetUsesOpenedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	if err := os.WriteFile(path, []byte("abcd"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := config.NewOptions()
	o.Outputs = []string{path}
	o.ContinueAtSet = true
	o.ResumeOffset = -1
	offset, err := ResolveResumeOffset(o, &url.URL{Scheme: "https", Host: "example.test"}, 0)
	if err != nil || offset != 4 {
		t.Fatalf("offset=%d err=%v", offset, err)
	}
	if o.ResumeOffset != -1 {
		t.Fatalf("resume offset leaked into shared options: %d", o.ResumeOffset)
	}
}

func TestResolveResumeOffsetIsPerURL(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first")
	second := filepath.Join(dir, "second")
	if err := os.WriteFile(first, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := config.NewOptions()
	o.Outputs = []string{first, second}
	o.ContinueAtSet = true
	o.ResumeOffset = -1
	u, _ := url.Parse("https://example.test/a")

	if got, err := ResolveResumeOffset(o, u, 0); err != nil || got != 3 {
		t.Fatalf("first offset = %d err = %v", got, err)
	}
	if got, err := ResolveResumeOffset(o, u, 1); err != nil || got != 10 {
		t.Fatalf("second offset = %d err = %v", got, err)
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

func TestWriteFileRejectsSymlinkedParent(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := WriteFile(filepath.Join(link, "cookies.txt"), []byte("data")); err == nil {
		t.Fatal("symlinked parent accepted")
	}
	if _, err := os.Stat(filepath.Join(real, "cookies.txt")); !os.IsNotExist(err) {
		t.Fatalf("write escaped into the symlink target: %v", err)
	}
}

func TestWriteFileWritesRegularPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.txt")
	if err := WriteFile(path, []byte("data")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "data" {
		t.Fatalf("content = %q err = %v", data, err)
	}
}
