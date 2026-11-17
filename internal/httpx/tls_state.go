package httpx

import (
	"crypto/x509"
	"net"
	"strings"

	"gocurl/internal/config"

	http "github.com/bogdanfinn/fhttp"
	utls "github.com/bogdanfinn/utls"
)

type tlsStatePinner struct {
	store *config.TLSStateStore
}

func (p *tlsStatePinner) Pin(conn *utls.UConn, host string) error {
	if p == nil || p.store == nil || conn == nil {
		return nil
	}
	state := conn.ConnectionState()
	stored := config.TLSState{
		Version:          state.Version,
		CipherSuite:      state.CipherSuite,
		ALPN:             state.NegotiatedProtocol,
		Verified:         len(state.VerifiedChains) > 0,
		PeerCertificates: append([]*x509.Certificate(nil), state.PeerCertificates...),
	}
	port := remotePort(conn)
	stored.Port = port
	if port == "" {
		p.store.Set(host, stored)
		return nil
	}
	p.store.SetOrigin(host, port, stored)
	return nil
}

func remotePort(conn *utls.UConn) string {
	if conn == nil {
		return ""
	}
	addr := conn.RemoteAddr()
	if addr == nil {
		return ""
	}
	if _, port, err := net.SplitHostPort(addr.String()); err == nil {
		return port
	}
	return ""
}

func ApplyTLSState(o *config.Options, resp *http.Response) {
	if o == nil || o.TLSState == nil || resp == nil || resp.TLS != nil || resp.Request == nil || resp.Request.URL == nil {
		return
	}
	if !strings.EqualFold(resp.Request.URL.Scheme, "https") {
		return
	}
	port := resp.Request.URL.Port()
	if port == "" {
		port = "443"
	}
	state, exact, ok := o.TLSState.LookupOrigin(resp.Request.URL.Hostname(), port)
	if !ok {
		return
	}
	// An entry stored for a different port belongs to a different origin and
	// must not be attributed to this response. Host-level fallback entries are
	// the exception: they carry no port claim.
	if exact && state.Port != "" && state.Port != port {
		return
	}
	resp.TLS = &utls.ConnectionState{
		Version:            state.Version,
		CipherSuite:        state.CipherSuite,
		NegotiatedProtocol: state.ALPN,
		PeerCertificates:   state.PeerCertificates,
	}
	if state.Verified {
		resp.TLS.VerifiedChains = make([][]*x509.Certificate, 1)
	}
}
