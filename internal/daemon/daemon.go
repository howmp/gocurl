package daemon

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"gocurl/internal/config"
	"gocurl/internal/exitcode"
	"gocurl/internal/httpx"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	http "github.com/bogdanfinn/fhttp"
	"github.com/bogdanfinn/fhttp/httptrace"
	tls_client "github.com/bogdanfinn/tls-client"
	utls "github.com/bogdanfinn/utls"
	"golang.org/x/net/http/httpguts"
)

type cachedDaemonClient struct {
	client   tls_client.HttpClient
	lastUsed time.Time
}

type daemonState struct {
	mu           sync.Mutex
	clients      map[string]*cachedDaemonClient
	tlsStates    *config.TLSStateStore
	jar          config.CookieJar
	cookieMu     sync.Mutex
	active       int
	lastActivity time.Time
	idle         time.Duration
	idleTimer    *time.Timer
	stopFunc     func()
	logger       *log.Logger
	wg           sync.WaitGroup
}

func Run(args []string) int {
	fs := flag.NewFlagSet("gocurl-daemon", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", SocketPath(), "unix socket path")
	idleText := fs.String("idle-timeout", daemonIdleTimeout().String(), "idle timeout")
	logPath := fs.String("log", daemonLogPath(), "log path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	idle, err := time.ParseDuration(*idleText)
	if err != nil || idle <= 0 {
		idle = defaultDaemonIdle
	}

	if err := ensurePrivateDir(filepath.Dir(*logPath)); err != nil {
		fmt.Fprintf(os.Stderr, "gocurl daemon: log directory: %v\n", err)
		return 1
	}
	logFile, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gocurl daemon: log file: %v\n", err)
		return 1
	}
	logger := log.New(logFile, "", log.LstdFlags)
	defer logFile.Close()

	if err := serveDaemon(*socket, idle, logger); err != nil {
		logger.Printf("daemon error: %v", err)
		return 1
	}
	return 0
}

func serveDaemon(socket string, idle time.Duration, logger *log.Logger) error {
	if err := ensurePrivateDir(filepath.Dir(socket)); err != nil {
		return err
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			if _, pingErr := pingDaemon(socket, 500*time.Millisecond); pingErr == nil {
				logger.Printf("daemon already running on %s", socket)
				return nil
			}
			for i := 0; i < 5; i++ {
				time.Sleep(100 * time.Millisecond)
				if _, pingErr := pingDaemon(socket, 500*time.Millisecond); pingErr == nil {
					logger.Printf("daemon already running on %s", socket)
					return nil
				}
			}
			if err := canRemoveStaleSocket(socket); err != nil {
				return err
			}
			_ = os.Remove(socket)
			ln, err = net.Listen("unix", socket)
		}
		if err != nil {
			return err
		}
	}
	socketInfo, statErr := os.Lstat(socket)
	if statErr != nil {
		return statErr
	}
	defer func() {
		if current, err := os.Lstat(socket); err == nil && os.SameFile(current, socketInfo) {
			_ = os.Remove(socket)
		}
	}()
	if err := os.Chmod(socket, 0o600); err != nil {
		return err
	}

	state := &daemonState{
		clients:      make(map[string]*cachedDaemonClient),
		tlsStates:    config.NewTLSStateStore(),
		jar:          httpx.NewCookieJar(),
		lastActivity: time.Now(),
		idle:         idle,
		logger:       logger,
	}
	state.stopFunc = func() { _ = ln.Close() }
	state.mu.Lock()
	state.idleTimer = time.AfterFunc(idle, state.checkIdle)
	state.mu.Unlock()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		<-sigCh
		state.stop()
	}()

	logger.Printf("daemon started pid=%d socket=%s idle=%s", os.Getpid(), socket, idle)
	connSem := make(chan struct{}, MaxConnections)
	for {
		conn, err := ln.Accept()
		if err != nil {
			break
		}
		if err := peerAllowedPlatform(conn); err != nil {
			logger.Printf("rejected daemon peer: %v", err)
			_ = conn.Close()
			continue
		}
		select {
		case connSem <- struct{}{}:
		default:
			logger.Printf("rejecting daemon connection: too many active connections")
			bw := bufio.NewWriter(conn)
			_ = writeDaemonReply(bw, daemonResponse{
				Version:   daemonProtocolVersion,
				Error:     "daemon busy",
				ErrorCode: 4,
			})
			_ = conn.Close()
			continue
		}
		state.wg.Add(1)
		go func() {
			defer state.wg.Done()
			defer func() { <-connSem }()
			defer func() {
				if recovered := recover(); recovered != nil {
					logger.Printf("daemon connection panic: %v", recovered)
				}
			}()
			state.handleConn(conn)
		}()
	}
	state.stopIdleTimer()
	state.wg.Wait()
	state.closeClients()
	logger.Printf("daemon stopped pid=%d", os.Getpid())
	return nil
}

