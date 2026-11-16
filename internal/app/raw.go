package app

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"gocurl/internal/config"
	"gocurl/internal/httpx"
	"gocurl/internal/output"
	"gocurl/internal/tlsname"
	"gocurl/internal/transport"
	"io"
	"net"
	stdhttp "net/http"
	"net/url"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	"github.com/bogdanfinn/tls-client/profiles"
	utls "github.com/bogdanfinn/utls"
	"golang.org/x/net/http/httpguts"
)

func runRawHTTP(o *config.Options, u *url.URL, idx int, stdout, stderr io.Writer) error {
	if o.Location {
		return fmt.Errorf("raw mode does not support redirects")
	}
	if o.Retries > 0 {
		return fmt.Errorf("raw mode does not support --retry")
	}
	if o.ContinueAtSet {
		return fmt.Errorf("raw mode does not support --continue-at")
	}
	if o.AuthMode == "digest" || o.AuthMode == "ntlm" || o.AuthMode == "negotiate" || o.AuthMode == "anyauth" {
		return fmt.Errorf("raw mode only supports basic authentication")
	}
	if o.HttpVersion == "2-prior" {
		return fmt.Errorf("raw mode only supports HTTP/1.x")
	}
	var rawData []byte
	var err error
	if o.RawRequestFile != "" {
		rawData, err = config.ReadLocalFile(o.RawRequestFile)
		if err != nil {
			return err
		}
	}
	var body []byte
	var contentType string
	if rawData == nil {
		body, contentType, err = httpx.BuildBody(o)
		if err != nil {
			return err
		}
	}
	method := httpx.RequestMethod(o, len(body) > 0)
	if rawData != nil {
		method = rawRequestMethod(rawData)
	}
	target := o.RequestTarget
	if target == "" {
		target = u.RequestURI()
	}
	if target == "" {
		target = "/"
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = transport.DefaultPort(u.Scheme)
	}
	start := time.Now()
	ctx := context.Background()
	timeout := o.MaxTime
	if timeout <= 0 {
		timeout = 24 * time.Hour
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	proxyRaw, err := transport.ProxyURLFor(o, u)
	if err != nil {
		return err
	}
	if o.UnixSocket != "" && proxyRaw != "" {
		return fmt.Errorf("option --unix-socket cannot be combined with a proxy")
	}
	var conn net.Conn
	if proxyRaw != "" {
		dial, err := transport.NewChainProxyContext(o, proxyRaw)
		if err != nil {
			return err
		}
		conn, err = dial(ctx, "tcp", net.JoinHostPort(host, port))
	} else if o.UnixSocket != "" {
		conn, err = transport.DialUnixContext(o.UnixSocket, o.AbstractUnix)(ctx, "tcp", net.JoinHostPort(host, port))
	} else {
		conn, err = transport.DialHTTPContext(o, ctx, "tcp", net.JoinHostPort(host, port))
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	if u.Scheme == "https" {
		cfg, err := transport.RawTLSConfig(o, host)
		if err != nil {
			return err
		}
		tlsConn := utls.UClient(conn, cfg, profiles.Chrome_152.GetClientHelloId(), false, true, true)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return err
		}
		conn = tlsConn
	}

	if rawData != nil {
		if o.Verbose {
			fmt.Fprintf(stderr, "* Sending raw request from %s\n", o.RawRequestFile)
		}
		if _, err := conn.Write(rawData); err != nil {
			if ctx.Err() != nil {
				return newTimeoutError(o, 0)
			}
			return err
		}
	} else {
		req, err := fhttp.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
		if err != nil {
			return err
		}
		httpx.ApplyHeaders(req, o, contentType, body)
		if err := httpx.ValidateUserHeaders(o); err != nil {
			return err
		}
		if o.RequestTarget != "" {
			req.URL.Opaque = o.RequestTarget
		}
		if err := validateRawRequest(req, target); err != nil {
			return err
		}
		if o.Verbose {
			output.PrintRequestVerbose(stderr, req, o)
		}

		proto := "HTTP/1.1"
		if o.HttpVersion == "1.0" {
			proto = "HTTP/1.0"
		}
		var request bytes.Buffer
		fmt.Fprintf(&request, "%s %s %s\r\n", req.Method, target, proto)
		hostValue := req.Host
		if hostValue == "" {
			hostValue = u.Host
		}
		fmt.Fprintf(&request, "Host: %s\r\n", hostValue)
		order := req.Header[fhttp.HeaderOrderKey]
		written := make(map[string]bool)
		for _, name := range order {
			lower := strings.ToLower(name)
			if lower == "host" || strings.EqualFold(name, fhttp.HeaderOrderKey) || strings.EqualFold(name, fhttp.PHeaderOrderKey) || written[lower] {
				continue
			}
			written[lower] = true
			writeRawHeader(&request, req.Header, name)
		}
		for name := range req.Header {
			lower := strings.ToLower(name)
			if lower == "host" || strings.EqualFold(name, fhttp.HeaderOrderKey) || strings.EqualFold(name, fhttp.PHeaderOrderKey) || written[lower] {
				continue
			}
			written[lower] = true
			writeRawHeader(&request, req.Header, name)
		}
		request.WriteString("\r\n")
		request.Write(body)
		if _, err := conn.Write(request.Bytes()); err != nil {
			if ctx.Err() != nil {
				return newTimeoutError(o, 0)
			}
			return err
		}
	}

	req, err := fhttp.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		req, _ = fhttp.NewRequestWithContext(ctx, stdhttp.MethodGet, u.String(), nil)
	}
	reader := bufio.NewReader(transport.LimitResponseHeaderReader(conn))
	stdResp, err := stdhttp.ReadResponse(reader, &stdhttp.Request{Method: method})
	if err != nil {
		if ctx.Err() != nil {
			return newTimeoutError(o, 0)
		}
		return err
	}
	defer stdResp.Body.Close()
	resp := &fhttp.Response{
		Status:        stdResp.Status,
		StatusCode:    stdResp.StatusCode,
		Proto:         stdResp.Proto,
		Header:        make(fhttp.Header, len(stdResp.Header)),
		Body:          stdResp.Body,
		ContentLength: stdResp.ContentLength,
		Request:       req,
	}
	for name, values := range stdResp.Header {
		resp.Header[name] = append([]string(nil), values...)
	}
	if stdResp.TLS != nil {
		resp.TLS = &utls.ConnectionState{
			Version:            stdResp.TLS.Version,
			CipherSuite:        stdResp.TLS.CipherSuite,
			NegotiatedProtocol: stdResp.TLS.NegotiatedProtocol,
			ServerName:         stdResp.TLS.ServerName,
			VerifiedChains:     stdResp.TLS.VerifiedChains,
			PeerCertificates:   stdResp.TLS.PeerCertificates,
		}
	}
	info := &output.ResponseInfo{
		Url:             u.String(),
		Scheme:          u.Scheme,
		HttpVersion:     shortHTTPVersion(resp.Proto),
		StatusCode:      resp.StatusCode,
		ContentType:     resp.Header.Get("Content-Type"),
		SizeHeader:      output.ResponseHeaderSize(resp),
		SslVerifyResult: sslVerifyResult(resp, o),
		Started:         start,
	}
	info.RemoteAddr = host
	if resp.TLS != nil {
		info.TlsVersion = tlsname.Version(resp.TLS.Version)
		info.Alpn = resp.TLS.NegotiatedProtocol
	}
	if err := output.WriteHeaderDump(o, resp, idx, stdout); err != nil {
		return err
	}
	return emitResponse(o, o, u, idx, stdout, resp, info, ctx, start)
}

