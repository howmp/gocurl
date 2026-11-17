package output

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"gocurl/internal/config"
	"gocurl/internal/exitcode"
	"gocurl/internal/tlsname"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	http "github.com/bogdanfinn/fhttp"
)

var (
	dumpMu          sync.Mutex
	dumpInitialized bool
)

func ResetHeaderDump() {
	dumpMu.Lock()
	dumpInitialized = false
	dumpMu.Unlock()
}

type ResponseInfo struct {
	Url               string
	Scheme            string
	HttpVersion       string
	StatusCode        int
	ContentType       string
	SizeDownload      int64
	SizeHeader        int64
	TimeTotal         time.Duration
	TimeConnect       time.Duration
	TimeAppConnect    time.Duration
	TimeStartTransfer time.Duration
	NumRedirects      int
	NumRetries        int
	SslVerifyResult   int
	RemoteAddr        string
	TlsVersion        string
	Alpn              string
	Started           time.Time
}

func protocolName(proto string) string {
	switch proto {
	case "HTTP/2.0":
		return "HTTP/2"
	case "HTTP/1.1":
		return "HTTP/1.1"
	case "HTTP/1.0":
		return "HTTP/1.0"
	default:
		return proto
	}
}

func WriteResponseHeaders(w io.Writer, resp *http.Response) error {
	proto := protocolName(resp.Proto)
	if strings.HasPrefix(proto, "HTTP/2") {
		if _, err := fmt.Fprintf(w, "%s %d\r\n", proto, resp.StatusCode); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(w, "%s %s\r\n", proto, resp.Status); err != nil {
			return err
		}
	}
	keys := make([]string, 0, len(resp.Header))
	for k := range resp.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range resp.Header[k] {
			if _, err := fmt.Fprintf(w, "%s: %s\r\n", k, v); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprint(w, "\r\n")
	return err
}

func PrintRequestVerbose(w io.Writer, req *http.Request, o *config.Options) {
	version := "1.1"
	switch o.HttpVersion {
	case "1.0":
		version = "1.0"
	case "2", "2-prior":
		version = "2"
	default:
		if req.URL.Scheme == "https" {
			version = "2"
		}
	}
	fmt.Fprintf(w, "> %s %s HTTP/%s\n", req.Method, req.URL.RequestURI(), version)
	keys := make([]string, 0, len(req.Header))
	for k := range req.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k == http.HeaderOrderKey || k == http.PHeaderOrderKey {
			continue
		}
		for _, v := range req.Header[k] {
			fmt.Fprintf(w, "> %s: %s\n", k, RedactHeaderValue(k, v, o.ShowSecrets))
		}
	}
	if req.Host != "" {
		fmt.Fprintf(w, "> Host: %s\n", req.Host)
	}
	fmt.Fprintln(w, ">")
}

func PrintResponseVerbose(w io.Writer, resp *http.Response, showSecrets bool) {
	if resp.TLS != nil {
		fmt.Fprintf(w, "* TLS version=%s cipher=%s alpn=%q\n",
			tlsname.Version(resp.TLS.Version), tlsname.Cipher(resp.TLS.CipherSuite), resp.TLS.NegotiatedProtocol)
	}
	fmt.Fprintf(w, "< %s %d\n", protocolName(resp.Proto), resp.StatusCode)
	keys := make([]string, 0, len(resp.Header))
	for k := range resp.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range resp.Header[k] {
			fmt.Fprintf(w, "< %s: %s\n", k, RedactHeaderValue(k, v, showSecrets))
		}
	}
	fmt.Fprintln(w, "<")
}

func RedactHeaderValue(name, value string, showSecrets bool) string {
	if showSecrets {
		return value
	}
	switch strings.ToLower(name) {
	case "authorization", "proxy-authorization", "cookie", "set-cookie":
		return "[REDACTED]"
	}
	return value
}

