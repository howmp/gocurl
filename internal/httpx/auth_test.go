package httpx

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"gocurl/internal/config"
)

func netrcOptions(t *testing.T, content string) *config.Options {
	t.Helper()
	path := filepath.Join(t.TempDir(), "netrc")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	o := config.NewOptions()
	o.NetrcFile = path
	return o
}

func TestNetrcMachineWinsOverDefault(t *testing.T) {
	o := netrcOptions(t, `default login defuser password defpass
machine example.test login machuser password machpass
`)
	user, pass, ok := netrcCredentials(o, "example.test")
	if !ok || user != "machuser" || pass != "machpass" {
		t.Fatalf("got %q/%q ok=%v", user, pass, ok)
	}
}

func TestNetrcFallsBackToDefault(t *testing.T) {
	o := netrcOptions(t, `default login defuser password defpass
machine example.test login machuser password machpass
`)
	user, pass, ok := netrcCredentials(o, "other.test")
	if !ok || user != "defuser" || pass != "defpass" {
		t.Fatalf("got %q/%q ok=%v", user, pass, ok)
	}
}

func TestNetrcMachineAfterDefaultAndMacdef(t *testing.T) {
	o := netrcOptions(t, `default login defuser password defpass
macdef init
	put file

machine example.test login machuser password machpass
`)
	user, pass, ok := netrcCredentials(o, "EXAMPLE.TEST")
	if !ok || user != "machuser" || pass != "machpass" {
		t.Fatalf("got %q/%q ok=%v", user, pass, ok)
	}
}

func TestNetrcNoMatch(t *testing.T) {
	o := netrcOptions(t, "machine other.test login u password p\n")
	if _, _, ok := netrcCredentials(o, "example.test"); ok {
		t.Fatal("unexpected credentials")
	}
}

func authSchemes(challenges []authChallenge) []string {
	out := make([]string, 0, len(challenges))
	for _, challenge := range challenges {
		out = append(out, challenge.scheme)
	}
	return out
}

func TestParseAuthChallengeTokenBoundaries(t *testing.T) {
	cases := []struct {
		header string
		want   []string
	}{
		{`Bearer realm="digest"`, nil},
		{`Xbasic`, nil},
		{`Digest realm="basic", nonce="n"`, []string{"DIGEST"}},
		{`Basic realm="a", Digest realm="b", nonce="n"`, []string{"BASIC", "DIGEST"}},
		{`Digest realm="a, Basic b", nonce="n"`, []string{"DIGEST"}},
		{`NTLM TlRMTVNTUAAB`, []string{"NTLM"}},
		{`Bearer abc, Negotiate TlRMTVNTUAAB`, []string{"NEGOTIATE"}},
	}
	for _, tc := range cases {
		got := authSchemes(parseAuthChallenge(tc.header))
		if len(got) != len(tc.want) {
			t.Errorf("parseAuthChallenge(%q) = %v, want %v", tc.header, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("parseAuthChallenge(%q) = %v, want %v", tc.header, got, tc.want)
				break
			}
		}
	}
}

func FuzzParseAuthChallenge(f *testing.F) {
	f.Add(`Digest realm="test", nonce="abc", qop="auth"`)
	f.Add("İ digest")
	f.Add(`Bearer realm="digest", Basic realm="x"`)
	f.Fuzz(func(t *testing.T, value string) {
		_ = parseAuthChallenge(value)
	})
}

func TestParseAuthChallengeUnicodeNoPanic(t *testing.T) {
	if got := parseAuthChallenge("İ digest"); len(got) != 0 {
		t.Fatalf("unexpected challenges: %v", authSchemes(got))
	}
	if got := parseAuthChallenge("Digest realm=\"x\", İ digest"); len(got) != 1 {
		t.Fatalf("unexpected challenges: %v", authSchemes(got))
	}
}

func TestParseDigestChallengeKeepsParams(t *testing.T) {
	challenges := parseAuthChallenge(`Digest realm="r", nonce="n", qop="auth,auth-int"`)
	if len(challenges) != 1 || challenges[0].scheme != "DIGEST" {
		t.Fatalf("challenges = %v", authSchemes(challenges))
	}
	if challenges[0].params["realm"] != "r" || challenges[0].params["nonce"] != "n" {
		t.Fatalf("params = %#v", challenges[0].params)
	}
	if challenges[0].params["qop"] != "auth,auth-int" {
		t.Fatalf("qop = %q", challenges[0].params["qop"])
	}
}

func resetNonceCounts() {
	nonceCountMu.Lock()
	nonceCounts = make(map[string]*nonceCountEntry)
	nonceCountMu.Unlock()
}

func TestNonceCountMonotonicAndScoped(t *testing.T) {
	resetNonceCounts()
	defer resetNonceCounts()
	first, _ := url.Parse("https://example.test:8443/x")
	second, _ := url.Parse("https://example.test:9443/x")
	got, err := nextNonceCount(first, "realm", "nonce")
	if err != nil || got != "00000001" {
		t.Fatalf("first count = %q err = %v", got, err)
	}
	got, err = nextNonceCount(first, "realm", "nonce")
	if err != nil || got != "00000002" {
		t.Fatalf("second count = %q err = %v", got, err)
	}
	got, err = nextNonceCount(second, "realm", "nonce")
	if err != nil {
		t.Fatal(err)
	}
	if got != "00000001" {
		t.Fatalf("different origin count = %q, want 00000001", got)
	}
}

func TestNonceCountTableFullDoesNotReset(t *testing.T) {
	resetNonceCounts()
	defer resetNonceCounts()
	u, _ := url.Parse("https://example.test/x")
	if got, err := nextNonceCount(u, "realm", "nonce"); err != nil || got != "00000001" {
		t.Fatalf("count = %q err = %v", got, err)
	}
	nonceCountMu.Lock()
	for i := 0; i < maxNonceCounts; i++ {
		nonceCounts[string(rune(i))] = &nonceCountEntry{count: 1}
	}
	nonceCountMu.Unlock()
	if got, err := nextNonceCount(u, "realm", "nonce"); err != nil || got != "00000002" {
		t.Fatalf("existing count = %q err = %v", got, err)
	}
	if _, err := nextNonceCount(u, "realm", "other"); err == nil {
		t.Fatal("expected nonce table full error")
	}
}
