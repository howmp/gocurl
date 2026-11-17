package transport

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"gocurl/internal/config"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	httpguts "golang.org/x/net/http/httpguts"
	xproxy "golang.org/x/net/proxy"
)

func NewChainProxyContext(o *config.Options, proxy string) (func(context.Context, string, string) (net.Conn, error), error) {
	local, err := LocalTCPAddr(o.InterfaceName)
	if err != nil {
		return nil, err
	}
	headers := make(fhttp.Header)
	for name, values := range ProxyHeaderMap(o) {
		headers[name] = values
	}
	dialer := &chainProxyDialer{
		o:              o,
		proxyURL:       proxy,
		timeout:        o.ConnectTimeout,
		localAddr:      local,
		connectHeaders: headers,
	}
	return dialer.DialContext, nil
}

type chainProxyDialer struct {
	o              *config.Options
	proxyURL       string
	timeout        time.Duration
	localAddr      *net.TCPAddr
	connectHeaders fhttp.Header
}

func (d *chainProxyDialer) Dial(network, addr string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, addr)
}

func (d *chainProxyDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	resolved, err := ResolveDialAddr(ctx, d.o, addr)
	if err != nil {
		return nil, err
	}
	addr = resolved
	proxyURL, err := url.Parse(d.proxyURL)
	if err != nil {
		return nil, err
	}
	normalizeProxyPort(proxyURL)
	switch strings.ToLower(proxyURL.Scheme) {
	case "socks4", "socks4a", "socks5", "socks5h":
		base := d.baseDialer()
		dialer, err := xproxy.FromURL(proxyURL, base)
		if err != nil {
			return nil, err
		}
		if cd, ok := dialer.(xproxy.ContextDialer); ok {
			return cd.DialContext(ctx, network, addr)
		}
		return dialer.Dial(network, addr)
	case "http", "https":
		return d.dialHTTPProxy(ctx, proxyURL, addr)
	default:
		return nil, fmt.Errorf("proxy scheme %q is not supported", proxyURL.Scheme)
	}
}

type contextDialerFunc func(context.Context, string, string) (net.Conn, error)

func (f contextDialerFunc) Dial(network, addr string) (net.Conn, error) {
	return f(context.Background(), network, addr)
}

func (f contextDialerFunc) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return f(ctx, network, addr)
}

func (d *chainProxyDialer) baseDialer() xproxy.Dialer {
	return contextDialerFunc(d.dialDirectOrPreProxy)
}

func (d *chainProxyDialer) dialDirectOrPreProxy(ctx context.Context, network, addr string) (net.Conn, error) {
	if d.o.PreProxy != "" {
		return dialViaProxy(ctx, d.o, d.o.PreProxy, addr, d.timeout, d.localAddr, nil, d.o.ProxyInsecure)
	}
	return DenyPrivateDialer(d.o, d.timeout, d.localAddr).DialContext(ctx, network, addr)
}

func (d *chainProxyDialer) dialHTTPProxy(ctx context.Context, proxyURL *url.URL, target string) (net.Conn, error) {
	return connectHTTPProxy(ctx, proxyURL, target, d.timeout, d.connectHeaders, d.o.ProxyInsecure, d.dialDirectOrPreProxy)
}

func proxyDialAddress(proxyURL *url.URL) string {
	if proxyURL.Port() != "" {
		return proxyURL.Host
	}
	return net.JoinHostPort(proxyURL.Hostname(), DefaultPort(proxyURL.Scheme))
}

func connectHTTPProxy(ctx context.Context, proxyURL *url.URL, target string, timeout time.Duration, headers fhttp.Header, insecure bool, dial func(context.Context, string, string) (net.Conn, error)) (*bufferedConn, error) {
	if err := config.ValidateConnectTarget(target); err != nil {
		return nil, err
	}
	conn, err := dial(ctx, "tcp", proxyDialAddress(proxyURL))
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(proxyURL.Scheme, "https") {
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName:         proxyURL.Hostname(),
			InsecureSkipVerify: insecure,
			NextProtos:         []string{"http/1.1"},
			MinVersion:         tls.VersionTLS12,
		})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, err
		}
		conn = tlsConn
	}

	reqHeaders := make(fhttp.Header, len(headers)+1)
	for name, values := range headers {
		reqHeaders[name] = append([]string(nil), values...)
	}
	if reqHeaders.Get("Proxy-Authorization") == "" && proxyURL.User != nil {
		password, _ := proxyURL.User.Password()
		token := base64.StdEncoding.EncodeToString([]byte(proxyURL.User.Username() + ":" + password))
		reqHeaders.Set("Proxy-Authorization", "Basic "+token)
	}
	if err := validateProxyHeaders(reqHeaders); err != nil {
		conn.Close()
		return nil, err
	}

	var request strings.Builder
	fmt.Fprintf(&request, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
	for name, values := range reqHeaders {
		for _, value := range values {
			fmt.Fprintf(&request, "%s: %s\r\n", name, value)
		}
	}
	request.WriteString("\r\n")
	deadline := time.Now().Add(30 * time.Second)
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	if _, err := io.WriteString(conn, request.String()); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(LimitResponseHeaderReader(conn))
	resp, err := http.ReadResponse(br, &http.Request{Method: "CONNECT"})
	if err != nil {
		conn.Close()
		return nil, err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("proxy responded with %s", resp.Status)
	}
	_ = conn.SetDeadline(time.Time{})
	return &bufferedConn{Conn: conn, reader: br}, nil
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func dialViaProxy(ctx context.Context, o *config.Options, proxyRaw, target string, timeout time.Duration, localAddr *net.TCPAddr, headers fhttp.Header, insecure bool) (net.Conn, error) {
	raw := strings.TrimSpace(proxyRaw)
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	proxyURL, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	normalizeProxyPort(proxyURL)
	dialer := DenyPrivateDialer(o, timeout, localAddr)
	switch strings.ToLower(proxyURL.Scheme) {
	case "http", "https":
		return connectHTTPProxy(ctx, proxyURL, target, timeout, headers, insecure, dialer.DialContext)
	case "socks4", "socks4a", "socks5", "socks5h":
		socksDialer, err := xproxy.FromURL(proxyURL, dialer)
		if err != nil {
			return nil, err
		}
		if cd, ok := socksDialer.(xproxy.ContextDialer); ok {
			return cd.DialContext(ctx, "tcp", target)
		}
		return socksDialer.Dial("tcp", target)
	default:
		return nil, fmt.Errorf("proxy scheme %q is not supported", proxyURL.Scheme)
	}
}

func validateProxyHeaders(headers fhttp.Header) error {
	for name, values := range headers {
		if !httpguts.ValidHeaderFieldName(name) {
			return fmt.Errorf("invalid proxy header name %q", name)
		}
		for _, value := range values {
			if !httpguts.ValidHeaderFieldValue(value) {
				return fmt.Errorf("invalid value for proxy header %q", name)
			}
		}
	}
	return nil
}

func normalizeProxyPort(proxyURL *url.URL) {
	if proxyURL.Port() != "" {
		return
	}
	port := "80"
	switch strings.ToLower(proxyURL.Scheme) {
	case "https":
		port = "443"
	case "socks4", "socks4a", "socks5", "socks5h":
		port = "1080"
	}
	proxyURL.Host = net.JoinHostPort(proxyURL.Hostname(), port)
}
