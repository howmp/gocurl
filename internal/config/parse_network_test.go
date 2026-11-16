package config

import "testing"

func TestParseConnectToRejectsInjection(t *testing.T) {
	o := NewOptions()
	if err := parseConnectToOption(o, "a.test:443:b.test:443\r\nX-Injected: 1"); err == nil {
		t.Fatal("expected connect-to rejection")
	}
	if err := parseConnectToOption(o, "a.test:bad:b.test:443"); err == nil {
		t.Fatal("expected source port rejection")
	}
	if err := parseConnectToOption(o, "a.test:443:b.test:70000"); err == nil {
		t.Fatal("expected target port rejection")
	}
	if err := parseConnectToOption(o, "a.test:443:b.test:8443"); err != nil {
		t.Fatalf("valid connect-to rejected: %v", err)
	}
}

func TestParseResolveRequiresIP(t *testing.T) {
	o := NewOptions()
	if err := parseResolveOption(o, "example.test:443:not-an-ip"); err == nil {
		t.Fatal("expected resolve address rejection")
	}
	if err := parseResolveOption(o, "example.test:bad:127.0.0.1"); err == nil {
		t.Fatal("expected resolve port rejection")
	}
	if err := parseResolveOption(o, "example.test:443:127.0.0.1,::1"); err != nil {
		t.Fatalf("valid resolve rejected: %v", err)
	}
}

func TestValidateDohURL(t *testing.T) {
	valid := []string{"https://dns.example/query", "https://127.0.0.1:8443/dns-query"}
	for _, value := range valid {
		if err := validateDohURL(value); err != nil {
			t.Errorf("validateDohURL(%q) = %v", value, err)
		}
	}
	invalid := []string{
		"http://dns.example/query",
		"https://user:pass@dns.example/query",
		"https://dns.example/query#fragment",
		"https:///query",
		"",
	}
	for _, value := range invalid {
		if err := validateDohURL(value); err == nil {
			t.Errorf("validateDohURL(%q) accepted", value)
		}
	}
}

func TestValidateConnectTarget(t *testing.T) {
	valid := []string{"example.test:443", "127.0.0.1:8080", "[::1]:443"}
	for _, target := range valid {
		if err := ValidateConnectTarget(target); err != nil {
			t.Errorf("ValidateConnectTarget(%q) = %v", target, err)
		}
	}
	invalid := []string{
		"example.test:443\r\nX: 1",
		"example.test",
		"example.test:0",
		"example.test:65536",
		":443",
		"[::1]",
	}
	for _, target := range invalid {
		if err := ValidateConnectTarget(target); err == nil {
			t.Errorf("ValidateConnectTarget(%q) accepted", target)
		}
	}
}

func TestValidateResolveMaps(t *testing.T) {
	if err := ValidateResolveMaps(
		map[string][]string{"example.test:443": {"127.0.0.1"}},
		map[string]string{"a.test:443": "b.test:8443"},
	); err != nil {
		t.Fatalf("valid maps rejected: %v", err)
	}
	if err := ValidateResolveMaps(map[string][]string{"example.test:443": {"not-ip"}}, nil); err == nil {
		t.Fatal("expected resolve map rejection")
	}
	if err := ValidateResolveMaps(nil, map[string]string{"a.test:443": "b.test:443\r\nX: 1"}); err == nil {
		t.Fatal("expected connect-to map rejection")
	}
}
