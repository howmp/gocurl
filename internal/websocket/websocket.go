package websocket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"time"

	"gocurl/internal/config"
	"gocurl/internal/exitcode"
	"gocurl/internal/httpx"
	"gocurl/internal/output"
	"gocurl/internal/tlsname"
	"gocurl/internal/transport"

	http "github.com/bogdanfinn/fhttp"
	"github.com/bogdanfinn/tls-client/profiles"
	utls "github.com/bogdanfinn/utls"
	"github.com/bogdanfinn/websocket"
)

const defaultMaxMessageSize int64 = 64 << 20

func Run(o *config.Options, u *url.URL, idx int, stdout, stderr io.Writer) error {

	proxyRaw, err := transport.ProxyURLFor(o, u)
	if err != nil {
		return err
	}
	var proxyDial func(context.Context, string, string) (net.Conn, error)
	if proxyRaw != "" {
		proxyDial, err = transport.NewChainProxyContext(o, proxyRaw)
		if err != nil {
			return err
		}
	}

	if err := httpx.ValidateUserHeaders(o); err != nil {
		return err
	}
	if o.RequestTarget != "" {
		if err := httpx.ValidateRequestTarget(o.RequestTarget); err != nil {
			return err
		}
	}
	headers := websocketHeaders(o, u)
	baseDial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if proxyDial != nil {
			return proxyDial(ctx, network, addr)
		}
		return transport.DialTCP(ctx, network, addr, o)
	}
	dialer := websocket.Dialer{
		HandshakeTimeout:  45 * time.Second,
		Jar:               o.CookieJar,
		EnableCompression: o.Compressed,
		NetDialContext:    baseDial,
	}
	if u.Scheme == "wss" {
		dialer.NetDialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialWSTLSVia(ctx, network, addr, o, baseDial)
		}
	}

	ctx := context.Background()
	if o.MaxTime > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.MaxTime)
		defer cancel()
	}

	reqForLog, _ := http.NewRequest(http.MethodGet, u.String(), nil)
	if reqForLog != nil {
		reqForLog.Header = headers
		reqForLog.Host = u.Host
		if o.Verbose {
			output.PrintRequestVerbose(stderr, reqForLog, o)
		}
	}

	start := time.Now()
	conn, resp, err := dialer.DialContext(ctx, u.String(), headers)
	if resp != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		if resp != nil && resp.StatusCode >= 400 {
			return &httpx.HTTPStatusError{Code: resp.StatusCode}
		}
		return err
	}
	defer conn.Close()
	if resp == nil {
		return fmt.Errorf("websocket handshake returned no response")
	}

	if o.Verbose {
		output.PrintResponseVerbose(stderr, resp, o.ShowSecrets)
	}
	if err := output.WriteHeaderDump(o, resp, idx, stdout); err != nil {
		return err
	}

	out, closeOut, err := output.OutputWriter(o, u, idx, stdout)
	if err != nil {
		return err
	}
	defer closeOut()

	info := &output.ResponseInfo{
		Url:         u.String(),
		HttpVersion: "1.1",
		ContentType: resp.Header.Get("Content-Type"),
		RemoteAddr:  conn.RemoteAddr().String(),
	}
	info.StatusCode = resp.StatusCode
	if tlsConn, ok := conn.UnderlyingConn().(*utls.UConn); ok {
		state := tlsConn.ConnectionState()
		info.TlsVersion = tlsname.Version(state.Version)
		info.Alpn = state.NegotiatedProtocol
	}

	if o.Include || o.Head {
		if err := output.WriteResponseHeaders(out, resp); err != nil {
			return err
		}
	}
	if o.Head {
		info.TimeTotal = time.Since(start)
		if err := closeOut(); err != nil {
			return err
		}
		return output.WriteOutFormat(o, info, stdout)
	}

	body, _, err := httpx.BuildBody(o)
	if err != nil {
		return err
	}
	if len(body) > 0 {
		if o.MaxTime > 0 {
			remaining := o.MaxTime - time.Since(start)
			if remaining <= 0 {
				return &exitcode.Coded{Code: exitcode.Timeout, Msg: "operation timed out before writing WebSocket data"}
			}
			_ = conn.SetWriteDeadline(time.Now().Add(remaining))
		}
		writeErr := conn.WriteMessage(websocket.TextMessage, body)
		_ = conn.SetWriteDeadline(time.Time{})
		if writeErr != nil {
			if ne, ok := writeErr.(net.Error); ok && ne.Timeout() {
				return &exitcode.Coded{Code: exitcode.Timeout, Msg: "operation timed out writing WebSocket data"}
			}
			return writeErr
		}
	}

	if o.MaxTime > 0 {
		remaining := o.MaxTime - time.Since(start)
		if remaining <= 0 {
			_ = closeOut()
			return &exitcode.Coded{Code: exitcode.Timeout, Msg: "operation timed out before reading WebSocket data"}
		}
		_ = conn.SetReadDeadline(time.Now().Add(remaining))
	}
	if len(body) == 0 && o.MaxTime == 0 {
		info.TimeTotal = time.Since(start)
		if err := closeOut(); err != nil {
			return err
		}
		return output.WriteOutFormat(o, info, stdout)
	}
	messages := 0
	total := int64(0)
	messageLimit := messageSizeLimit(o)
	var terminalErr error
	for {
		messageType, data, readErr := readWSMessage(conn, messageLimit)
		if readErr != nil {
			if ne, ok := readErr.(net.Error); ok && ne.Timeout() {
				terminalErr = &exitcode.Coded{Code: exitcode.Timeout, Msg: fmt.Sprintf("operation timed out after %d messages", messages)}
				break
			}
			var tooLarge *messageTooLargeError
			if errors.As(readErr, &tooLarge) {
				_ = conn.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseMessageTooBig, ""),
					time.Now().Add(time.Second))
				terminalErr = readErr
				break
			}
			if websocket.IsCloseError(readErr, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				break
			}
			terminalErr = &exitcode.Coded{Code: exitcode.RecvFailure, Msg: "WebSocket connection closed unexpectedly"}
			break
		}
		messageSize := int64(len(data))
		if messageType == websocket.TextMessage {
			messageSize++
		}
		if o.MaxFileSize > 0 && total+messageSize > o.MaxFileSize {
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseMessageTooBig, ""),
				time.Now().Add(time.Second))
			terminalErr = &output.MaxFilesizeError{}
			break
		}
		written, writeErr := writeWSMessage(out, data, messageType == websocket.TextMessage)
		total += int64(written)
		info.SizeDownload += int64(written)
		if writeErr != nil {
			terminalErr = writeErr
			break
		}
		messages++
		if o.MaxTime == 0 && len(body) > 0 && messages >= 1 {
			break
		}
	}
	info.TimeTotal = time.Since(start)
	if err := closeOut(); err != nil {
		return err
	}
	if err := output.WriteOutFormat(o, info, stdout); err != nil {
		return err
	}
	if terminalErr != nil {
		return terminalErr
	}
	return nil
}