func (s *daemonState) stop() {
	if s.stopFunc != nil {
		s.stopFunc()
	}
}

func (s *daemonState) stopIdleTimer() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.idleTimer != nil {
		s.idleTimer.Stop()
	}
}

func (s *daemonState) checkIdle() {
	s.mu.Lock()
	if s.active > 0 {
		s.mu.Unlock()
		return
	}
	remaining := s.idle - time.Since(s.lastActivity)
	if remaining > 0 {
		s.mu.Unlock()
		s.idleTimer.Reset(remaining)
		return
	}
	s.mu.Unlock()
	s.stop()
}

func (s *daemonState) begin() {
	s.mu.Lock()
	s.active++
	s.lastActivity = time.Now()
	if s.idleTimer != nil {
		s.idleTimer.Reset(s.idle)
	}
	s.mu.Unlock()
}

func (s *daemonState) end() {
	s.mu.Lock()
	if s.active > 0 {
		s.active--
	}
	s.lastActivity = time.Now()
	if s.idleTimer != nil {
		s.idleTimer.Reset(s.idle)
	}
	s.mu.Unlock()
}

func (s *daemonState) closeClients() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.clients {
		c.client.CloseIdleConnections()
	}
	s.clients = make(map[string]*cachedDaemonClient)
}

func (s *daemonState) handleConn(conn net.Conn) {
	defer conn.Close()
	s.begin()
	defer s.end()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Minute))

	br := bufio.NewReader(conn)
	bw := bufio.NewWriter(conn)
	var req daemonRequest
	if err := readDaemonFrame(br, &req); err != nil {
		return
	}
	if req.Version != daemonProtocolVersion {
		resp := daemonResponse{
			Version:   daemonProtocolVersion,
			Error:     fmt.Sprintf("daemon protocol version mismatch: client=%d daemon=%d", req.Version, daemonProtocolVersion),
			ErrorCode: 2,
		}
		_ = writeDaemonReply(bw, resp)
		return
	}

	switch req.Op {
	case daemonOpPing:
		resp := daemonResponse{Version: daemonProtocolVersion, BodyLength: 0}
		_ = writeDaemonReply(bw, resp)
	case daemonOpShutdown:
		resp := daemonResponse{Version: daemonProtocolVersion, BodyLength: 0}
		_ = writeDaemonReply(bw, resp)
		go s.stop()
	case daemonOpDumpCookies:
		text := httpx.CookieFileTextForHosts(s.jar, req.CookieHosts)
		resp := daemonResponse{Version: daemonProtocolVersion, BodyLength: int64(len(text))}
		if err := writeDaemonReply(bw, resp); err != nil {
			return
		}
		_, _ = io.WriteString(conn, text)
	case daemonOpRequest:
		s.handleRequest(conn, br, bw, &req)
	default:
		resp := daemonResponse{Version: daemonProtocolVersion, Error: "unknown daemon operation", ErrorCode: 2}
		_ = writeDaemonReply(bw, resp)
	}
}

const (
	MaxConnections       = 64
	maxDaemonRequestBody = 32 << 20
)

var daemonBodySem = make(chan struct{}, 4)

const daemonBodyGateBytes = 1 << 20

func readRequestBody(br *bufio.Reader, length int64) ([]byte, error) {
	if length < 0 || length > maxDaemonRequestBody {
		return nil, fmt.Errorf("invalid request body length")
	}
	if length == 0 {
		return nil, nil
	}
	var body bytes.Buffer
	if _, err := io.CopyN(&body, br, length); err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("short request body: got %d of %d bytes", body.Len(), length)
		}
		return nil, err
	}
	return body.Bytes(), nil
}

func responseRedirectURLs(resp *http.Response) []string {
	var reverse []string
	for req := resp.Request; req != nil && req.Response != nil; req = req.Response.Request {
		if req.Response.Request != nil && req.Response.Request.URL != nil {
			reverse = append(reverse, req.Response.Request.URL.String())
		}
	}
	urls := make([]string, 0, len(reverse))
	for i := len(reverse) - 1; i >= 0; i-- {
		urls = append(urls, reverse[i])
	}
	return urls
}

