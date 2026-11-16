package daemon

import (
	"gocurl/internal/config"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsurePrivateDir(t *testing.T) {
	dir := t.TempDir()
	_ = os.Chmod(dir, 0o700)
	if err := ensurePrivateDir(dir); err != nil {
		t.Fatalf("temp dir rejected: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDir(dir); err == nil {
		t.Fatal("expected insecure directory to be rejected")
	}
}

func TestValidateDaemonSocketRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	_ = os.Chmod(dir, 0o700)
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "daemon.sock")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := validateDaemonSocket(link); err == nil {
		t.Fatal("expected symlink socket path to be rejected")
	}
}

func TestTransportMappingRoundTrip(t *testing.T) {
	o := config.NewOptions()
	o.HttpVersion = "1.1"
	o.Insecure = true
	o.Ipv4 = true
	o.Resolves = map[string][]string{"example.test:443": {"127.0.0.1"}}
	o.DnsServers = []string{"1.1.1.1:53"}
	o.DohURL = "https://dns.example/query"
	o.DohInsecure = true
	o.ProxyInsecure = true
	o.PreProxy = "socks5://127.0.0.1:1080"
	target, _ := url.Parse("https://example.test/")
	tr := daemonTransportFromOptions(o, target)
	got := optionsFromDaemonTransport(tr)
	if got.HttpVersion != o.HttpVersion || !got.Insecure || !got.Ipv4 || got.DohURL != o.DohURL || !got.DohInsecure || got.PreProxy != o.PreProxy {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if len(got.Resolves["example.test:443"]) != 1 || got.DnsServers[0] != "1.1.1.1:53" {
		t.Fatalf("maps not preserved: %+v", got)
	}
}

func TestTransportMappingCoversFields(t *testing.T) {
	tr := daemonTransport{
		HTTPVersion:     "1.1",
		Insecure:        true,
		ProxyUser:       "proxy-user",
		NoProxy:         "localhost",
		IPv4:            true,
		IPv6:            true,
		Location:        true,
		LocationTrusted: true,
		Post301:         true,
		Post302:         true,
		Post303:         true,
		MaxRedirs:       7,
		CertType:        "PEM",
		KeyType:         "PEM",
		KeyPass:         "secret",
		Resolve:         map[string][]string{"example.test:443": {"127.0.0.1"}},
		ConnectTo:       map[string]string{"example.test:443": "127.0.0.1:8443"},
		UnixSocket:      "/tmp/http.sock",
		AbstractUnix:    true,
		Interface:       "eth0",
		ProxyInsecure:   true,
		PreProxy:        "socks5://127.0.0.1:1080",
		DnsServers:      []string{"1.1.1.1:53"},
		DohURL:          "https://dns.example/query",
		DohInsecure:     true,
	}
	o := optionsFromDaemonTransport(tr)
	if !o.Ipv4 || !o.Ipv6 || o.InterfaceName != "eth0" || o.CaCert != tr.CACert || o.ProxyUser != "proxy-user" {
		t.Fatalf("aliases not copied: %+v", o)
	}
	if o.UnixSocket != tr.UnixSocket || !o.AbstractUnix || !o.LocationTrusted || o.MaxRedirs != 7 {
		t.Fatalf("fields not copied: %+v", o)
	}
	if len(o.Resolves["example.test:443"]) != 1 || o.ConnectTo["example.test:443"] != "127.0.0.1:8443" {
		t.Fatalf("maps not copied: %+v", o)
	}

	target, _ := url.Parse("https://example.test/")
	back := daemonTransportFromOptions(o, target)
	if back.Interface != "eth0" || !back.IPv4 || !back.IPv6 || back.ProxyUser != "proxy-user" {
		t.Fatalf("reverse aliases not copied: %+v", back)
	}
	if !back.ProxyInsecure || back.PreProxy != "socks5://127.0.0.1:1080" || !back.DohInsecure {
		t.Fatalf("reverse fields not copied: %+v", back)
	}
}
