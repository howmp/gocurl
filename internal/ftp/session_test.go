package ftp

import (
	"bytes"
	"io"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"gocurl/internal/config"
	"gocurl/internal/exitcode"
)

type fakeSession struct {
	storedPath string
	storedOff  int64
	storedData string
	listNames  bool
	retrPath   string
	sizeVal    int64
	closeCount int
}

func (f *fakeSession) store(path string, off int64, r io.Reader) error {
	f.storedPath, f.storedOff = path, off
	b, err := io.ReadAll(r)
	f.storedData = string(b)
	return err
}
func (f *fakeSession) retrieve(path string, off int64, w io.Writer) (int64, error) {
	f.retrPath = path
	return io.Copy(w, strings.NewReader("payload"))
}
func (f *fakeSession) list(path string, names bool, w io.Writer) (int64, error) {
	f.listNames = names
	n, err := io.WriteString(w, "entry\n")
	return int64(n), err
}
func (f *fakeSession) size(string) (int64, error) { return f.sizeVal, nil }
func (f *fakeSession) mdtm(string) (time.Time, error) {
	return time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC), nil
}
func (f *fakeSession) Close() error { f.closeCount++; return nil }

func TestSessionUpload(t *testing.T) {
	o := config.NewOptions()
	o.UploadFile = "-"
	f := &fakeSession{}
	u, _ := url.Parse("ftp://example.test/file")
	if err := runFTPSession(o, u, 0, io.Discard, io.Discard, f, time.Now(), "addr", "file"); err != nil {
		t.Fatal(err)
	}
	if f.storedPath != "file" || f.storedOff != 0 {
		t.Fatalf("store called with %q offset %d", f.storedPath, f.storedOff)
	}
}

func TestSessionHead(t *testing.T) {
	o := config.NewOptions()
	o.Head = true
	f := &fakeSession{sizeVal: 42}
	u, _ := url.Parse("ftp://example.test/file")
	var out bytes.Buffer
	if err := runFTPSession(o, u, 0, &out, io.Discard, f, time.Now(), "addr", "/file"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Content-Length: 42") {
		t.Fatalf("missing size header: %q", out.String())
	}
}

func TestSessionListAndRetrieve(t *testing.T) {
	o := config.NewOptions()
	o.ListOnly = true
	f := &fakeSession{}
	u, _ := url.Parse("ftp://example.test/dir/")
	if err := runFTPSession(o, u, 0, io.Discard, io.Discard, f, time.Now(), "addr", "/dir/"); err != nil {
		t.Fatal(err)
	}
	if !f.listNames {
		t.Fatal("expected NLST")
	}
	o = config.NewOptions()
	f = &fakeSession{}
	u, _ = url.Parse("ftp://example.test/file")
	var out bytes.Buffer
	if err := runFTPSession(o, u, 0, &out, io.Discard, f, time.Now(), "addr", "/file"); err != nil {
		t.Fatal(err)
	}
	if f.retrPath != "/file" || out.String() != "payload" {
		t.Fatalf("retrieve path=%q out=%q", f.retrPath, out.String())
	}
}

func TestFTPWriteErrorMapsPipeAndDiskErrors(t *testing.T) {
	for _, err := range []error{syscall.EPIPE, syscall.ENOSPC, &os.PathError{Op: "write", Path: "out", Err: syscall.EROFS}} {
		coded := ftpWriteError(err)
		if code := exitcode.From(coded); code != exitcode.WriteFailure {
			t.Fatalf("err %v mapped to %d", err, code)
		}
	}
	if got := ftpWriteError(io.ErrUnexpectedEOF); got != io.ErrUnexpectedEOF {
		t.Fatalf("source error rewritten: %v", got)
	}
}
