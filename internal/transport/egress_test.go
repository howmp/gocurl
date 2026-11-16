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

func TestResolveHostIPsNilOptions(t *testing.T) {
	ips, err := ResolveHostIPs(context.Background(), nil, "127.0.0.1")
	if err != nil || len(ips) != 1 {
		t.Fatalf("ips = %v err = %v", ips, err)
	}
	if _, err := ResolveHostIPs(context.Background(), nil, "example.test"); err == nil {
		t.Fatal("expected no-address error")
	}
}