func RemoteName(u *url.URL) (string, error) {
	decoded, err := url.PathUnescape(u.Path)
	if err != nil {
		return "", fmt.Errorf("invalid remote file name: %w", err)
	}
	name := path.Base(decoded)
	if name == "/" || name == "." || name == "" {
		return "", fmt.Errorf("Remote file name has no length")
	}
	if name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return "", fmt.Errorf("refusing unsafe remote file name %q", name)
	}
	return name, nil
}

func OutputName(o *config.Options, u *url.URL, idx int) (string, error) {
	name := ""
	if len(o.Outputs) > 0 {
		outputIndex := idx
		if outputIndex >= len(o.Outputs) {
			outputIndex = len(o.Outputs) - 1
		}
		name = o.Outputs[outputIndex]
	} else if o.RemoteName {
		var err error
		name, err = RemoteName(u)
		if err != nil {
			return "", err
		}
	}
	return name, nil
}

func WithAppendOutput(o *config.Options) *config.Options {
	local := *o
	local.AppendOutput = true
	return &local
}

func ResumeOutput(o *config.Options, u *url.URL, idx int, offset int64, requireFile bool) (*config.Options, error) {
	if offset <= 0 {
		return o, nil
	}
	name, err := OutputName(o, u, idx)
	if err != nil {
		return nil, err
	}
	if name == "" || name == "-" {
		if requireFile {
			return nil, fmt.Errorf("resume requires an output file")
		}
		return o, nil
	}
	return WithAppendOutput(o), nil
}

func ResolveResumeOffset(o *config.Options, u *url.URL, idx int) (int64, error) {
	if o == nil || !o.ContinueAtSet {
		return 0, nil
	}
	offset := o.ResumeOffset
	if offset >= 0 {
		return offset, nil
	}
	name, err := OutputName(o, u, idx)
	if err != nil {
		return 0, err
	}
	if name == "" || name == "-" {
		return 0, fmt.Errorf("option --continue-at: output file is required for '-'")
	}
	f, err := openOutputFilePlatform(name, os.O_RDONLY)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	info, statErr := f.Stat()
	_ = f.Close()
	if statErr != nil {
		return 0, statErr
	}
	return info.Size(), nil
}

func OutputWriter(o *config.Options, u *url.URL, idx int, stdout io.Writer) (io.Writer, func() error, error) {
	name, err := OutputName(o, u, idx)
	if err != nil {
		return nil, nil, err
	}
	if name == "" || name == "-" {
		w, flush := bufferedOutput(stdout, o.NoBuffer, isTerminalWriter(stdout))
		return w, func() error {
			if err := flush(); err != nil {
				return &exitcode.WriteError{Err: err}
			}
			return nil
		}, nil
	}
	if o.CreateDirs {
		if dir := filepath.Dir(name); dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, nil, &exitcode.WriteError{Err: err}
			}
		}
	}
	var f *os.File
	resumeOffset, offsetErr := ResolveResumeOffset(o, u, idx)
	if offsetErr != nil {
		return nil, nil, offsetErr
	}
	if o.ContinueAtSet && resumeOffset > 0 {
		f, err = openResumeOutput(name, resumeOffset)
	} else if o.ContinueAtSet && o.ResumeOffset >= 0 {
		f, err = openResumeOutput(name, o.ResumeOffset)
	} else {
		f, err = openOutputFile(name, o.AppendOutput, !o.AppendOutput)
	}
	if err != nil {
		if coded, ok := err.(*exitcode.Coded); ok {
			return nil, nil, coded
		}
		return nil, nil, &exitcode.WriteError{Err: err}
	}
	w, flush := bufferedOutput(f, o.NoBuffer, false)
	return w, idempotentClose(func() error {
		flushErr := flush()
		closeErr := f.Close()
		if flushErr != nil {
			return &exitcode.WriteError{Err: flushErr}
		}
		if closeErr != nil {
			return &exitcode.WriteError{Err: closeErr}
		}
		return nil
	}), nil
}

