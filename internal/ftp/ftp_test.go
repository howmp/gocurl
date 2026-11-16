package ftp

import (
	"errors"
	"net/textproto"
	"net/url"
	"testing"

	"gocurl/internal/config"
)

func TestActiveFTPListenAddrDashUsesControlLocalIP(t *testing.T) {
	o := config.NewOptions()
	o.FtpPort = "-"
	addr, err := activeFTPListenAddr(o, "127.0.0.1:21")
	if err != nil {
		t.Fatal(err)
	}
	if addr != "127.0.0.1:0" {
		t.Fatalf("listen addr = %q, want 127.0.0.1:0", addr)
	}
}

func TestFTPRemotePathDecodesOnce(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"ftp://example.test/%252e%252e/etc/passwd", "/%2e%2e/etc/passwd"},
		{"ftp://example.test/%250d%250a", "/%0d%0a"},
		{"ftp://example.test/file;type=i", "/file"},
		{"ftp://example.test/plain", "/plain"},
	}
	for _, tc := range cases {
		u, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := ftpRemotePath(u); got != tc.want {
			t.Errorf("ftpRemotePath(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestFTPUploadOffset(t *testing.T) {
	o := config.NewOptions()
	if offset, err := ftpUploadOffset(o, func() (int64, error) { return 0, nil }); err != nil || offset != 0 {
		t.Fatalf("no resume: offset=%d err=%v", offset, err)
	}

	o.ContinueAtSet = true
	o.ResumeOffset = 42
	if offset, err := ftpUploadOffset(o, func() (int64, error) { return 0, nil }); err != nil || offset != 42 {
		t.Fatalf("explicit offset: offset=%d err=%v", offset, err)
	}

	o.ResumeOffset = -1
	if offset, err := ftpUploadOffset(o, func() (int64, error) { return 10, nil }); err != nil || offset != 10 {
		t.Fatalf("remote size: offset=%d err=%v", offset, err)
	}

	if _, err := ftpUploadOffset(o, func() (int64, error) {
		return 0, &textproto.Error{Code: 550, Msg: "not found"}
	}); err != nil {
		t.Fatalf("550 should resume from zero: %v", err)
	}

	if _, err := ftpUploadOffset(o, func() (int64, error) {
		return 0, errors.New("temporary failure")
	}); err == nil {
		t.Fatal("expected SIZE failure to propagate")
	}

	o.UploadFile = "-"
	if _, err := ftpUploadOffset(o, func() (int64, error) { return 0, nil }); err == nil {
		t.Fatal("expected stdin resume error")
	}
}
