//go:build linux

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigIncludeRejectsSymlinkedEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "cfg")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret.conf")
	if err := os.WriteFile(secret, []byte("--url http://evil.test/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if _, _, _, err := resolveConfigPath("link/secret.conf", 1, root, root); err == nil {
		t.Fatal("include through symlinked directory was accepted")
	}
}

func TestConfigIncludeAcceptsPathInsideRoot(t *testing.T) {
	root := t.TempDir()
	inner := filepath.Join(root, "inner.conf")
	if err := os.WriteFile(inner, []byte("--url http://ok.test/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, _, _, err := resolveConfigPath("inner.conf", 1, root, root)
	if err != nil {
		t.Fatalf("valid include rejected: %v", err)
	}
	if resolved != inner {
		t.Fatalf("resolved = %q, want %q", resolved, inner)
	}
}
