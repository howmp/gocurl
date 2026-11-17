//go:build linux

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := ReadFile(link); err == nil {
		t.Fatal("symlink was followed")
	}
}

func TestReadFileRejectsOversizeInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big")
	data := make([]byte, MaxLocalInputBytes+1)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(path); err == nil {
		t.Fatal("oversized file accepted")
	}
}

func TestReadFileRejectsDirectory(t *testing.T) {
	if _, err := ReadFile(t.TempDir()); err == nil {
		t.Fatal("directory accepted")
	}
}

func TestTLSStateStoreSeparatesPorts(t *testing.T) {
	s := NewTLSStateStore()
	s.SetOrigin("example.test", "8443", TLSState{Version: 0x0304, Port: "8443"})
	s.SetOrigin("example.test", "9443", TLSState{Version: 0x0303, Port: "9443"})

	first, exact, ok := s.LookupOrigin("example.test", "8443")
	if !ok || !exact || first.Version != 0x0304 {
		t.Fatalf("8443 lookup = %+v exact=%v ok=%v", first, exact, ok)
	}
	second, exact, ok := s.LookupOrigin("example.test", "9443")
	if !ok || !exact || second.Version != 0x0303 {
		t.Fatalf("9443 lookup = %+v exact=%v ok=%v", second, exact, ok)
	}
}
