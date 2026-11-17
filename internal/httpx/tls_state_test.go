package httpx

import (
	"net/url"
	"testing"

	"gocurl/internal/config"

	http "github.com/bogdanfinn/fhttp"
)

func TestApplyTLSStateRejectsPlaintextFinalResponse(t *testing.T) {
	o := config.NewOptions()
	o.TLSState = config.NewTLSStateStore()
	o.TLSState.Set("example.test", config.TLSState{
		Version: 0x0304, CipherSuite: 0x1301, ALPN: "http/1.1", Verified: true,
	})
	u, _ := url.Parse("http://example.test/final")
	resp := &http.Response{Request: &http.Request{URL: u}}
	ApplyTLSState(o, resp)
	if resp.TLS != nil {
		t.Fatalf("plaintext response received TLS state: %+v", resp.TLS)
	}
}

func TestApplyTLSStateFillsHTTPSResponse(t *testing.T) {
	o := config.NewOptions()
	o.TLSState = config.NewTLSStateStore()
	o.TLSState.Set("example.test", config.TLSState{
		Version: 0x0304, CipherSuite: 0x1301, ALPN: "h2", Verified: true,
	})
	u, _ := url.Parse("https://example.test/final")
	resp := &http.Response{Request: &http.Request{URL: u}}
	ApplyTLSState(o, resp)
	if resp.TLS == nil {
		t.Fatal("https response did not receive TLS state")
	}
	if resp.TLS.Version != 0x0304 || len(resp.TLS.VerifiedChains) != 1 {
		t.Fatalf("tls state = %+v", resp.TLS)
	}
}
