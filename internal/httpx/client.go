package httpx

import (
	"context"
	"fmt"
	"gocurl/internal/config"
	"gocurl/internal/output"
	"gocurl/internal/tlsname"
	"gocurl/internal/transport"
	"io"
	"net"
	"net/url"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"
	"github.com/bogdanfinn/fhttp/httptrace"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	utls "github.com/bogdanfinn/utls"
	"golang.org/x/net/idna"
)

type URLError struct{ Msg string }

func (e *URLError) Error() string { return e.Msg }
func (e *URLError) ExitCode() int { return 3 }

type ProtocolError struct{ Scheme string }

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("Protocol %q not supported", e.Scheme)
}
func (e *ProtocolError) ExitCode() int { return 1 }

type HTTPStatusError struct{ Code int }

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("The requested URL returned error: %d", e.Code)
}
func (e *HTTPStatusError) ExitCode() int { return 22 }

type TooManyRedirectsError struct{ Max int }

func (e *TooManyRedirectsError) Error() string {
	return fmt.Sprintf("Maximum (%d) redirects followed", e.Max)
}
func (e *TooManyRedirectsError) ExitCode() int { return 47 }

func NormalizeURL(raw string) (*url.URL, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, &URLError{Msg: "URL rejected: empty URL"}
	}
	if !strings.Contains(s, "://") {
		if idx := strings.IndexByte(s, ':'); idx > 0 &&
			isAlpha(s[:idx]) && (idx+1 == len(s) || !isDigit(s[idx+1])) {
			return nil, &ProtocolError{Scheme: s[:idx]}
		}
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "ws", "wss", "ftp", "ftps":
		u.Scheme = strings.ToLower(u.Scheme)
	default:
		return nil, &ProtocolError{Scheme: u.Scheme}
	}
	if u.Host == "" {
		return nil, &URLError{Msg: "URL rejected: no host part in the URL"}
	}
	host := u.Hostname()
	if host != "" {
		if ascii, err := idna.Lookup.ToASCII(host); err == nil && ascii != host {
			if port := u.Port(); port != "" {
				u.Host = net.JoinHostPort(ascii, port)
			} else {
				u.Host = ascii
			}
		}
	}
	return u, nil
}

