package transport

import (
	"context"
	"net"
	"strings"
	"testing"

	"gocurl/internal/config"
)

func TestFilterIPsDeniesPrivate(t *testing.T) {
	ips := []net.IP{
		net.ParseIP("8.8.8.8"),
		net.ParseIP("10.0.0.1"),
		net.ParseIP("127.0.0.1"),
		net.ParseIP("169.254.169.254"),
		net.ParseIP("100.64.0.1"),
		net.ParseIP("::1"),
		net.ParseIP("fc00::1"),
	}
	o := config.NewOptions()
	o.DenyPrivateAddresses = true
	got := filterIPs(ips, o)
	if len(got) != 1 || got[0].String() != "8.8.8.8" {
		t.Fatalf("filtered = %v", got)
	}
	o.DenyPrivateAddresses = false
	if got := filterIPs(ips, o); len(got) != len(ips) {
		t.Fatalf("default policy filtered addresses: %v", got)
	}
}

func TestResolveCandidatesFiltersDeniedAddresses(t *testing.T) {
	o := config.NewOptions()
	o.DenyPrivateAddresses = true
	o.Resolves["example.test:443"] = []string{"10.0.0.1", "8.8.8.8"}
	target, err := resolveCandidates(context.Background(), o, "example.test", "443")
	if err != nil {
		t.Fatal(err)
	}
	if len(target.addrs) != 1 || target.addrs[0] != "8.8.8.8" {
		t.Fatalf("addrs = %v", target.addrs)
	}
}

func TestQueryDoHDeniesPrivateEndpoint(t *testing.T) {
	o := config.NewOptions()
	o.DohURL = "https://127.0.0.1:1/dns-query"
	o.DohInsecure = true
	o.DenyPrivateAddresses = true
	if _, err := queryDoH(context.Background(), o, nil); err == nil || !strings.Contains(err.Error(), "private address") {
		t.Fatalf("err = %v", err)
	}
}

func TestPrivateAddressDeniedCoversEmbeddedIPv4(t *testing.T) {
	denied := []string{
		"127.0.0.1",
		"10.0.0.1",
		"0.0.0.0",
		"0.1.2.3",
		"169.254.169.254",
		"100.64.0.1",
		"255.255.255.255",
		"240.0.0.1",
		"198.18.0.1",
		"::1",
		"fc00::1",
		"fe80::1",
		"::ffff:127.0.0.1",
		"::127.0.0.1",
		"64:ff9b::7f00:1",
		"64:ff9b::a00:1",
		"2002:7f00:1::",
		"2002:a00:1::",
	}
	for _, raw := range denied {
		if !privateAddressDenied(net.ParseIP(raw)) {
			t.Errorf("%s should be denied", raw)
		}
	}
	if !privateAddressDenied(nil) {
		t.Error("unparsable address must be denied")
	}
	if privateAddressDenied(net.ParseIP("8.8.8.8")) {
		t.Error("public address denied")
	}
	if privateAddressDenied(net.ParseIP("2606:4700::1111")) {
		t.Error("public IPv6 address denied")
	}
}

func TestDenyPrivateControlStripsZone(t *testing.T) {
	o := config.NewOptions()
	o.DenyPrivateAddresses = true
	control := denyPrivateControl(o)
	if control == nil {
		t.Fatal("control missing")
	}
	if err := control("tcp4", "[fe80::1%eth0]:80", nil); err == nil {
		t.Fatal("zoned link-local address was not denied")
	}
	if err := control("tcp4", "[2606:4700::1111]:443", nil); err != nil {
		t.Fatalf("public address denied: %v", err)
	}
}

func TestResolveHostIPsNilOptions(t *testing.T) {
	ips, err := ResolveHostIPs(context.Background(), nil, "127.0.0.1")
	if err != nil || len(ips) != 1 {
		t.Fatalf("ips = %v err = %v", ips, err)
	}
	if _, err := ResolveHostIPs(context.Background(), nil, "example.test"); err == nil {
		t.Fatal("expected no-address error")
	}
}
