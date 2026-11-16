package httpx

import (
	"testing"

	"gocurl/internal/config"

	http "github.com/bogdanfinn/fhttp"
)

func redirectRequest(t *testing.T, prevURL, nextURL string) (*http.Request, []*http.Request) {
	t.Helper()
	prev, err := http.NewRequest(http.MethodGet, prevURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	prev.Header.Set("Authorization", "Bearer TOPSECRET")
	prev.Header.Set("Cookie", "sid=SECRET")
	next, err := http.NewRequest(http.MethodGet, nextURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	return next, []*http.Request{prev}
}

func TestLocationTrustedForwardsOnlyAuthorization(t *testing.T) {
	o := config.NewOptions()
	o.Location = true
	o.LocationTrusted = true

	req, via := redirectRequest(t, "https://a.test/", "https://b.test/")
	if err := redirectFunc(o)(req, via); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer TOPSECRET" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := req.Header.Get("Cookie"); got != "" {
		t.Fatalf("Cookie leaked across hosts: %q", got)
	}
}

func TestRedirectToSubdomainStripsCredentials(t *testing.T) {
	o := config.NewOptions()
	o.Location = true

	req, via := redirectRequest(t, "https://first.test/", "https://a.first.test/")
	req.Header.Set("Authorization", "Bearer NEXTSECRET")
	req.Header.Set("Cookie", "sid=NEXTSECRET")
	req.Header.Set("Cookie2", "sid2=NEXTSECRET")
	if err := redirectFunc(o)(req, via); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization leaked to subdomain: %q", got)
	}
	if got := req.Header.Get("Cookie"); got != "" {
		t.Fatalf("Cookie leaked to subdomain: %q", got)
	}
	if got := req.Header.Get("Cookie2"); got != "" {
		t.Fatalf("Cookie2 leaked to subdomain: %q", got)
	}
}

func TestRedirectSameHostDifferentPortStripsCredentials(t *testing.T) {
	o := config.NewOptions()
	o.Location = true

	req, via := redirectRequest(t, "https://first.test:443/a", "https://first.test:8443/b")
	req.Header.Set("Authorization", "Bearer NEXTSECRET")
	req.Header.Set("Cookie", "sid=NEXTSECRET")
	if err := redirectFunc(o)(req, via); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization leaked across ports: %q", got)
	}
	if got := req.Header.Get("Cookie"); got != "" {
		t.Fatalf("Cookie leaked across ports: %q", got)
	}
}

func TestRedirectSameOriginDefaultPortKeepsCredentials(t *testing.T) {
	o := config.NewOptions()
	o.Location = true

	req, via := redirectRequest(t, "https://first.test/a", "https://first.test:443/b")
	req.Header.Set("Authorization", "Bearer NEXTSECRET")
	req.Header.Set("Cookie", "sid=NEXTSECRET")
	if err := redirectFunc(o)(req, via); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer NEXTSECRET" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := req.Header.Get("Cookie"); got != "sid=NEXTSECRET" {
		t.Fatalf("Cookie = %q", got)
	}
}

func TestRedirectSameHostKeepsCredentials(t *testing.T) {
	o := config.NewOptions()
	o.Location = true

	req, via := redirectRequest(t, "https://first.test/a", "https://FIRST.test./b")
	req.Header.Set("Authorization", "Bearer TOPSECRET")
	req.Header.Set("Cookie", "sid=SECRET")
	if err := redirectFunc(o)(req, via); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got == "" {
		t.Fatal("Authorization missing on same-host redirect")
	}
	if got := req.Header.Get("Cookie"); got == "" {
		t.Fatal("Cookie missing on same-host redirect")
	}
}

func TestLocationTrustedRejectsHTTPSDowngrade(t *testing.T) {
	o := config.NewOptions()
	o.Location = true
	o.LocationTrusted = true

	req, via := redirectRequest(t, "https://a.test/", "http://b.test/")
	req.Header.Set("Authorization", "Bearer NEXTSECRET")
	req.Header.Set("Cookie", "sid=NEXTSECRET")
	if err := redirectFunc(o)(req, via); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization leaked to http: %q", got)
	}
	if got := req.Header.Get("Cookie"); got != "" {
		t.Fatalf("Cookie leaked to http: %q", got)
	}
}

func TestRedirectFuncLimitsRedirects(t *testing.T) {
	o := config.NewOptions()
	o.MaxRedirs = 0
	req, _ := redirectRequest(t, "https://a.test/", "https://b.test/")
	via := make([]*http.Request, 1)
	if err := redirectFunc(o)(req, via); err == nil {
		t.Fatal("expected redirect limit error")
	}
}

func TestRedirectPreservesMethodWhenConfigured(t *testing.T) {
	o := config.NewOptions()
	o.Post302 = true
	prev, err := http.NewRequest(http.MethodPost, "https://a.test/", nil)
	if err != nil {
		t.Fatal(err)
	}
	next, err := http.NewRequest(http.MethodGet, "https://a.test/next", nil)
	if err != nil {
		t.Fatal(err)
	}
	next.Response = &http.Response{StatusCode: http.StatusFound, Request: prev}
	if err := redirectFunc(o)(next, []*http.Request{prev}); err != nil {
		t.Fatal(err)
	}
	if next.Method != http.MethodPost {
		t.Fatalf("method = %q, want POST", next.Method)
	}
}

func TestRedirectHTTPSDowngradeStripsCookies(t *testing.T) {
	o := config.NewOptions()
	o.Location = true
	o.LocationTrusted = true

	req, via := redirectRequest(t, "https://first.test/a", "http://first.test/b")
	req.Header.Set("Authorization", "Bearer TOPSECRET")
	req.Header.Set("Cookie", "sid=SECRET")
	if err := redirectFunc(o)(req, via); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization leaked on downgrade: %q", got)
	}
	if got := req.Header.Get("Cookie"); got != "" {
		t.Fatalf("Cookie leaked on downgrade: %q", got)
	}
}
