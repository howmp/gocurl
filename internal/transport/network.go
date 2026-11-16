package transport

import (
	"context"
	"fmt"
	"gocurl/internal/config"
	"net"
	"os"
	"runtime"
	"strings"
)

func ConnectToLookup(o *config.Options, host, port string) (string, bool) {
	if o == nil || len(o.ConnectTo) == 0 {
		return "", false
	}
	host = config.NormalizeResolveHost(host)
	if target, ok := o.ConnectTo[host+":"+port]; ok {
		return target, true
	}
	if target, ok := o.ConnectTo["*:"+port]; ok {
		return target, true
	}
	if target, ok := o.ConnectTo[host+":*"]; ok {
		return target, true
	}
	target, ok := o.ConnectTo["*:*"]
	return target, ok
}

func DialHTTPContext(o *config.Options, ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	target, err := resolveCandidates(ctx, o, host, port)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: o.ConnectTimeout}
	local, err := LocalTCPAddr(o.InterfaceName)
	if err != nil {
		return nil, err
	}
	if local != nil {
		dialer.LocalAddr = local
	}
	if len(target.addrs) == 0 && HasCustomDNS(o) && net.ParseIP(target.host) == nil {
		return nil, fmt.Errorf("custom DNS returned no addresses for %s", target.host)
	}
	if len(target.addrs) == 0 {
		return dialer.DialContext(ctx, network, net.JoinHostPort(target.host, target.port))
	}
	var lastErr error
	for _, resolved := range target.addrs {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(resolved, target.port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no address for %s", addr)
	}
	return nil, lastErr
}

func DialUnixContext(path string, abstract bool) func(context.Context, string, string) (net.Conn, error) {
	if abstract && runtime.GOOS != "linux" {
		return func(context.Context, string, string) (net.Conn, error) {
			return nil, fmt.Errorf("abstract unix socket is only supported on Linux")
		}
	}
	target := path
	if abstract && !strings.HasPrefix(target, "@") {
		target = "@" + target
	}
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", target)
	}
}

func LocalTCPAddr(interfaceName string) (*net.TCPAddr, error) {
	value := strings.TrimSpace(interfaceName)
	if value == "" {
		return nil, nil
	}
	value = strings.TrimPrefix(value, "if!")
	if ip := net.ParseIP(strings.Trim(value, "[]")); ip != nil {
		return &net.TCPAddr{IP: ip}, nil
	}
	iface, err := net.InterfaceByName(value)
	if err != nil {
		return nil, fmt.Errorf("option --interface: %w", err)
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, fmt.Errorf("option --interface: %w", err)
	}
	var fallback net.IP
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil {
			continue
		}
		if fallback == nil {
			fallback = ip
		}
		if v4 := ip.To4(); v4 != nil {
			return &net.TCPAddr{IP: v4}, nil
		}
	}
	if fallback != nil {
		return &net.TCPAddr{IP: fallback}, nil
	}
	return nil, fmt.Errorf("option --interface: interface %q has no addresses", value)
}

func DefaultPort(scheme string) string {
	switch strings.ToLower(scheme) {
	case "https", "wss":
		return "443"
	case "ftps":
		return "990"
	case "ftp":
		return "21"
	default:
		return "80"
	}
}

func parseEnvProxy(name string) string {
	if strings.EqualFold(name, "http_proxy") {
		return os.Getenv("http_proxy")
	}
	if value := os.Getenv(strings.ToUpper(name)); value != "" {
		return value
	}
	return os.Getenv(strings.ToLower(name))
}

func DialTCP(ctx context.Context, network, addr string, o *config.Options) (net.Conn, error) {
	if o.Ipv4 {
		network = "tcp4"
	} else if o.Ipv6 {
		network = "tcp6"
	}
	if len(o.Resolves) > 0 || len(o.ConnectTo) > 0 || o.InterfaceName != "" || HasCustomDNS(o) {
		return DialHTTPContext(o, ctx, network, addr)
	}
	d := &net.Dialer{Timeout: o.ConnectTimeout}
	return d.DialContext(ctx, network, addr)
}
