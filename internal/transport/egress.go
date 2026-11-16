package transport

import (
	"fmt"
	"net"
	"syscall"

	"gocurl/internal/config"
)

func privateAddressDenied(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return true
		}
	}
	return false
}

func denyPrivateControl(o *config.Options) func(string, string, syscall.RawConn) error {
	if o == nil || !o.DenyPrivateAddresses {
		return nil
	}
	return func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if privateAddressDenied(ip) {
			return fmt.Errorf("private address denied: %s", host)
		}
		return nil
	}
}

func filterDeniedResolveAddrs(addrs []string, o *config.Options) []string {
	if o == nil || !o.DenyPrivateAddresses {
		return addrs
	}
	out := addrs[:0]
	for _, addr := range addrs {
		if !privateAddressDenied(net.ParseIP(addr)) {
			out = append(out, addr)
		}
	}
	return out
}
