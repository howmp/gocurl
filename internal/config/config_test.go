package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandConfigArgsIsOptionAware(t *testing.T) {
	cases := [][]string{
		{"--", "--config=/definitely/missing"},
		{"-d", "--config=/definitely/missing"},
		{"--url", "--config=/definitely/missing"},
	}
	for _, args := range cases {
		got, err := ExpandConfigArgs(args)
		if err != nil {
			t.Fatalf("ExpandConfigArgs(%v): %v", args, err)
		}
		if len(got) != len(args) {
			t.Fatalf("ExpandConfigArgs(%v) = %v", args, got)
		}
		for i := range args {
			if got[i] != args[i] {
				t.Fatalf("ExpandConfigArgs(%v) = %v", args, got)
			}
		}
	}
}

func TestExpandConfigArgsMissingParameter(t *testing.T) {
	if _, err := ExpandConfigArgs([]string{"--config"}); err == nil {
		t.Fatal("expected missing parameter error")
	}
}

func TestExpandConfigArgsNestedRelative(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(sub, "child.conf")
	if err := os.WriteFile(child, []byte("silent\nheader = \"X-Child: yes\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rootFile := filepath.Join(root, "root.conf")
	if err := os.WriteFile(rootFile, []byte("config sub/child.conf\nurl = \"http://example.test/\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ExpandConfigArgs([]string{"--config", rootFile})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{"silent", "--header=X-Child: yes", "--url=http://example.test/"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expanded args missing %q: %v", want, got)
		}
	}
}

func TestExpandConfigArgsRejectsEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside.conf")
	if err := os.WriteFile(outside, []byte("silent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rootFile := filepath.Join(root, "root.conf")
	if err := os.WriteFile(rootFile, []byte("config ../outside.conf\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExpandConfigArgs([]string{"--config", rootFile}); err == nil {
		t.Fatal("expected include escape error")
	}
}

func TestExpandConfigArgsRejectsNestedStdin(t *testing.T) {
	rootFile := filepath.Join(t.TempDir(), "root.conf")
	if err := os.WriteFile(rootFile, []byte("config -\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExpandConfigArgs([]string{"--config", rootFile}); err == nil {
		t.Fatal("expected nested stdin error")
	}
}

func TestExpandConfigArgsRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.conf")
	if err := os.WriteFile(target, []byte("silent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.conf")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ExpandConfigArgs([]string{"--config", link}); err == nil {
		t.Fatal("expected symlink rejection")
	}
}

func TestExpandConfigArgsRejectsTooManyLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge.conf")
	var b strings.Builder
	for i := 0; i <= maxConfigLines; i++ {
		b.WriteString("silent\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExpandConfigArgs([]string{"--config", path}); err == nil {
		t.Fatal("expected line budget error")
	}
}
