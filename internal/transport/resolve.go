package transport

import (
	"context"
	"fmt"
	"net"

	"gocurl/internal/config"
)

func ResolveAddresses(o *config.Options, host, port string) []string {
	if o == nil || len(o.Resolves) == 0 {
		return nil
	}
	host = config.NormalizeResolveHost(host)
	for _, key := range []string{
		host + ":" + port,
		host + ":*",
		"*:" + port,
		"*:*",
	} {
		if addrs, ok := o.Resolves[key]; ok {
			return addrs
		}
	}
	return nil
}

type dialCandidates struct {
	host  string
	port  string
	addrs []string
}

func resolveCandidates(ctx context.Context, o *config.Options, host, port string) (dialCandidates, error) {
	target := dialCandidates{host: host, port: port}
	if connectTo, ok := ConnectToLookup(o, host, port); ok {
		var err error
		target.host, target.port, err = net.SplitHostPort(connectTo)
		if err != nil {
			return dialCandidates{}, err
		}
	}
	target.addrs = filterDeniedResolveAddrs(ResolveAddresses(o, target.host, target.port), o)
	if len(target.addrs) == 0 && HasCustomDNS(o) && net.ParseIP(target.host) == nil {
		ips, err := ResolveHostIPs(ctx, o, target.host)
		if err != nil {
			return dialCandidates{}, err
		}
		for _, ip := range ips {
			target.addrs = append(target.addrs, ip.String())
		}
	}
	return target, nil
}

func ResolveDialAddr(ctx context.Context, o *config.Options, addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, nil
	}
	target, err := resolveCandidates(ctx, o, host, port)
	if err != nil {
		return "", err
	}
	if len(target.addrs) > 0 {
		return net.JoinHostPort(target.addrs[0], target.port), nil
	}
	if HasCustomDNS(o) && net.ParseIP(target.host) == nil {
		return "", fmt.Errorf("no addresses for %s", target.host)
	}
	return net.JoinHostPort(target.host, target.port), nil
}
