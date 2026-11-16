package httpx

import (
	"os"
	"path/filepath"
	"testing"

	"gocurl/internal/config"
)

func netrcOptions(t *testing.T, content string) *config.Options {
	t.Helper()
	path := filepath.Join(t.TempDir(), "netrc")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	o := config.NewOptions()
	o.NetrcFile = path
	return o
}

func TestNetrcMachineWinsOverDefault(t *testing.T) {
	o := netrcOptions(t, `default login defuser password defpass
machine example.test login machuser password machpass
`)
	user, pass, ok := netrcCredentials(o, "example.test")
	if !ok || user != "machuser" || pass != "machpass" {
		t.Fatalf("got %q/%q ok=%v", user, pass, ok)
	}
}

func TestNetrcFallsBackToDefault(t *testing.T) {
	o := netrcOptions(t, `default login defuser password defpass
machine example.test login machuser password machpass
`)
	user, pass, ok := netrcCredentials(o, "other.test")
	if !ok || user != "defuser" || pass != "defpass" {
		t.Fatalf("got %q/%q ok=%v", user, pass, ok)
	}
}

func TestNetrcMachineAfterDefaultAndMacdef(t *testing.T) {
	o := netrcOptions(t, `default login defuser password defpass
macdef init
	put file

machine example.test login machuser password machpass
`)
	user, pass, ok := netrcCredentials(o, "EXAMPLE.TEST")
	if !ok || user != "machuser" || pass != "machpass" {
		t.Fatalf("got %q/%q ok=%v", user, pass, ok)
	}
}

func TestNetrcNoMatch(t *testing.T) {
	o := netrcOptions(t, "machine other.test login u password p\n")
	if _, _, ok := netrcCredentials(o, "example.test"); ok {
		t.Fatal("unexpected credentials")
	}
}
