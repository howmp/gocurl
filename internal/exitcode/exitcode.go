package exitcode

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"syscall"

	utls "github.com/bogdanfinn/utls"
)

const (
	OK               = 0
	Failed           = 1
	BadURL           = 3
	CouldntResolve   = 6
	CouldntConnect   = 7
	PartialFile      = 18
	HTTPReturned     = 22
	WriteFailure     = 23
	Timeout          = 28
	RangeError       = 33
	TooManyRedirects = 47
	RecvFailure      = 56
	SSLVerify        = 60
	BadEncoding      = 61
)

type coded interface{ ExitCode() int }

func From(err error) int {
	if err == nil {
		return 0
	}
	var c coded
	if errors.As(err, &c) {
		if code := c.ExitCode(); code > 0 {
			return code
		}
		return 1
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return 28
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return 28
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return 6
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && errors.Is(opErr.Err, syscall.ECONNREFUSED) {
		return 7
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return 60
	}
	var uCertErr *utls.CertificateVerificationError
	if errors.As(err, &uCertErr) {
		return 60
	}
	var unknownAuth x509.UnknownAuthorityError
	if errors.As(err, &unknownAuth) {
		return 60
	}
	var hostErr x509.HostnameError
	if errors.As(err, &hostErr) {
		return 60
	}
	var invalidCert x509.CertificateInvalidError
	if errors.As(err, &invalidCert) {
		return 60
	}
	var recordErr tls.RecordHeaderError
	if errors.As(err, &recordErr) {
		return 35
	}
	var uRecordErr utls.RecordHeaderError
	if errors.As(err, &uRecordErr) {
		return 35
	}
	if errors.Is(err, io.EOF) {
		return 52
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return 18
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return 56
	}
	if errors.Is(err, os.ErrNotExist) {
		return 26
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return 3
	}
	return 1
}

type WriteError struct{ Err error }

func (e *WriteError) Error() string {
	if e.Err == nil {
		return "write error"
	}
	return e.Err.Error()
}
func (e *WriteError) Unwrap() error { return e.Err }
func (e *WriteError) ExitCode() int { return 23 }

type Coded struct {
	Code int
	Msg  string
}

func (e *Coded) Error() string { return e.Msg }
func (e *Coded) ExitCode() int {
	if e.Code > 0 {
		return e.Code
	}
	return 1
}

func IsTimeout(err error) bool {
	if err == nil {
		return false
	}
	var coded *Coded
	if errors.As(err, &coded) && coded.Code == Timeout {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
