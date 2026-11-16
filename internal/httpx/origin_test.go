package httpx

import (
	"net/url"
	"testing"
)

func TestSameOriginURL(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"https://a.test/x", "https://A.TEST./y", true},
		{"https://a.test/x", "https://a.test:443/y", true},
		{"http://a.test/x", "http://a.test:80/y", true},
		{"https://a.test/x", "https://a.test:8443/y", false},
		{"https://a.test/x", "http://a.test/y", false},
		{"http://a.test/x", "https://a.test/y", false},
		{"https://a.test/x", "https://b.test/y", false},
		{"wss://a.test/x", "wss://a.test:443/y", true},
		{"wss://a.test/x", "ws://a.test/y", false},
	}
	for _, tc := range cases {
		a, err := url.Parse(tc.a)
		if err != nil {
			t.Fatal(err)
		}
		b, err := url.Parse(tc.b)
		if err != nil {
			t.Fatal(err)
		}
		if got := SameOriginURL(a, b); got != tc.want {
			t.Errorf("SameOriginURL(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
