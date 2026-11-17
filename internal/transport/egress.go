package transport

import (
	"fmt"
	"net"
	"strings"
	"syscall"
	"time"

	"gocurl/internal/config"
)

func privateAddressDenied(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if isDeniedIP(ip) {
		return true
	}
	if embedded := embeddedIPv4(ip); embedded != nil {
		return isDeniedIP(embedded)
	}
	return false
}

func isDeniedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	if v4[0] == 0 {
		return true
	}
	if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return true
	}
	if v4[0] >= 240 {
		return true
	}
	if v4[0] == 198 && (v4[1] == 18 || v4[1] == 19) {
		return true
	}
	return false
}

func embeddedIPv4(ip net.IP) net.IP {
	if v4 := ip.To4(); v4 != nil {
		return v4
	}
	if len(ip) != net.IPv6len {
		return nil
	}
	if ip[0] == 0x00 && ip[1] == 0x64 && ip[2] == 0xff && ip[3] == 0x9b &&
		allZeroBytes(ip[4:12]) {
		return net.IPv4(ip[12], ip[13], ip[14], ip[15]).To4()
	}
	if ip[0] == 0x20 && ip[1] == 0x02 {
		return net.IPv4(ip[2], ip[3], ip[4], ip[5]).To4()
	}
	if allZeroBytes(ip[:12]) {
		return net.IPv4(ip[12], ip[13], ip[14], ip[15]).To4()
	}
	return nil
}

func allZeroBytes(data []byte) bool {
	for _, b := range data {
		if b != 0 {
			return false
		}
	}
	return true
}

func parseDialIP(host string) net.IP {
	if idx := strings.IndexByte(host, '%'); idx >= 0 {
		host = host[:idx]
	}
	return net.ParseIP(host)
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
		if privateAddressDenied(parseDialIP(host)) {
			return fmt.Errorf("private address denied: %s", host)
		}
		return nil
	}
}

// DenyPrivateDialer returns a dialer that enforces --deny-private-addresses,
// or a plain dialer when the policy is off. Callers outside this package use
// it to keep protocol-specific dial paths (FTP, proxy chains) consistent.
func DenyPrivateDialer(o *config.Options, timeout time.Duration, local *net.TCPAddr) *net.Dialer {
	return &net.Dialer{Timeout: timeout, LocalAddr: local, Control: denyPrivateControl(o)}
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
