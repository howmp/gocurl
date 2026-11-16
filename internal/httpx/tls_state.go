package httpx

import (
	"crypto/x509"
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
	p.store.Set(host, config.TLSState{
		Version:          state.Version,
		CipherSuite:      state.CipherSuite,
		ALPN:             state.NegotiatedProtocol,
		Verified:         len(state.VerifiedChains) > 0,
		PeerCertificates: append([]*x509.Certificate(nil), state.PeerCertificates...),
	})
	return nil
}

func ApplyTLSState(o *config.Options, resp *http.Response) {
	if o == nil || o.TLSState == nil || resp == nil || resp.TLS != nil || resp.Request == nil || resp.Request.URL == nil {
		return
	}
	state, ok := o.TLSState.Get(resp.Request.URL.Hostname())
	if !ok {
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