func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func bufferedOutput(w io.Writer, noBuffer, lineBuffered bool) (io.Writer, func() error) {
	if noBuffer {
		return w, func() error { return nil }
	}
	bw := bufio.NewWriter(w)
	if lineBuffered {
		return &lineBufferedWriter{bw: bw}, bw.Flush
	}
	return &writeOnly{bw}, bw.Flush
}

type writeOnly struct {
	io.Writer
}

type lineBufferedWriter struct {
	bw *bufio.Writer
}

func (w *lineBufferedWriter) Write(p []byte) (int, error) {
	n, err := w.bw.Write(p)
	if err == nil && bytes.IndexByte(p[:n], '\n') >= 0 {
		err = w.bw.Flush()
	}
	return n, err
}

func idempotentClose(fn func() error) func() error {
	var once sync.Once
	var err error
	return func() error {
		once.Do(func() { err = fn() })
		return err
	}
}

func WriteHeaderDump(o *config.Options, resp *http.Response, idx int, stdout io.Writer) error {
	if o.DumpHeader == "" {
		return nil
	}
	dumpMu.Lock()
	defer dumpMu.Unlock()
	if o.DumpHeader == "-" {
		return WriteResponseHeaders(stdout, resp)
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if !dumpInitialized {
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}
	appendMode := flags&os.O_APPEND != 0
	f, err := openOutputFile(o.DumpHeader, appendMode, !appendMode)
	if err != nil {
		return &exitcode.WriteError{Err: err}
	}
	dumpInitialized = true
	if err := WriteResponseHeaders(f, resp); err != nil {
		_ = f.Close()
		return &exitcode.WriteError{Err: err}
	}
	if err := f.Close(); err != nil {
		return &exitcode.WriteError{Err: err}
	}
	return nil
}

func hostOnly(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

func WriteOutFormat(o *config.Options, info *ResponseInfo, stdout io.Writer) error {
	if o.WriteOut == "" {
		return nil
	}
	format := o.WriteOut
	if strings.HasPrefix(format, "@") {
		data, err := config.ReadLocalFile(format[1:])
		if err != nil {
			return err
		}
		format = string(data)
	}
	format = strings.NewReplacer(
		`\n`, "\n",
		`\r`, "\r",
		`\t`, "\t",
		`\\`, "\\",
	).Replace(format)

	values := map[string]any{
		"http_code":          info.StatusCode,
		"response_code":      info.StatusCode,
		"http_version":       info.HttpVersion,
		"scheme":             info.Scheme,
		"url_effective":      info.Url,
		"size_download":      info.SizeDownload,
		"size_header":        info.SizeHeader,
		"time_total":         info.TimeTotal.Seconds(),
		"time_connect":       info.TimeConnect.Seconds(),
		"time_appconnect":    info.TimeAppConnect.Seconds(),
		"time_starttransfer": info.TimeStartTransfer.Seconds(),
		"num_redirects":      info.NumRedirects,
		"num_retries":        info.NumRetries,
		"content_type":       info.ContentType,
		"remote_ip":          hostOnly(info.RemoteAddr),
		"ssl_verify_result":  info.SslVerifyResult,
		"tls_version":        info.TlsVersion,
		"alpn":               info.Alpn,
	}
	jsonInfo, _ := json.Marshal(values)
	values["json"] = string(jsonInfo)
	var b strings.Builder
	for i := 0; i < len(format); i++ {
		if format[i] == '%' && i+1 < len(format) && format[i+1] == '{' {
			if end := strings.IndexByte(format[i+2:], '}'); end >= 0 {
				key := format[i+2 : i+2+end]
				if v, ok := values[key]; ok {
					switch value := v.(type) {
					case string:
						b.WriteString(value)
					case float64:
						b.WriteString(strconv.FormatFloat(value, 'f', 6, 64))
					default:
						b.WriteString(fmt.Sprint(value))
					}
				}
				i += 2 + end
				continue
			}
		}
		b.WriteByte(format[i])
	}
	_, err := io.WriteString(stdout, b.String())
	return err
}
