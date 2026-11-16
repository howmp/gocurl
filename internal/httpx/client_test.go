package httpx

import (
	"strings"
	"testing"

	"gocurl/internal/config"
)

func TestNormalizeURLSchemes(t *testing.T) {
	for _, raw := range []string{"https://example.test/a", "http://example.test/a"} {
		if _, err := NormalizeURL(raw); err != nil {
			t.Errorf("NormalizeURL(%q): %v", raw, err)
		}
	}
	if _, err := NormalizeURL("://bad"); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestValidateUserHeaders(t *testing.T) {
	o := config.NewOptions()
	o.Headers = []config.HeaderArg{{Name: "Content-Length", Value: "1"}}
	if err := ValidateUserHeaders(o); err == nil || !strings.Contains(err.Error(), "Content-Length") {
		t.Fatalf("expected Content-Length rejection, got %v", err)
	}
	o.Headers = []config.HeaderArg{{Name: "X-Test", Value: "ok"}}
	if err := ValidateUserHeaders(o); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRequestTarget(t *testing.T) {
	for _, target := range []string{"/ok", "/a?b=c", "*", "http://example.test/path", "/%C3%BC"} {
		if err := ValidateRequestTarget(target); err != nil {
			t.Errorf("ValidateRequestTarget(%q) = %v", target, err)
		}
	}
	for _, target := range []string{"/bad path", "/bad\tpath", "/bad\rpath", "/bad\npath", "/bad\x00path", "/bad\x0bpath", "/bad\x7fpath", "/ü"} {
		if err := ValidateRequestTarget(target); err == nil {
			t.Errorf("ValidateRequestTarget(%q) accepted", target)
		}
	}
}