func isAlpha(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func RequestMethod(o *config.Options, hasBody bool) string {
	if o.Method != "" {
		return o.Method
	}
	if o.Head {
		return http.MethodHead
	}
	if o.Get {
		return http.MethodGet
	}
	if o.UploadFile != "" {
		return http.MethodPut
	}
	if hasBody {
		return http.MethodPost
	}
	return http.MethodGet
}

func BuildBody(o *config.Options) ([]byte, string, error) {
	if len(o.Forms) > 0 {
		if o.Get {
			return nil, "", fmt.Errorf("option --form cannot be combined with --get")
		}
		if len(o.Data) > 0 || o.JsonSet || o.UploadFile != "" {
			return nil, "", fmt.Errorf("option --form cannot be combined with --data, --json or --upload-file")
		}
		return config.BuildMultipartBody(o.Forms)
	}
	if o.UploadFile != "" {
		path := o.UploadFile
		if path != "-" {
			path = "@" + path
		} else {
			path = "@-"
		}
		v, err := dataValue(path, false)
		if err != nil {
			return nil, "", err
		}
		return []byte(v), "application/octet-stream", nil
	}
	if o.JsonSet {
		v, err := dataValue(o.JsonData, false)
		if err != nil {
			return nil, "", err
		}
		return []byte(v), "application/json", nil
	}
	if len(o.Data) == 0 {
		return nil, "", nil
	}
	parts := make([]string, 0, len(o.Data))
	for _, p := range o.Data {
		var (
			v   string
			err error
		)
		switch p.Kind {
		case config.DataForm:
			v, err = dataValue(p.Value, true)
		case config.DataRaw:
			v = p.Value
		case config.DataBinary:
			v, err = dataValue(p.Value, false)
		case config.DataURLEncode:
			v, err = urlencodeData(p.Value)
		}
		if err != nil {
			return nil, "", err
		}
		parts = append(parts, v)
	}
	return []byte(strings.Join(parts, "&")), "application/x-www-form-urlencoded", nil
}

func dataValue(v string, stripNewlines bool) (string, error) {
	if !strings.HasPrefix(v, "@") {
		return v, nil
	}
	name := v[1:]
	data, err := config.ReadLocalFile(name)
	if err != nil {
		return "", err
	}
	s := string(data)
	if stripNewlines {
		s = strings.NewReplacer("\r", "", "\n", "").Replace(s)
	}
	return s, nil
}

func urlencodeData(v string) (string, error) {
	if strings.HasPrefix(v, "@") {
		data, err := dataValue(v, false)
		if err != nil {
			return "", err
		}
		return url.QueryEscape(data), nil
	}
	if strings.HasPrefix(v, "=") {
		return "=" + url.QueryEscape(v[1:]), nil
	}
	if idx := strings.IndexByte(v, '='); idx >= 0 {
		return v[:idx] + "=" + url.QueryEscape(v[idx+1:]), nil
	}
	if idx := strings.IndexByte(v, '@'); idx > 0 {
		data, err := dataValue(v[idx:], false)
		if err != nil {
			return "", err
		}
		return v[:idx] + "=" + url.QueryEscape(data), nil
	}
	return url.QueryEscape(v), nil
}

type defaultHeader struct {
	name  string
	value string
}

// Values and order follow Chrome 152 on Windows.
const ChromeUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36"

var defaultHeaders = []defaultHeader{
	{"sec-ch-ua", `"Chromium";v="152", "Not?A_Brand";v="24", "Google Chrome";v="152"`},
	{"sec-ch-ua-mobile", "?0"},
	{"sec-ch-ua-platform", `"Windows"`},
	{"upgrade-insecure-requests", "1"},
	{"user-agent", ChromeUserAgent},
	{"accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"},
	{"sec-fetch-site", "none"},
	{"sec-fetch-mode", "navigate"},
	{"sec-fetch-user", "?1"},
	{"sec-fetch-dest", "document"},
	{"accept-encoding", "gzip, deflate, br, zstd"},
	{"accept-language", "en-US,en;q=0.9"},
	{"priority", "u=0, i"},
	{"cache-control", "max-age=0"},
}

func DefaultHeaderValue(name string) (string, bool) {
	name = strings.ToLower(name)
	for _, header := range defaultHeaders {
		if header.name == name {
			return header.value, true
		}
	}
	return "", false
}

var defaultHeaderOrder = []string{
	"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform",
	"upgrade-insecure-requests", "user-agent", "accept",
	"sec-fetch-site", "sec-fetch-mode", "sec-fetch-user",
	"sec-fetch-dest", "accept-encoding", "accept-language",
	"priority", "cache-control", "cookie",
}

var chromePseudoHeaderOrder = []string{":method", ":authority", ":scheme", ":path"}

func headerOrderAdd(order []string, name string) []string {
	name = strings.ToLower(name)
	for _, n := range order {
		if n == name {
			return order
		}
	}
	return append(order, name)
}

func HeaderOrderInsertBefore(order []string, name, before string) []string {
	name = strings.ToLower(name)
	before = strings.ToLower(before)
	for _, n := range order {
		if n == name {
			return order
		}
	}
	for i, n := range order {
		if n == before {
			out := make([]string, 0, len(order)+1)
			out = append(out, order[:i]...)
			out = append(out, name)
			out = append(out, order[i:]...)
			return out
		}
	}
	return append(order, name)
}

func headerOrderRemove(order []string, name string) []string {
	name = strings.ToLower(name)
	out := order[:0]
	for _, n := range order {
		if n != name {
			out = append(out, n)
		}
	}
	return out
}

func headerOrderInsertFor(order []string, name string) []string {
	switch name {
	case "referer", "authorization", "content-type", "content-length", "range":
		return HeaderOrderInsertBefore(order, name, "accept-encoding")
	default:
		return headerOrderAdd(order, name)
	}
}

func ApplyHeaders(req *http.Request, o *config.Options, contentType string, body []byte) {
	ApplyHeadersWithLength(req, o, contentType, body, int64(len(body)))
}

func ApplyHeadersWithLength(req *http.Request, o *config.Options, contentType string, body []byte, bodyLen int64) {
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	order := append([]string(nil), defaultHeaderOrder...)

	for _, h := range defaultHeaders {
		req.Header.Set(h.name, h.value)
	}
	if o.JsonSet {
		req.Header.Set("Accept", "application/json")
	}
	if o.UserAgent != "" {
		req.Header.Set("User-Agent", o.UserAgent)
	}
	if o.Referer != "" {
		req.Header.Set("Referer", o.Referer)
		order = HeaderOrderInsertBefore(order, "referer", "accept-encoding")
	}
	if user, pass, ok := CredentialsForURL(o, req.URL); ok && (o.AuthMode == "" || o.AuthMode == "basic") {
		order = applyBasicAuth(req, order, user, pass)
	}
	if contentType != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", contentType)
		order = HeaderOrderInsertBefore(order, "content-type", "accept-encoding")
	}
	if bodyLen > 0 {
		req.ContentLength = bodyLen
		order = HeaderOrderInsertBefore(order, "content-length", "accept-encoding")
	}
	if o.RangeSpec != "" {
		req.Header.Set("Range", "bytes="+o.RangeSpec)
		order = HeaderOrderInsertBefore(order, "range", "accept-encoding")
	}
	if o.TrEncoding {
		req.Header.Set("TE", "gzip")
		order = HeaderOrderInsertBefore(order, "te", "accept-encoding")
	}

	userSet := make(map[string]bool)
	for _, h := range o.Headers {
		if strings.EqualFold(h.Name, "Host") {
			if h.Remove {
				req.Host = ""
			} else {
				req.Host = h.Value
			}
			continue
		}
		lower, wasNew := MergeUserHeader(req.Header, userSet, h)
		switch {
		case h.Remove:
			order = headerOrderRemove(order, lower)
		case h.Empty, wasNew:
			order = headerOrderInsertFor(order, lower)
		}
	}

	if len(o.HeaderOrder) > 0 {
		order = applyHeaderOrder(o.HeaderOrder, order, req.Header)
	}

	req.Header[http.HeaderOrderKey] = order
	req.Header[http.PHeaderOrderKey] = chromePseudoHeaderOrder
}

