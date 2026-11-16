package transport

import (
	"net/url"
	"testing"

	"gocurl/internal/config"
)

func TestParseEnvProxyIgnoresUppercaseHTTPProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://evil.test:8080")
	t.Setenv("http_proxy", "http://good.test:8080")
	if got := parseEnvProxy("http_proxy"); got != "http://good.test:8080" {
		t.Fatalf("http_proxy = %q", got)
	}

	t.Setenv("HTTPS_PROXY", "http://upper.test:8080")
	t.Setenv("https_proxy", "")
	if got := parseEnvProxy("https_proxy"); got != "http://upper.test:8080" {
		t.Fatalf("https_proxy = %q", got)
	}
}

func TestNoProxyMatchesExplicitPort(t *testing.T) {
	o := config.NewOptions()
	o.Proxy = "http://proxy.example:3128"
	o.NoProxy = "internal.test:8080"
	u, err := url.Parse("http://internal.test:8080/path")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ProxyURLFor(o, u)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("NO_PROXY port was ignored: %q", got)
	}
}
