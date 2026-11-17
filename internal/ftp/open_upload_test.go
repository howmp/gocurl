//go:build linux

package ftp

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenUploadFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.bin")
	if err := os.WriteFile(target, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.bin")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := ftpResumeReader(link, 0); err == nil {
		t.Fatal("symlink upload source was opened")
	}
}

func TestOpenUploadFileRejectsDirectory(t *testing.T) {
	if _, err := ftpResumeReader(t.TempDir(), 0); err == nil {
		t.Fatal("directory upload source was opened")
	}
}

func TestFTPResumeReaderRejectsOffsetBeyondSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "small.bin")
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ftpResumeReader(path, 10); err == nil {
		t.Fatal("offset beyond file size accepted")
	}
	reader, err := ftpResumeReader(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "bc" {
		t.Fatalf("resumed content = %q, want %q", data, "bc")
	}
}
