package transport

import (
	"context"
	"net"
	"net/url"
	"strings"
	"sync"

	"gocurl/internal/config"
)

type hopDialFunc func(context.Context, string, string) (net.Conn, error)

type HopProxyDialer struct {
	o       *config.Options
	mu      sync.Mutex
	schemes map[string]string
	dialers map[string]hopDialFunc
}

func NewHopProxyDialer(o *config.Options) *HopProxyDialer {
	return &HopProxyDialer{
		o:       o,
		schemes: make(map[string]string),
		dialers: make(map[string]hopDialFunc),
	}
}

func (d *HopProxyDialer) Record(u *url.URL) {
	if d == nil || u == nil {
		return
	}
	port := u.Port()
	if port == "" {
		port = DefaultPort(u.Scheme)
	}
	addr := net.JoinHostPort(u.Hostname(), port)
	scheme := strings.ToLower(u.Scheme)
	d.mu.Lock()
	d.schemes[addr] = scheme
	d.mu.Unlock()
}

func (d *HopProxyDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	scheme := d.schemeFor(addr)
	proxy, err := ProxyURLFor(d.o, &url.URL{Scheme: scheme, Host: addr})
	if err != nil {
		return nil, err
	}
	if proxy == "" {
		return DialTCP(ctx, network, addr, d.o)
	}
	dial, err := d.proxyDialer(proxy)
	if err != nil {
		return nil, err
	}
	return dial(ctx, network, addr)
}

func (d *HopProxyDialer) schemeFor(addr string) string {
	d.mu.Lock()
	scheme := d.schemes[addr]
	d.mu.Unlock()
	if scheme != "" {
		return scheme
	}
	if _, port, err := net.SplitHostPort(addr); err == nil {
		switch port {
		case "443", "8443", "9443":
			return "https"
		}
	}
	return "http"
}

func (d *HopProxyDialer) proxyDialer(proxy string) (hopDialFunc, error) {
	d.mu.Lock()
	dial := d.dialers[proxy]
	d.mu.Unlock()
	if dial != nil {
		return dial, nil
	}
	dial, err := NewChainProxyContext(d.o, proxy)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	if existing := d.dialers[proxy]; existing != nil {
		dial = existing
	} else {
		d.dialers[proxy] = dial
	}
	d.mu.Unlock()
	return dial, nil
}
