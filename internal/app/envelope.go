package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"gocurl/internal/config"
	"gocurl/internal/exitcode"
	"gocurl/internal/output"
	"gocurl/internal/tlsname"
	"io"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	http "github.com/bogdanfinn/fhttp"
	utls "github.com/bogdanfinn/utls"
)

const envelopeBodyLimit = 1 << 20

type envelopeHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type envelopeBody struct {
	Size      int64  `json:"size"`
	Encoding  string `json:"encoding,omitempty"`
	Text      string `json:"text,omitempty"`
	Base64    string `json:"base64,omitempty"`
	File      string `json:"file,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

type envelopeRequest struct {
	Method    string           `json:"method,omitempty"`
	URL       string           `json:"url"`
	Target    string           `json:"target,omitempty"`
	Headers   []envelopeHeader `json:"headers,omitempty"`
	BodyBytes int64            `json:"body_bytes,omitempty"`
}

type envelopeTiming struct {
	Total         float64 `json:"total_seconds"`
	Connect       float64 `json:"connect_seconds,omitempty"`
	AppConnect    float64 `json:"appconnect_seconds,omitempty"`
	StartTransfer float64 `json:"starttransfer_seconds,omitempty"`
}

type envelopeCert struct {
	Subject     string   `json:"subject,omitempty"`
	Issuer      string   `json:"issuer,omitempty"`
	Serial      string   `json:"serial,omitempty"`
	DNSNames    []string `json:"dns_names,omitempty"`
	IPAddresses []string `json:"ip_addresses,omitempty"`
	NotBefore   string   `json:"not_before,omitempty"`
	NotAfter    string   `json:"not_after,omitempty"`
	SHA256      string   `json:"sha256,omitempty"`
}

type envelopeTLS struct {
	Version          string         `json:"version,omitempty"`
	ALPN             string         `json:"alpn,omitempty"`
	CipherSuite      string         `json:"cipher_suite,omitempty"`
	Verified         bool           `json:"verified"`
	PeerCertificates []envelopeCert `json:"peer_certificates,omitempty"`
}

type envelopeResponse struct {
	Status       int              `json:"status"`
	StatusText   string           `json:"status_text,omitempty"`
	Proto        string           `json:"proto,omitempty"`
	EffectiveURL string           `json:"effective_url,omitempty"`
	Headers      []envelopeHeader `json:"headers,omitempty"`
	Redirects    []string         `json:"redirects,omitempty"`
	Cookies      []string         `json:"set_cookie,omitempty"`
	Body         envelopeBody     `json:"body"`
	TLS          *envelopeTLS     `json:"tls,omitempty"`
}

type envelopeError struct {
	Code    int    `json:"code"`
	Kind    string `json:"kind,omitempty"`
	Message string `json:"message,omitempty"`
}

type jsonEnvelope struct {
	URL      string            `json:"url"`
	Request  envelopeRequest   `json:"request"`
	Response *envelopeResponse `json:"response,omitempty"`
	Timing   envelopeTiming    `json:"timing"`
	Retries  int               `json:"retries,omitempty"`
	Error    *envelopeError    `json:"error,omitempty"`
	ExitCode int               `json:"exit_code"`
	written  bool
}

func newJSONEnvelope(u *url.URL) *jsonEnvelope {
	env := &jsonEnvelope{}
	if u != nil {
		env.URL = u.String()
		env.Request.URL = u.String()
	}
	return env
}

func (env *jsonEnvelope) setRequest(req *http.Request, o *config.Options, bodyLen int64) {
	if env == nil || req == nil {
		return
	}
	env.Request.Method = req.Method
	env.Request.URL = req.URL.String()
	env.Request.BodyBytes = bodyLen
	env.Request.Target = o.RequestTarget
	if env.Request.Target == "" {
		env.Request.Target = req.URL.RequestURI()
	}
	env.Request.Headers = requestHeaderList(req, o.ShowSecrets)
}

func requestHeaderList(req *http.Request, showSecrets bool) []envelopeHeader {
	var out []envelopeHeader
	seen := make(map[string]bool)
	if req.Host != "" {
		out = append(out, envelopeHeader{Name: "Host", Value: req.Host})
		seen["host"] = true
	}
	order := req.Header[http.HeaderOrderKey]
	for _, name := range order {
		lower := strings.ToLower(name)
		if lower == "host" || strings.EqualFold(name, http.HeaderOrderKey) || strings.EqualFold(name, http.PHeaderOrderKey) || seen[lower] {
			continue
		}
		values := req.Header.Values(http.CanonicalHeaderKey(name))
		for _, value := range values {
			out = append(out, envelopeHeader{Name: http.CanonicalHeaderKey(name), Value: output.RedactHeaderValue(name, value, showSecrets)})
		}
		seen[lower] = true
	}
	var extra []string
	for name := range req.Header {
		if strings.EqualFold(name, http.HeaderOrderKey) || strings.EqualFold(name, http.PHeaderOrderKey) {
			continue
		}
		lower := strings.ToLower(name)
		if !seen[lower] && lower != "host" {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	for _, name := range extra {
		for _, value := range req.Header.Values(name) {
			out = append(out, envelopeHeader{Name: name, Value: output.RedactHeaderValue(name, value, showSecrets)})
		}
	}
	return out
}

func (env *jsonEnvelope) setResponse(resp *http.Response, showSecrets bool) {
	if env == nil || resp == nil {
		return
	}
	r := &envelopeResponse{
		Status:       resp.StatusCode,
		StatusText:   resp.Status,
		Proto:        resp.Proto,
		EffectiveURL: resp.Request.URL.String(),
		Headers:      responseHeaderList(resp, showSecrets),
		Redirects:    responseRedirects(resp.Request),
	}
	if values := resp.Header.Values("Set-Cookie"); len(values) > 0 {
		if !showSecrets {
			values = []string{"[REDACTED]"}
		}
		r.Cookies = values
	}
	if resp.TLS != nil {
		r.TLS = responseTLS(resp.TLS)
	}
	env.Response = r
}

func responseHeaderList(resp *http.Response, showSecrets bool) []envelopeHeader {
	if resp == nil {
		return nil
	}
	var keys []string
	for name := range resp.Header {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	var out []envelopeHeader
	for _, name := range keys {
		for _, value := range resp.Header.Values(name) {
			out = append(out, envelopeHeader{Name: name, Value: output.RedactHeaderValue(name, value, showSecrets)})
		}
	}
	return out
}

func responseRedirects(final *http.Request) []string {
	if final == nil {
		return nil
	}
	var reverse []string
	for req := final; req != nil && req.Response != nil; req = req.Response.Request {
		if req.Response.Request != nil && req.Response.Request.URL != nil {
			reverse = append(reverse, req.Response.Request.URL.String())
		}
	}
	chain := make([]string, 0, len(reverse)+1)
	for i := len(reverse) - 1; i >= 0; i-- {
		chain = append(chain, reverse[i])
	}
	if final.URL != nil {
		chain = append(chain, final.URL.String())
	}
	return chain
}

func responseTLS(state *utls.ConnectionState) *envelopeTLS {
	t := &envelopeTLS{
		Version:     tlsname.Version(state.Version),
		ALPN:        state.NegotiatedProtocol,
		CipherSuite: tlsname.Cipher(state.CipherSuite),
		Verified:    len(state.VerifiedChains) > 0,
	}
	for _, cert := range state.PeerCertificates {
		if cert == nil {
			continue
		}
		sum := sha256.Sum256(cert.Raw)
		t.PeerCertificates = append(t.PeerCertificates, envelopeCert{
			Subject:     cert.Subject.String(),
			Issuer:      cert.Issuer.String(),
			Serial:      cert.SerialNumber.String(),
			DNSNames:    append([]string(nil), cert.DNSNames...),
			IPAddresses: ipStrings(cert.IPAddresses),
			NotBefore:   cert.NotBefore.UTC().Format(time.RFC3339),
			NotAfter:    cert.NotAfter.UTC().Format(time.RFC3339),
			SHA256:      hex.EncodeToString(sum[:]),
		})
	}
	return t
}

func ipStrings(ips []net.IP) []string {
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		if ip != nil {
			out = append(out, ip.String())
		}
	}
	return out
}

func (env *jsonEnvelope) setTiming(info *output.ResponseInfo) {
	if env == nil || info == nil {
		return
	}
	env.Timing = envelopeTiming{
		Total:         info.TimeTotal.Seconds(),
		Connect:       info.TimeConnect.Seconds(),
		AppConnect:    info.TimeAppConnect.Seconds(),
		StartTransfer: info.TimeStartTransfer.Seconds(),
	}
	env.Retries = info.NumRetries
}

func (env *jsonEnvelope) finish(err error) {
	if err != nil {
		code := exitcode.From(err)
		env.ExitCode = code
		env.Error = &envelopeError{
			Code:    code,
			Kind:    errorKind(code),
			Message: redactErrorText(err.Error()),
		}
	}
}

func errorKind(code int) string {
	switch code {
	case 22:
		return "http_status"
	case 28:
		return "timeout"
	case 18:
		return "transfer"
	case 56:
		return "network"
	case 60:
		return "tls"
	case 63:
		return "max_filesize"
	case 6:
		return "dns"
	case 7:
		return "connect"
	case 47:
		return "redirect"
	case 1:
		return "protocol_or_request"
	}
	return "other"
}

func (env *jsonEnvelope) write(w io.Writer) error {
	if env == nil || env.written {
		return nil
	}
	env.written = true
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(env)
}

type envelopeCapture struct {
	buf       bytes.Buffer
	limit     int64
	total     int64
	truncated bool
}

func (c *envelopeCapture) Write(p []byte) (int, error) {
	c.total += int64(len(p))
	if room := c.limit - int64(c.buf.Len()); room > 0 {
		if int64(len(p)) > room {
			c.buf.Write(p[:room])
			c.truncated = true
		} else {
			c.buf.Write(p)
		}
	} else if len(p) > 0 {
		c.truncated = true
	}
	return len(p), nil
}

func (c *envelopeCapture) body() envelopeBody {
	body := envelopeBody{Size: c.total, Truncated: c.truncated}
	data := c.buf.Bytes()
	if utf8.Valid(data) {
		body.Encoding = "text"
		body.Text = string(data)
	} else {
		body.Encoding = "base64"
		body.Base64 = base64.StdEncoding.EncodeToString(data)
	}
	return body
}

func finishEnvelopeResponse(ctx context.Context, o *config.Options, env *jsonEnvelope, resp *http.Response, info *output.ResponseInfo, u *url.URL, idx int, start time.Time, stdout io.Writer) error {
	info.TimeTotal = time.Since(start)
	env.setTiming(info)
	env.setResponse(resp, o.ShowSecrets)
	if failStatus(o, resp, false) {
		return statusError(resp)
	}

	name, err := output.OutputName(o, u, idx)
	if err != nil {
		return err
	}
	fileOutput := name != "" && name != "-"
	if o.Head {
		if fileOutput {
			return fmt.Errorf("option --json-envelope with -I cannot be combined with -o")
		}
		return nil
	}

	var (
		out      io.Writer
		closeOut func() error
		capture  *envelopeCapture
	)
	if fileOutput {
		out, closeOut, err = output.OutputWriter(o, u, idx, stdout)
		if err != nil {
			return err
		}
	} else {
		capture = &envelopeCapture{limit: envelopeBodyLimit}
		out = capture
	}

	streamErr := streamResponseBody(ctx, o, resp, out, info)
	if fileOutput {
		if err := closeOut(); err != nil {
			return err
		}
		env.Response.Body = envelopeBody{Size: info.SizeDownload, Encoding: "file", File: name}
	} else {
		env.Response.Body = capture.body()
	}
	info.TimeTotal = time.Since(start)
	env.setTiming(info)
	if streamErr != nil {
		return streamErr
	}
	if failStatus(o, resp, true) {
		return statusError(resp)
	}
	return nil
}
