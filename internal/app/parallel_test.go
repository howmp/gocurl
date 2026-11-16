package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParallelRequiresOneOutputPerURL(t *testing.T) {
	code, _, stderr := runGocurl(t, "-Z", "-o", "out.txt", "http://example.test/a", "http://example.test/b")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "each URL requires its own -o output") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestGetRejectsInvalidQueryBytes(t *testing.T) {
	code, _, _ := runGocurl(t, "-G", "-d", "a=b c", "http://example.test/path")
	if code != 3 {
		t.Fatalf("exit = %d, want 3", code)
	}
}

func TestHelpDoesNotLeakInternalTypeNames(t *testing.T) {
	code, out, _ := runGocurl(t, "--help")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if strings.Contains(out, "config.Options") {
		t.Fatalf("help leaks internal type name: %q", out)
	}
}

func TestHeaderRemovalSuppressesCookieInjection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.Header.Get("Cookie"))
	}))
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sS", "-b", "sid=SECRET", "-H", "Cookie:", srv.URL)
	if code != 0 || out != "" {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
}
