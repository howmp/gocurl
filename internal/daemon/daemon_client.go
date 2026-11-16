package daemon

import (
	"bufio"
	"context"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"gocurl/internal/config"
	"gocurl/internal/exitcode"
	"gocurl/internal/httpx"
	"gocurl/internal/transport"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	http "github.com/bogdanfinn/fhttp"
	utls "github.com/bogdanfinn/utls"
)

const maxRequestBodySize = maxDaemonRequestBody

var ErrRequestBodyTooLarge = errors.New("request body exceeds daemon limit")

var ErrBusy = errors.New("daemon busy")

func checkDaemonBodySize(length int64) error {
	if length > maxRequestBodySize {
		return ErrRequestBodyTooLarge
	}
	return nil
}

func Ensure(o *config.Options) (string, error) {
	if !o.Persist || os.Getenv("GOCURL_NO_DAEMON") == "1" {
		return "", nil
	}
	if o.CookieFile == "-" {
		return "", nil
	}
	socket := SocketPathFor(o.Session)
	if _, err := os.Lstat(socket); err == nil {
		if err := validateDaemonSocket(socket); err != nil {
			return "", err
		}
	}
	if resp, err := pingDaemon(socket, 500*time.Millisecond); err == nil {
		if resp.Version == daemonProtocolVersion {
			return socket, nil
		}
		_ = Stop(socket)
	}
	if err := startDaemonProcess(socket); err != nil {
		return "", err
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := pingDaemon(socket, 200*time.Millisecond)
		if err == nil && resp.Version == daemonProtocolVersion {
			return socket, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return "", fmt.Errorf("daemon did not become ready")
}

func pingDaemon(socket string, timeout time.Duration) (*daemonResponse, error) {
	conn, err := net.DialTimeout("unix", socket, timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	br := bufio.NewReader(conn)
	bw := bufio.NewWriter(conn)
	req := daemonRequest{Version: daemonProtocolVersion, Op: daemonOpPing}
	if err := writeDaemonFrame(bw, req); err != nil {
		return nil, err
	}
	if err := bw.Flush(); err != nil {
		return nil, err
	}
	var resp daemonResponse
	if err := readDaemonFrame(br, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func Stop(socket string) error {
	conn, err := net.DialTimeout("unix", socket, 500*time.Millisecond)
	if err != nil {
		_ = removeIfSocket(socket)
		return nil
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	bw := bufio.NewWriter(conn)
	if err := writeDaemonFrame(bw, daemonRequest{Version: daemonProtocolVersion, Op: daemonOpShutdown}); err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	br := bufio.NewReader(conn)
	var resp daemonResponse
	_ = readDaemonFrame(br, &resp)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err != nil {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not stop")
}

func canRemoveStaleSocket(socket string) error {
	conn, err := net.DialTimeout("unix", socket, 200*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		return fmt.Errorf("daemon socket %s is busy", socket)
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("daemon socket %s is not stale: %w", socket, err)
}

func removeIfSocket(socket string) error {
	if info, err := os.Stat(socket); err == nil && info.Mode()&0o170000 == 0o140000 {
		return os.Remove(socket)
	}
	return nil
}

func Do(o *config.Options, req *http.Request, body []byte) (*http.Response, string, func(), error) {
	if err := checkDaemonBodySize(int64(len(body))); err != nil {
		return nil, "", func() {}, err
	}
	conn, err := net.DialTimeout("unix", o.DaemonSocket, 5*time.Second)
	if err != nil {
		return nil, "", func() {}, err
	}
	deadline := time.Time{}
	if o.MaxTime > 0 {
		deadline = time.Now().Add(o.MaxTime)
	} else {
		deadline = time.Now().Add(24 * time.Hour)
	}
	_ = conn.SetDeadline(deadline)

	headers, order := headersForDaemon(req)
	transport := daemonTransportFromOptions(o, req.URL)
	dreq := daemonRequest{
		Version:       daemonProtocolVersion,
		Op:            daemonOpRequest,
		URL:           req.URL.String(),
		Method:        req.Method,
		Host:          req.Host,
		RequestTarget: o.RequestTarget,
		Headers:       headers,
		HeaderOrder:   order,
		BodyLength:    int64(len(body)),
		Transport:     transport,
		CookieHeader:  o.CookieHeader,
	}
	if header := httpx.CookieHeaderForURL(o.CookieJar, req.URL); header != "" {
		if dreq.CookieHeader == "" {
			dreq.CookieHeader = header
		} else {
			dreq.CookieHeader += "; " + header
		}
	}

	br := bufio.NewReader(conn)
	bw := bufio.NewWriter(conn)
	if err := writeDaemonFrame(bw, dreq); err != nil {
		conn.Close()
		return nil, "", func() {}, err
	}
	if len(body) > 0 {
		if _, err := bw.Write(body); err != nil {
			conn.Close()
			return nil, "", func() {}, err
		}
	}
	if err := bw.Flush(); err != nil {
		conn.Close()
		return nil, "", func() {}, err
	}

	var dresp daemonResponse
	if err := readDaemonFrame(br, &dresp); err != nil {
		conn.Close()
		return nil, "", func() {}, err
	}
	if dresp.Version != daemonProtocolVersion {
		conn.Close()
		return nil, "", func() {}, fmt.Errorf("daemon protocol version mismatch")
	}
	if dresp.Error != "" {
		conn.Close()
		if dresp.ErrorCode == 4 {
			return nil, "", func() {}, ErrBusy
		}
		code := dresp.ErrorCode
		if code == 0 {
			code = 1
		}
		return nil, "", func() {}, &exitcode.Coded{Code: code, Msg: dresp.Error}
	}

	effective, err := url.Parse(dresp.EffectiveURL)
	if err != nil {
		effective = req.URL
	}
	resp := &http.Response{
		Status:        dresp.Status,
		StatusCode:    dresp.StatusCode,
		Proto:         dresp.Proto,
		Header:        make(http.Header, len(dresp.Headers)),
		ContentLength: dresp.BodyLength,
		Request: &http.Request{
			Method: req.Method,
			URL:    effective,
			Host:   effective.Host,
		},
	}
	resp.Request.Response = redirectResponseChain(dresp.Redirects)
	for name, values := range dresp.Headers {
		for _, value := range values {
			resp.Header.Add(name, value)
		}
	}
	if dresp.TLSVersion != 0 {
		resp.TLS = &utls.ConnectionState{
			Version:            dresp.TLSVersion,
			CipherSuite:        dresp.CipherSuite,
			NegotiatedProtocol: dresp.ALPN,
		}
		if dresp.TLSVerified {
			resp.TLS.VerifiedChains = make([][]*x509.Certificate, 1)
		}
		for _, der := range dresp.TLSPeerCerts {
			if cert, err := x509.ParseCertificate(der); err == nil {
				resp.TLS.PeerCertificates = append(resp.TLS.PeerCertificates, cert)
			}
		}
	}
	var reader io.Reader = br
	if dresp.BodyLength >= 0 {
		reader = io.LimitReader(br, dresp.BodyLength)
	}
	resp.Body = &daemonBody{reader: reader, conn: conn, remaining: dresp.BodyLength, chunked: dresp.BodyChunked}
	cleanup := func() {
		_ = conn.Close()
	}
	return resp, dresp.RemoteAddr, cleanup, nil
}

type daemonBody struct {
	reader    io.Reader
	conn      net.Conn
	remaining int64
	chunked   bool
	chunkLeft uint32
}

func (b *daemonBody) Read(p []byte) (int, error) {
	if b.chunked {
		return b.readChunked(p)
	}
	if b.remaining == 0 {
		return 0, io.EOF
	}
	if b.remaining > 0 && int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.reader.Read(p)
	if n > 0 && b.remaining > 0 {
		b.remaining -= int64(n)
	}
	if err == io.EOF && b.remaining > 0 {
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}

func (b *daemonBody) readChunked(p []byte) (int, error) {
	if b.chunkLeft == 0 {
		var header [4]byte
		if _, err := io.ReadFull(b.reader, header[:]); err != nil {
			if err == io.EOF {
				return 0, io.ErrUnexpectedEOF
			}
			return 0, err
		}
		size := binary.BigEndian.Uint32(header[:])
		switch size {
		case daemonChunkEnd:
			return 0, io.EOF
		case daemonChunkError:
			return 0, b.readChunkError()
		}
		if size > maxDaemonChunk {
			return 0, fmt.Errorf("daemon chunk too large: %d", size)
		}
		b.chunkLeft = size
	}
	if uint32(len(p)) > b.chunkLeft {
		p = p[:b.chunkLeft]
	}
	n, err := b.reader.Read(p)
	b.chunkLeft -= uint32(n)
	if err == io.EOF && b.chunkLeft > 0 {
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}

func (b *daemonBody) readChunkError() error {
	var header [5]byte
	if _, err := io.ReadFull(b.reader, header[:]); err != nil {
		return io.ErrUnexpectedEOF
	}
	kind := header[0]
	size := binary.BigEndian.Uint32(header[1:])
	if size > 4096 {
		return fmt.Errorf("daemon body error")
	}
	msg := make([]byte, size)
	if _, err := io.ReadFull(b.reader, msg); err != nil {
		return io.ErrUnexpectedEOF
	}
	switch kind {
	case chunkErrTruncated:
		return io.ErrUnexpectedEOF
	case chunkErrReset:
		return syscall.ECONNRESET
	case chunkErrTimeout:
		return context.DeadlineExceeded
	default:
		if len(msg) == 0 {
			return fmt.Errorf("daemon body error")
		}
		return errors.New(string(msg))
	}
}
func (b *daemonBody) Close() error { return b.conn.Close() }

func redirectResponseChain(urls []string) *http.Response {
	var prev *http.Response
	for _, rawURL := range urls {
		u, err := url.Parse(rawURL)
		if err != nil {
			continue
		}
		req := &http.Request{URL: u}
		req.Response = prev
		prev = &http.Response{Request: req}
	}
	return prev
}

func DumpCookies(socket string, hosts []string) (string, error) {
	conn, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(conn)
	bw := bufio.NewWriter(conn)
	if err := writeDaemonFrame(bw, daemonRequest{
		Version:     daemonProtocolVersion,
		Op:          daemonOpDumpCookies,
		CookieHosts: hosts,
	}); err != nil {
		return "", err
	}
	if err := bw.Flush(); err != nil {
		return "", err
	}
	var resp daemonResponse
	if err := readDaemonFrame(br, &resp); err != nil {
		return "", err
	}
	if resp.Error != "" {
		return "", &exitcode.Coded{Code: resp.ErrorCode, Msg: resp.Error}
	}
	data, err := io.ReadAll(br)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func headersForDaemon(req *http.Request) (map[string][]string, []string) {
	order := append([]string(nil), req.Header[http.HeaderOrderKey]...)
	headers := make(map[string][]string, len(req.Header))
	for name, values := range req.Header {
		if name == http.HeaderOrderKey || name == http.PHeaderOrderKey {
			continue
		}
		headers[name] = append([]string(nil), values...)
	}
	seen := make(map[string]bool, len(order))
	for _, name := range order {
		seen[strings.ToLower(name)] = true
	}
	var extra []string
	for name := range headers {
		if !seen[strings.ToLower(name)] {
			extra = append(extra, strings.ToLower(name))
		}
	}
	sort.Strings(extra)
	order = append(order, extra...)
	return headers, order
}

func daemonTransportFromOptions(o *config.Options, target *url.URL) daemonTransport {
	var t daemonTransport
	transportFromOptions(&t, o)
	proxyURL, _ := transport.ProxyURLFor(o, target)
	if proxyURL == "" {
		t.ProxyMode = "direct"
	} else {
		t.ProxyMode = "proxy"
		t.Proxy = proxyURL
	}
	t.CertFile = AbsPath(o.CertFile)
	t.KeyFile = AbsPath(o.KeyFile)
	t.CACert = AbsPath(o.CaCert)
	t.CAPath = AbsPath(o.CaPath)
	t.UnixSocket = unixSocketForTransport(o)
	t.ProxyHeaders = transport.ProxyHeaderMap(o)
	return t
}

func AbsPath(path string) string {
	if path == "" || path == "-" {
		return path
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

func unixSocketForTransport(o *config.Options) string {
	if o.UnixSocket == "" || o.AbstractUnix {
		return o.UnixSocket
	}
	return AbsPath(o.UnixSocket)
}
