package exitcode

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"syscall"
	"testing"
)

type codedTestError struct{ code int }

func (e *codedTestError) Error() string { return "coded" }
func (e *codedTestError) ExitCode() int { return e.code }

func TestFrom(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"coded", &codedTestError{code: 61}, 61},
		{"deadline", context.DeadlineExceeded, 28},
		{"dns", &net.DNSError{Err: "no such host", Name: "x", IsNotFound: true}, 6},
		{"eof", io.EOF, 52},
		{"unexpected eof", io.ErrUnexpectedEOF, 18},
		{"connection reset", syscall.ECONNRESET, 56},
		{"broken pipe", syscall.EPIPE, 56},
		{"url wrap unexpected eof", &url.Error{Op: "Get", URL: "http://x", Err: io.ErrUnexpectedEOF}, 18},
		{"url parse", &url.Error{Op: "parse", URL: "x", Err: errors.New("bad url")}, 3},
		{"not exist", os.ErrNotExist, 26},
		{"write error", &WriteError{Err: errors.New("disk full")}, 23},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := From(tc.err); got != tc.want {
				t.Fatalf("From(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}