func applyHeaderOrder(preferred, existing []string, headers http.Header) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(preferred)+len(headers))
	for _, name := range preferred {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || name == "host" || strings.HasPrefix(name, ":") || seen[name] {
			continue
		}
		out = append(out, name)
		seen[name] = true
	}
	for _, name := range existing {
		name = strings.ToLower(name)
		if seen[name] || name == http.HeaderOrderKey || name == http.PHeaderOrderKey {
			continue
		}
		out = append(out, name)
		seen[name] = true
	}
	for name := range headers {
		lower := strings.ToLower(name)
		if seen[lower] || name == http.HeaderOrderKey || name == http.PHeaderOrderKey {
			continue
		}
		out = append(out, lower)
		seen[lower] = true
	}
	return out
}

func NewHTTPClient(o *config.Options, target *url.URL) (tls_client.HttpClient, error) {
	jar := o.CookieJar
	if jar == nil {
		jar = NewCookieJar()
	}

	clientTimeout := 24 * time.Hour
	profile := profiles.Chrome_152
	if o.HttpVersion == "2-prior" {
		profile = profileWithAllowHTTP(profile)
	}
	clientOpts := []tls_client.HttpClientOption{
		tls_client.WithClientProfile(profile),
		tls_client.WithTimeoutMilliseconds(int(clientTimeout.Milliseconds())),
		tls_client.WithCookieJar(jar),
	}
	if o.TLSState != nil {
		clientOpts = append(clientOpts, tls_client.WithCertificatePinner(&tlsStatePinner{store: o.TLSState}))
	}
	if o.Insecure {
		clientOpts = append(clientOpts, tls_client.WithInsecureSkipVerify())
	}
	customDial := len(o.Resolves) > 0 || len(o.ConnectTo) > 0 || transport.HasCustomDNS(o)
	if o.ConnectTimeout > 0 && !customDial && o.UnixSocket == "" {
		clientOpts = append(clientOpts, tls_client.WithDialer(net.Dialer{Timeout: o.ConnectTimeout}))
	}
	if o.Ipv4 {
		clientOpts = append(clientOpts, tls_client.WithDisableIPV6())
	} else if o.Ipv6 {
		clientOpts = append(clientOpts, tls_client.WithDisableIPV4())
	}
	if !o.Location {
		clientOpts = append(clientOpts, tls_client.WithNotFollowRedirects())
	} else {
		clientOpts = append(clientOpts, tls_client.WithCustomRedirectFunc(redirectFunc(o)))
	}
	proxy, err := transport.ProxyURLFor(o, target)
	if err != nil {
		return nil, err
	}
	if o.UnixSocket != "" {
		if proxy != "" {
			return nil, fmt.Errorf("option --unix-socket cannot be combined with a proxy")
		}
		clientOpts = append(clientOpts, tls_client.WithDialContext(transport.DialUnixContext(o.UnixSocket, o.AbstractUnix)))
	} else if customDial && proxy == "" {
		clientOpts = append(clientOpts, tls_client.WithDialContext(
			func(ctx context.Context, network, addr string) (net.Conn, error) {
				return transport.DialHTTPContext(o, ctx, network, addr)
			}))
	} else if o.InterfaceName != "" && proxy == "" {
		local, err := transport.LocalTCPAddr(o.InterfaceName)
		if err != nil {
			return nil, err
		}
		if local != nil {
			clientOpts = append(clientOpts, tls_client.WithLocalAddr(*local))
		}
	}
	if proxy == "" && o.PreProxy != "" {
		return nil, fmt.Errorf("option --preproxy requires a proxy")
	}
	if proxy != "" {
		dial, err := transport.NewChainProxyContext(o, proxy)
		if err != nil {
			return nil, err
		}
		clientOpts = append(clientOpts, tls_client.WithDialContext(dial))
	}
	transportOptions, err := transport.LoadTransportOptions(o)
	if err != nil {
		return nil, err
	}
	if transportOptions == nil {
		transportOptions = &tls_client.TransportOptions{}
	}
	idleTimeout := 10 * time.Minute
	transportOptions.IdleConnTimeout = &idleTimeout
	transportOptions.MaxIdleConns = 100
	transportOptions.MaxIdleConnsPerHost = 10
	transportOptions.DisableCompression = true
	clientOpts = append(clientOpts, tls_client.WithTransportOptions(transportOptions))
	switch o.HttpVersion {
	case "1.1":
		clientOpts = append(clientOpts, tls_client.WithForceHttp1())
	case "1.0":
		return nil, fmt.Errorf("HTTP/1.0 requires --raw-http")
	}
	return tls_client.NewHttpClient(tls_client.NewNoopLogger(), clientOpts...)
}

