package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseShortOptions(t *testing.T) {
	o, err := ParseArgs([]string{
		"-sSL", "-XPOST", "-H", "X-Test: yes", "-HContent-Type: text/plain",
		"-d", "a=1", "-o", "out.txt", "https://example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !o.Silent || !o.ShowError || !o.Location {
		t.Fatalf("boolean cluster not parsed: %+v", o)
	}
	if o.Method != "POST" {
		t.Fatalf("method = %q", o.Method)
	}
	if len(o.Headers) != 2 || o.Headers[0].Name != "X-Test" || o.Headers[1].Name != "Content-Type" {
		t.Fatalf("headers = %+v", o.Headers)
	}
	if len(o.Data) != 1 || o.Data[0].Value != "a=1" {
		t.Fatalf("data = %+v", o.Data)
	}
	if len(o.Outputs) != 1 || o.Outputs[0] != "out.txt" {
		t.Fatalf("outputs = %+v", o.Outputs)
	}
	if len(o.Urls) != 1 || o.Urls[0] != "https://example.com" {
		t.Fatalf("urls = %+v", o.Urls)
	}
}

func TestParseLongOptions(t *testing.T) {
	o, err := ParseArgs([]string{
		"--header=X-Test: yes", "--http1.1", "--max-time", "1.5",
		"--connect-timeout=2", "--url", "https://example.com",
		"--data-binary", "@file.bin", "--max-redirs", "7",
	})
	if err != nil {
		t.Fatal(err)
	}
	if o.HttpVersion != "1.1" || o.MaxRedirs != 7 {
		t.Fatalf("unexpected options: %+v", o)
	}
	if o.MaxTime.String() != "1.5s" || o.ConnectTimeout.String() != "2s" {
		t.Fatalf("timeouts: %v %v", o.MaxTime, o.ConnectTimeout)
	}
	if len(o.Data) != 1 || o.Data[0].Kind != DataBinary || o.Data[0].Value != "@file.bin" {
		t.Fatalf("data = %+v", o.Data)
	}
}

func TestParseUnknownOption(t *testing.T) {
	if _, err := ParseArgs([]string{"--not-an-option"}); err == nil {
		t.Fatal("expected unknown option error")
	}
	if _, err := ParseArgs([]string{"--socks5", "127.0.0.1:1080"}); err == nil {
		t.Fatal("expected unsupported option error")
	}
	if _, err := ParseArgs([]string{"--http3"}); err == nil {
		t.Fatal("expected unsupported --http3 error")
	}
}

func TestHeaderFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "headers.txt")
	if err := os.WriteFile(file, []byte("X-One: 1\n# comment\n\nX-Two;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o, err := ParseArgs([]string{"-H", "@" + file, "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Headers) != 2 || o.Headers[0].Name != "X-One" ||
		o.Headers[1].Name != "X-Two" || !o.Headers[1].Empty {
		t.Fatalf("headers = %+v", o.Headers)
	}
}

func TestResolveParsing(t *testing.T) {
	o, err := ParseArgs([]string{"--resolve", "example.test:443:[::1],[::2]", "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	got := o.Resolves["example.test:443"]
	if len(got) != 2 || got[0] != "::1" || got[1] != "::2" {
		t.Fatalf("resolves = %#v", o.Resolves)
	}
}

func TestTLSCompatOptionsUnsupported(t *testing.T) {
	for _, args := range [][]string{
		{"--tlsv1.2"},
		{"--tls-max", "1.2"},
		{"--ciphers", "AES128-GCM-SHA256"},
		{"--curves", "X25519"},
		{"--pinnedpubkey", "sha256//x"},
		{"--cert-status"},
	} {
		if _, err := ParseArgs(append(args, "https://example.com")); err == nil {
			t.Fatalf("%v should be unsupported", args)
		}
	}
}

func TestBoolOptionTable(t *testing.T) {
	for name := range longBoolOptions {
		if _, err := ParseArgs([]string{"--" + name, "https://example.com"}); err != nil {
			t.Fatalf("--%s: %v", name, err)
		}
		if _, err := ParseArgs([]string{"--" + name + "=1", "https://example.com"}); err == nil {
			t.Fatalf("--%s=1 should reject a value", name)
		}
	}
}

func TestLongValueOptionsParse(t *testing.T) {
	values := map[string]string{
		"header": "X-Test: value", "proxy-header": "X-Test: value",
		"form": "a=b", "form-string": "a=b", "range": "0-1",
		"resolve": "example.test:443:127.0.0.1", "connect-to": "a.test:443:b.test:443",
		"continue-at": "1", "limit-rate": "1k", "max-filesize": "1k",
		"doh-url": "https://dns.example/query",
	}
	for name := range longValueOptions {
		v := values[name]
		if v == "" {
			v = "1"
		}
		if _, err := ParseArgs([]string{"--" + name, v, "https://example.test/"}); err != nil {
			t.Errorf("--%s: %v", name, err)
		}
	}
}
