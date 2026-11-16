package app

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNextMultipleOperations(t *testing.T) {
	var methods []string
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		methods = append(methods, r.Method)
		bodies = append(bodies, string(body))
		fmt.Fprint(w, r.URL.Path)
	}))
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sS", srv.URL+"/a",
		"--next", "-sS", "-X", "POST", "-d", "payload", srv.URL+"/b")
	if code != 0 || out != "/a/b" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if len(methods) != 2 || methods[0] != http.MethodGet || methods[1] != http.MethodPost {
		t.Fatalf("methods=%v", methods)
	}
	if len(bodies) != 2 || bodies[0] != "" || bodies[1] != "payload" {
		t.Fatalf("bodies=%q", bodies)
	}
}

func TestNextRequiresURLPerOperation(t *testing.T) {
	code, _, stderr := runGocurl(t, "-sS", "http://example.test/", "--next", "-sS")
	if code != 2 || !strings.Contains(stderr, "operation 2 has no URL") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}
