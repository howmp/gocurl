package transport

import (
	"context"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"gocurl/internal/config"
)

func hangingProxy(t *testing.T) *url.URL {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		time.Sleep(2 * time.Second)
		_ = conn.Close()
	}()
	return &url.URL{Scheme: "http", Host: ln.Addr().String()}
}

func TestConnectHTTPProxyHonorsTimeout(t *testing.T) {
	proxyURL := hangingProxy(t)
	start := time.Now()
	_, err := connectHTTPProxy(context.Background(), proxyURL, "example.test:443", 100*time.Millisecond, nil, false, (&net.Dialer{}).DialContext)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("CONNECT took %s, want about 100ms", elapsed)
	}
}

func TestConnectHTTPProxyHonorsContextCancellation(t *testing.T) {
	proxyURL := hangingProxy(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := connectHTTPProxy(ctx, proxyURL, "example.test:443", 0, nil, false, (&net.Dialer{}).DialContext)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("CONNECT took %s, want about 100ms", elapsed)
	}
}

func TestConnectHTTPProxyRejectsInjectedTarget(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	dialed := make(chan struct{}, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		dialed <- struct{}{}
		_ = conn.Close()
	}()
	proxyURL := &url.URL{Scheme: "http", Host: ln.Addr().String()}
	_, err = connectHTTPProxy(context.Background(), proxyURL,
		"example.test:443\r\nX-Injected: yes", time.Second, nil, false, (&net.Dialer{}).DialContext)
	if err == nil || !strings.Contains(err.Error(), "CONNECT target") {
		t.Fatalf("err = %v", err)
	}
	select {
	case <-dialed:
		t.Fatal("proxy dial was attempted for an invalid target")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestNormalizeProxyPortSocks(t *testing.T) {
	u, err := url.Parse("socks5://proxy.test")
	if err != nil {
		t.Fatal(err)
	}
	normalizeProxyPort(u)
	if u.Host != "proxy.test:1080" {
		t.Fatalf("host = %q, want proxy.test:1080", u.Host)
	}
}

func TestProxyDisabledSkipsEnvironment(t *testing.T) {
	t.Setenv("http_proxy", "http://proxy.test:8080")
	target, _ := url.Parse("http://example.test/")
	o := config.NewOptions()
	o.ProxyDisabled = true
	got, err := ProxyURLFor(o, target)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("proxy = %q, want direct", got)
	}
	o.ProxyDisabled = false
	got, err = ProxyURLFor(o, target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "proxy.test:8080") {
		t.Fatalf("proxy = %q, want env proxy", got)
	}
}