func rawRequestMethod(data []byte) string {
	line := data
	if idx := bytes.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	fields := strings.Fields(strings.TrimRight(string(line), "\r"))
	if len(fields) == 0 {
		return stdhttp.MethodGet
	}
	return fields[0]
}

func validateRawRequest(req *fhttp.Request, target string) error {
	if req == nil || !httpguts.ValidHeaderFieldName(req.Method) {
		return fmt.Errorf("raw mode: invalid request method %q", req.Method)
	}
	if err := httpx.ValidateRequestTarget(target); err != nil {
		return fmt.Errorf("raw mode: %w", err)
	}
	if req.Host != "" && !httpguts.ValidHeaderFieldValue(req.Host) {
		return fmt.Errorf("raw mode: invalid Host header")
	}
	for name, values := range req.Header {
		if strings.EqualFold(name, fhttp.HeaderOrderKey) || strings.EqualFold(name, fhttp.PHeaderOrderKey) {
			continue
		}
		if !httpguts.ValidHeaderFieldName(name) {
			return fmt.Errorf("raw mode: invalid header name %q", name)
		}
		for _, value := range values {
			if !httpguts.ValidHeaderFieldValue(value) {
				return fmt.Errorf("raw mode: invalid value for header %q", name)
			}
		}
	}
	return nil
}

func writeRawHeader(w *bytes.Buffer, headers fhttp.Header, name string) {
	var values []string
	for key, vals := range headers {
		if strings.EqualFold(key, name) {
			values = vals
			break
		}
	}
	if len(values) == 0 {
		return
	}
	canonical := fhttp.CanonicalHeaderKey(name)
	for _, value := range values {
		fmt.Fprintf(w, "%s: %s\r\n", canonical, value)
	}
}