func (s *daemonState) handleRequest(conn net.Conn, br *bufio.Reader, bw *bufio.Writer, req *daemonRequest) {
	if err := validateDaemonRequest(req); err != nil {
		s.writeDaemonError(bw, err, 3)
		return
	}
	if req.BodyLength > daemonBodyGateBytes {
		daemonBodySem <- struct{}{}
		defer func() { <-daemonBodySem }()
	}
	body, err := readRequestBody(br, req.BodyLength)
	if err != nil {
		s.writeDaemonError(bw, err, 2)
		return
	}

	o := optionsFromDaemonTransport(req.Transport)
	if o.MaxTime > 0 {
		_ = conn.SetDeadline(time.Now().Add(o.MaxTime + 5*time.Second))
	} else {
		_ = conn.SetDeadline(time.Time{})
	}
	o.CookieJar = s.jar
	u, err := url.Parse(req.URL)
	if err != nil {
		s.writeDaemonError(bw, err, 3)
		return
	}
	ctx := context.Background()
	if o.MaxTime > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.MaxTime)
		defer cancel()
	}

	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}
	r, err := http.NewRequestWithContext(ctx, req.Method, req.URL, bodyReader)
	if err != nil {
		s.writeDaemonError(bw, err, 3)
		return
	}
	if len(body) > 0 {
		r.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
	}
	r.Header = make(http.Header, len(req.Headers)+2)
	for name, values := range req.Headers {
		for _, value := range values {
			r.Header.Add(name, value)
		}
	}
	r.Header[http.HeaderOrderKey] = req.HeaderOrder
	if len(req.HeaderOrder) == 0 {
		r.Header[http.HeaderOrderKey] = append([]string(nil), httpx.DefaultHeaderOrder()...)
	}
	r.Header[http.PHeaderOrderKey] = httpx.ChromePseudoHeaderOrder()
	if req.CookieHeader != "" && r.Header.Get("Cookie") == "" {
		r.Header.Set("Cookie", req.CookieHeader)
	}
	if req.Host != "" {
		r.Host = req.Host
	}
	if req.RequestTarget != "" {
		r.URL.Opaque = req.RequestTarget
	}

	var remoteAddr string
	var tlsState *utls.ConnectionState
	trace := &httptrace.ClientTrace{
		GotConn: func(got httptrace.GotConnInfo) {
			remoteAddr = got.Conn.RemoteAddr().String()
		},
		TLSHandshakeDone: func(state utls.ConnectionState, err error) {
			if err == nil {
				s := state
				tlsState = &s
			}
		},
	}
	r = r.WithContext(httptrace.WithClientTrace(r.Context(), trace))

	client, err := s.getClient(o, u)
	if err != nil {
		s.writeDaemonError(bw, err, exitcode.From(err))
		return
	}
	resp, err := client.Do(r)
	if err != nil {
		s.writeDaemonError(bw, err, exitcode.From(err))
		return
	}
	defer resp.Body.Close()

	if resp.TLS != nil {
		tlsState = resp.TLS
	}
	meta := daemonResponse{
		Version:      daemonProtocolVersion,
		Status:       resp.Status,
		StatusCode:   resp.StatusCode,
		Proto:        resp.Proto,
		Headers:      cloneHeader(resp.Header),
		EffectiveURL: resp.Request.URL.String(),
		BodyLength:   -1,
		Redirects:    responseRedirectURLs(resp),
		RemoteAddr:   remoteAddr,
	}
	if state, ok := s.tlsStates.Get(resp.Request.URL.Hostname()); ok {
		meta.TLSVersion = state.Version
		meta.CipherSuite = state.CipherSuite
		meta.ALPN = state.ALPN
		meta.TLSVerified = state.Verified
		for _, cert := range state.PeerCertificates {
			if cert != nil {
				meta.TLSPeerCerts = append(meta.TLSPeerCerts, cert.Raw)
			}
		}
	} else if tlsState != nil {
		meta.TLSVersion = tlsState.Version
		meta.CipherSuite = tlsState.CipherSuite
		meta.ALPN = tlsState.NegotiatedProtocol
		meta.TLSVerified = len(tlsState.VerifiedChains) > 0
	}
	if req.Method == http.MethodHead {
		meta.BodyLength = 0
	} else if resp.ContentLength >= 0 {
		meta.BodyLength = resp.ContentLength
	} else {
		meta.BodyChunked = true
	}
	if err := writeDaemonFrame(bw, meta); err != nil {
		return
	}
	if err := bw.Flush(); err != nil {
		return
	}
	if err := writeDaemonResponseBody(conn, resp.Body, meta.BodyLength, meta.BodyChunked); err != nil {
		s.logger.Printf("daemon response body copy failed: %v", err)
	}
}