type messageTooLargeError struct {
	limit int64
}

func (e *messageTooLargeError) Error() string {
	return fmt.Sprintf("WebSocket message exceeds maximum size of %d bytes", e.limit)
}

func (e *messageTooLargeError) ExitCode() int { return 63 }

func messageSizeLimit(o *config.Options) int64 {
	if o.MaxFileSize > 0 {
		return o.MaxFileSize
	}
	return defaultMaxMessageSize
}

func readWSMessage(conn *websocket.Conn, limit int64) (int, []byte, error) {
	messageType, r, err := conn.NextReader()
	if err != nil {
		return 0, nil, err
	}
	readLimit := limit
	if readLimit < math.MaxInt64 {
		readLimit++
	}
	data, err := io.ReadAll(io.LimitReader(r, readLimit))
	if err != nil {
		return messageType, nil, err
	}
	if int64(len(data)) > limit {
		return messageType, nil, &messageTooLargeError{limit: limit}
	}
	return messageType, data, nil
}

func writeWSMessage(out io.Writer, data []byte, text bool) (int, error) {
	n, err := out.Write(data)
	if err != nil {
		return n, &exitcode.WriteError{Err: err}
	}
	if text {
		written, err := out.Write([]byte("\n"))
		n += written
		if err != nil {
			return n, &exitcode.WriteError{Err: err}
		}
	}
	return n, nil
}

func dialWSTLSVia(ctx context.Context, network, addr string, o *config.Options, base func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	raw, err := base(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	cfg := &utls.Config{
		ServerName:         host,
		InsecureSkipVerify: o.Insecure,
		NextProtos:         []string{"http/1.1"},
		MinVersion:         utls.VersionTLS12,
		MaxVersion:         utls.VersionTLS13,
	}
	transportOptions, err := transport.LoadTransportOptions(o)
	if err != nil {
		raw.Close()
		return nil, err
	}
	if transportOptions != nil {
		cfg.Certificates = transportOptions.Certificates
		cfg.RootCAs = transportOptions.RootCAs
	}
	conn := utls.UClient(raw, cfg, profiles.Chrome_152.GetClientHelloId(), false, true, true)
	if net.ParseIP(host) != nil {
		_ = conn.RemoveSNIExtension()
	}
	if err := conn.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	return conn, nil
}

func websocketHeaders(o *config.Options, u *url.URL) http.Header {
	h := make(http.Header)
	for _, name := range []string{"user-agent", "accept-language", "accept-encoding"} {
		if value, ok := httpx.DefaultHeaderValue(name); ok {
			h.Set(name, value)
		}
	}
	h.Set("Accept", "*/*")
	originScheme := "http"
	if u.Scheme == "wss" {
		originScheme = "https"
	}
	h.Set("Origin", originScheme+"://"+u.Host)
	if o.UserAgent != "" {
		h.Set("User-Agent", o.UserAgent)
	}
	if o.Referer != "" {
		h.Set("Referer", o.Referer)
	}
	if o.CookieHeader != "" {
		h.Set("Cookie", o.CookieHeader)
	}

	order := []string{"host", "connection", "upgrade", "sec-websocket-key", "sec-websocket-version",
		"user-agent", "accept", "accept-encoding", "accept-language", "origin", "sec-websocket-extensions", "cookie"}
	orderSet := make(map[string]bool, len(order))
	for _, name := range order {
		orderSet[name] = true
	}
	userSet := make(map[string]bool)
	for _, hdr := range o.Headers {
		lower, wasNew := httpx.MergeUserHeader(h, userSet, hdr)
		if wasNew && !orderSet[lower] {
			order = append(order, lower)
			orderSet[lower] = true
		}
	}
	h[http.HeaderOrderKey] = order
	h[http.PHeaderOrderKey] = httpx.ChromePseudoHeaderOrder()
	return h
}
