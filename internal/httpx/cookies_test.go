package httpx

import (
	"gocurl/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"

	http "github.com/bogdanfinn/fhttp"
)

func setCookie(t *testing.T, jar config.CookieJar, rawURL, name, value string) {
	t.Helper()
	u, err := NormalizeURL(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(u, []*http.Cookie{{Name: name, Value: value, Path: "/"}})
}

func cookieLines(text string) []string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func TestCookieExportFiltersToRequestedHosts(t *testing.T) {
	jar := NewCookieJar()
	setCookie(t, jar, "http://example.test:28090/x", "a", "1")
	setCookie(t, jar, "http://secret.test:28090/x", "s", "2")

	text := CookieFileTextForHosts(jar, []string{"example.test:28090/x"})
	lines := cookieLines(text)
	if len(lines) != 1 {
		t.Fatalf("got %d cookie lines, want 1: %q", len(lines), text)
	}
	if !strings.Contains(lines[0], "example.test") || strings.Contains(lines[0], "secret.test") {
		t.Fatalf("unexpected cookie line: %q", lines[0])
	}
}

func TestCookieExportWithoutHostsIsEmpty(t *testing.T) {
	jar := NewCookieJar()
	setCookie(t, jar, "http://example.test/x", "a", "1")

	for _, hosts := range [][]string{nil, {"://bad"}, {"ftp://example.test/x"}} {
		if lines := cookieLines(CookieFileTextForHosts(jar, hosts)); len(lines) != 0 {
			t.Fatalf("hosts %v exported cookies: %v", hosts, lines)
		}
	}
}

func TestCookieHostOnlyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cookies.txt")
	input := "# Netscape HTTP Cookie File\n" +
		"example.test\tFALSE\t/\tFALSE\t0\tsid\tX\n" +
		".example.test\tTRUE\t/\tFALSE\t0\tdomain\tY\n"
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}

	jar := NewCookieJar()
	if err := LoadCookieFile(jar, path); err != nil {
		t.Fatal(err)
	}
	text := CookieFileTextForHosts(jar, []string{"http://example.test/"})
	lines := cookieLines(text)
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), text)
	}
	for _, line := range lines {
		fields := strings.Split(line, "\t")
		switch fields[5] {
		case "sid":
			if fields[0] != "example.test" || fields[1] != "FALSE" {
				t.Fatalf("host-only cookie changed: %q", line)
			}
		case "domain":
			if fields[0] != ".example.test" || fields[1] != "TRUE" {
				t.Fatalf("domain cookie changed: %q", line)
			}
		default:
			t.Fatalf("unexpected cookie: %q", line)
		}
	}
}

func TestCookieExportRejectsInvalidDomains(t *testing.T) {
	for _, tc := range []struct {
		name   string
		host   string
		domain string
	}{
		{"public suffix", "evil.test", "test"},
		{"unrelated domain", "evil.test", "victim.test"},
		{"ip domain attribute", "127.0.0.1", "127.0.0.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jar := NewCookieJar()
			u, err := NormalizeURL("http://" + tc.host + "/")
			if err != nil {
				t.Fatal(err)
			}
			jar.SetCookies(u, []*http.Cookie{{Name: "pwn", Value: "1", Path: "/", Domain: tc.domain}})
			text := CookieFileTextForHosts(jar, []string{"http://" + tc.host + "/"})
			if lines := cookieLines(text); len(lines) != 0 {
				t.Fatalf("invalid domain was tracked: %v", lines)
			}
		})
	}
}

func TestCookieExportPreservesMaxAge(t *testing.T) {
	jar := NewCookieJar()
	u, err := NormalizeURL("http://example.test/")
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(u, []*http.Cookie{{Name: "sid", Value: "1", Path: "/", MaxAge: 3600}})
	text := CookieFileTextForHosts(jar, []string{"http://example.test/"})
	lines := cookieLines(text)
	if len(lines) != 1 {
		t.Fatalf("got %d lines: %q", len(lines), text)
	}
	fields := strings.Split(lines[0], "\t")
	if len(fields) < 7 || fields[4] == "0" {
		t.Fatalf("Max-Age was not preserved: %q", lines[0])
	}
}

func TestCookieExportStripsPortAndMatchesSubdomains(t *testing.T) {
	jar := NewCookieJar()
	setCookie(t, jar, "http://example.test:28090/x", "hostonly", "1")
	u, err := NormalizeURL("http://example.test:28090/x")
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(u, []*http.Cookie{{Name: "domain", Value: "2", Path: "/", Domain: ".example.test"}})

	sub := cookieLines(CookieFileTextForHosts(jar, []string{"http://sub.example.test/x"}))
	if len(sub) != 1 || !strings.Contains(sub[0], "domain") {
		t.Fatalf("subdomain export = %v, want only domain cookie", sub)
	}
	host := cookieLines(CookieFileTextForHosts(jar, []string{"http://example.test:1234/x"}))
	if len(host) != 2 {
		t.Fatalf("host export = %v, want both cookies", host)
	}
	for _, line := range host {
		if strings.Contains(line, ":1234") {
			t.Fatalf("exported cookie domain still has port: %q", line)
		}
	}
}

func TestCookieFileTextAllExportsUnvisitedHosts(t *testing.T) {
	jar := NewCookieJar()
	setCookie(t, jar, "http://example.test/", "a", "1")
	setCookie(t, jar, "http://other.test/", "b", "2")
	text := CookieFileTextAll(jar)
	if lines := cookieLines(text); len(lines) != 2 {
		t.Fatalf("all export lines = %v", lines)
	}
}