func profileWithAllowHTTP(p profiles.ClientProfile) profiles.ClientProfile {
	return profiles.NewClientProfile(
		p.GetClientHelloId(),
		p.GetSettings(),
		p.GetSettingsOrder(),
		p.GetPseudoHeaderOrder(),
		p.GetConnectionFlow(),
		p.GetPriorities(),
		p.GetHeaderPriority(),
		p.GetStreamID(),
		true,
		p.GetHttp3Settings(),
		p.GetHttp3SettingsOrder(),
		p.GetHttp3PriorityParam(),
		p.GetHttp3PseudoHeaderOrder(),
		p.GetHttp3SendGreaseFrames(),
	)
}

type Doer interface {
	Do(*http.Request) (*http.Response, error)
	CloseIdleConnections()
}

func NewHTTPDoer(o *config.Options, target *url.URL) (Doer, error) {
	if o.HttpVersion == "2-prior" && target.Scheme == "http" {
		if proxy, _ := transport.ProxyURLFor(o, target); proxy != "" {
			return nil, fmt.Errorf("h2c does not support a proxy")
		}
		return newH2CClient(o)
	}
	return NewHTTPClient(o, target)
}

func AddTrace(req *http.Request, o *config.Options, stderr io.Writer, info *output.ResponseInfo) *http.Request {
	trace := &httptrace.ClientTrace{
		GotConn: func(got httptrace.GotConnInfo) {
			info.RemoteAddr = got.Conn.RemoteAddr().String()
			if info.TimeConnect == 0 {
				info.TimeConnect = time.Since(info.Started)
			}
			if o.Verbose {
				fmt.Fprintf(stderr, "* Connected to %s\n", info.RemoteAddr)
			}
		},
		ConnectDone: func(_, _ string, err error) {
			if err == nil {
				info.TimeConnect = time.Since(info.Started)
			}
		},
		TLSHandshakeDone: func(state utls.ConnectionState, err error) {
			if err == nil {
				info.TimeAppConnect = time.Since(info.Started)
				info.TlsVersion = tlsname.Version(state.Version)
				info.Alpn = state.NegotiatedProtocol
			}
		},
		GotFirstResponseByte: func() {
			info.TimeStartTransfer = time.Since(info.Started)
		},
	}
	return req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
}