func (s *daemonState) writeDaemonError(bw *bufio.Writer, err error, code int) {
	resp := daemonResponse{
		Version:    daemonProtocolVersion,
		Error:      err.Error(),
		ErrorCode:  code,
		BodyLength: 0,
	}
	_ = writeDaemonReply(bw, resp)
}

func writeDaemonReply(bw *bufio.Writer, resp daemonResponse) error {
	if err := validateDaemonResponse(&resp); err != nil {
		return err
	}
	if err := writeDaemonFrame(bw, resp); err != nil {
		return err
	}
	return bw.Flush()
}

func optionsFromDaemonTransport(t daemonTransport) *config.Options {
	o := optionsFromTransport(t)
	o.Persist = false
	if t.ProxyMode == "direct" {
		o.ProxyDisabled = true
	}
	return o
}

func (s *daemonState) getClient(o *config.Options, target *url.URL) (tls_client.HttpClient, error) {
	key := clientProfileKey(o, target)

	s.mu.Lock()
	if cached, ok := s.clients[key]; ok {
		cached.lastUsed = time.Now()
		s.mu.Unlock()
		return cached.client, nil
	}
	s.mu.Unlock()

	o.TLSState = s.tlsStates
	client, err := httpx.NewHTTPClient(o, target)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if cached, ok := s.clients[key]; ok {
		client.CloseIdleConnections()
		cached.lastUsed = time.Now()
		return cached.client, nil
	}
	s.clients[key] = &cachedDaemonClient{client: client, lastUsed: time.Now()}
	if len(s.clients) > 64 {
		var oldestKey string
		var oldest time.Time
		for k, cached := range s.clients {
			if oldestKey == "" || cached.lastUsed.Before(oldest) {
				oldestKey, oldest = k, cached.lastUsed
			}
		}
		if oldestKey != "" && oldestKey != key {
			s.clients[oldestKey].client.CloseIdleConnections()
			delete(s.clients, oldestKey)
		}
	}
	return client, nil
}

func clientProfileKey(o *config.Options, target *url.URL) string {
	transport := daemonTransportFromOptions(o, target)
	payload := struct {
		Transport daemonTransport `json:"transport"`
		CertStamp string          `json:"cert_stamp"`
		KeyStamp  string          `json:"key_stamp"`
		CAStamp   string          `json:"ca_stamp"`
	}{
		Transport: transport,
		CertStamp: fileStamp(o.CertFile),
		KeyStamp:  fileStamp(o.KeyFile),
		CAStamp:   fileStamp(o.CaCert),
	}
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func fileStamp(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "missing:" + abs
	}
	return fmt.Sprintf("%s:%d:%d", abs, info.Size(), info.ModTime().UnixNano())
}

func cloneHeader(h http.Header) map[string][]string {
	out := make(map[string][]string, len(h))
	for k, values := range h {
		if k == http.HeaderOrderKey || k == http.PHeaderOrderKey {
			continue
		}
		out[k] = append([]string(nil), values...)
	}
	return out
}

func validateDaemonRequest(req *daemonRequest) error {
	if req == nil {
		return fmt.Errorf("invalid daemon request")
	}
	if req.Method == "" || strings.ContainsAny(req.Method, " \t\r\n") {
		return fmt.Errorf("invalid daemon request method")
	}
	u, err := url.Parse(req.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("invalid daemon request URL")
	}
	if !httpguts.ValidHostHeader(u.Host) {
		return fmt.Errorf("invalid daemon request host")
	}
	if req.Host != "" && !httpguts.ValidHostHeader(req.Host) {
		return fmt.Errorf("invalid daemon request host header")
	}
	if err := httpx.ValidateRequestTarget(req.RequestTarget); err != nil {
		return err
	}
	for name, values := range req.Headers {
		if !httpguts.ValidHeaderFieldName(name) {
			return fmt.Errorf("invalid daemon header name %q", name)
		}
		for _, value := range values {
			if !httpguts.ValidHeaderFieldValue(value) {
				return fmt.Errorf("invalid daemon header value for %q", name)
			}
		}
	}
	return nil
}
