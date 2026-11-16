package daemon

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/net/http/httpguts"
)

const (
	daemonProtocolVersion = 5
	maxDaemonMetadata     = 1 << 20
	maxDaemonRedirects    = 10000
	maxDaemonHeaders      = 4096
	maxDumpCookiesBytes   = 16 << 20
	defaultDaemonIdle     = 15 * time.Minute
)

const (
	daemonOpRequest     = "request"
	daemonOpPing        = "ping"
	daemonOpShutdown    = "shutdown"
	daemonOpDumpCookies = "dump_cookies"
)

type daemonTransport struct {
	HTTPVersion      string              `json:"http_version"`
	Insecure         bool                `json:"insecure"`
	Proxy            string              `json:"proxy,omitempty"`
	ProxyMode        string              `json:"proxy_mode,omitempty"`
	ProxyUser        string              `json:"proxy_user,omitempty"`
	NoProxy          string              `json:"no_proxy,omitempty"`
	IPv4             bool                `json:"ipv4,omitempty"`
	IPv6             bool                `json:"ipv6,omitempty"`
	ConnectTimeoutMS int64               `json:"connect_timeout_ms,omitempty"`
	Location         bool                `json:"location,omitempty"`
	LocationTrusted  bool                `json:"location_trusted,omitempty"`
	Post301          bool                `json:"post301,omitempty"`
	Post302          bool                `json:"post302,omitempty"`
	Post303          bool                `json:"post303,omitempty"`
	MaxRedirs        int                 `json:"max_redirs,omitempty"`
	MaxTimeMS        int64               `json:"max_time_ms,omitempty"`
	CertFile         string              `json:"cert_file,omitempty"`
	KeyFile          string              `json:"key_file,omitempty"`
	CertType         string              `json:"cert_type,omitempty"`
	KeyType          string              `json:"key_type,omitempty"`
	KeyPass          string              `json:"key_pass,omitempty"`
	CACert           string              `json:"ca_cert,omitempty"`
	CAPath           string              `json:"ca_path,omitempty"`
	Resolve          map[string][]string `json:"resolve,omitempty"`
	ConnectTo        map[string]string   `json:"connect_to,omitempty"`
	UnixSocket       string              `json:"unix_socket,omitempty"`
	AbstractUnix     bool                `json:"abstract_unix,omitempty"`
	Interface        string              `json:"interface,omitempty"`
	ProxyHeaders     map[string][]string `json:"proxy_headers,omitempty"`
	ProxyInsecure    bool                `json:"proxy_insecure,omitempty"`
	PreProxy         string              `json:"pre_proxy,omitempty"`
	DnsServers       []string            `json:"dns_servers,omitempty"`
	DohURL           string              `json:"doh_url,omitempty"`
	DohInsecure      bool                `json:"doh_insecure,omitempty"`
}

type daemonRequest struct {
	Version       int                 `json:"version"`
	Op            string              `json:"op"`
	URL           string              `json:"url,omitempty"`
	Method        string              `json:"method,omitempty"`
	Host          string              `json:"host,omitempty"`
	RequestTarget string              `json:"request_target,omitempty"`
	Headers       map[string][]string `json:"headers,omitempty"`
	HeaderOrder   []string            `json:"header_order,omitempty"`
	BodyLength    int64               `json:"body_length,omitempty"`
	Transport     daemonTransport     `json:"transport,omitempty"`
	CookieHeader  string              `json:"cookie_header,omitempty"`
	CookieHosts   []string            `json:"cookie_hosts,omitempty"`
}

type daemonResponse struct {
	Version      int                 `json:"version"`
	Status       string              `json:"status,omitempty"`
	StatusCode   int                 `json:"status_code,omitempty"`
	Proto        string              `json:"proto,omitempty"`
	Headers      map[string][]string `json:"headers,omitempty"`
	EffectiveURL string              `json:"effective_url,omitempty"`
	BodyLength   int64               `json:"body_length"`
	BodyChunked  bool                `json:"body_chunked,omitempty"`
	Redirects    []string            `json:"redirects,omitempty"`
	TLSVersion   uint16              `json:"tls_version,omitempty"`
	TLSVerified  bool                `json:"tls_verified,omitempty"`
	TLSPeerCerts [][]byte            `json:"tls_peer_certs,omitempty"`
	CipherSuite  uint16              `json:"cipher_suite,omitempty"`
	ALPN         string              `json:"alpn,omitempty"`
	RemoteAddr   string              `json:"remote_addr,omitempty"`
	Error        string              `json:"error,omitempty"`
	ErrorCode    int                 `json:"error_code,omitempty"`
}

func validateDaemonResponse(resp *daemonResponse) error {
	if resp == nil {
		return fmt.Errorf("daemon response is missing")
	}
	if resp.BodyLength < -1 {
		return fmt.Errorf("invalid daemon body length %d", resp.BodyLength)
	}
	if resp.BodyLength < 0 && !resp.BodyChunked {
		return fmt.Errorf("daemon body length %d requires chunked encoding", resp.BodyLength)
	}
	if resp.BodyChunked && resp.BodyLength != -1 {
		return fmt.Errorf("daemon chunked response has body length %d", resp.BodyLength)
	}
	if len(resp.Redirects) > maxDaemonRedirects {
		return fmt.Errorf("daemon response has too many redirects: %d", len(resp.Redirects))
	}
	for _, raw := range resp.Redirects {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("invalid daemon redirect URL")
		}
	}
	if len(resp.Headers) > maxDaemonHeaders {
		return fmt.Errorf("daemon response has too many headers: %d", len(resp.Headers))
	}
	for name, values := range resp.Headers {
		if !httpguts.ValidHeaderFieldName(name) {
			return fmt.Errorf("invalid daemon response header name")
		}
		for _, value := range values {
			if !httpguts.ValidHeaderFieldValue(value) {
				return fmt.Errorf("invalid daemon response header value")
			}
		}
	}
	return nil
}

func writeDaemonFrame(w *bufio.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(data) > maxDaemonMetadata {
		return fmt.Errorf("daemon metadata too large")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func readDaemonFrame(r *bufio.Reader, v any) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n > maxDaemonMetadata {
		return fmt.Errorf("daemon metadata too large")
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func SocketPath() string {
	return SocketPathFor("")
}

func SocketPathFor(session string) string {
	if path := os.Getenv("GOCURL_SOCKET"); path != "" {
		if abs, err := filepath.Abs(path); err == nil {
			return abs
		}
		return path
	}
	if session == "" {
		session = os.Getenv("GOCURL_SESSION")
	}
	stamp := daemonBuildStamp()
	if session != "" {
		sum := sha256.Sum256([]byte(session))
		stamp += "-s" + hex.EncodeToString(sum[:6])
	}
	path := filepath.Join(daemonDir(), "daemon-"+stamp+".sock")
	if len(path) > 100 {
		path = filepath.Join(os.TempDir(), fmt.Sprintf("gocurl-%d", os.Getuid()), "daemon-"+stamp+".sock")
	}
	return path
}

func daemonDir() string {
	if dir := os.Getenv("GOCURL_DAEMON_DIR"); dir != "" {
		return dir
	}
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
		return filepath.Join(base, fmt.Sprintf("gocurl-%d", os.Getuid()))
	}
	return filepath.Join(base, "gocurl")
}

func daemonLogPath() string {
	if path := os.Getenv("GOCURL_DAEMON_LOG"); path != "" {
		return path
	}
	return filepath.Join(daemonDir(), "daemon.log")
}

func daemonBuildStamp() string {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Sprintf("v%d", daemonProtocolVersion)
	}
	info, err := os.Stat(exe)
	if err != nil {
		return fmt.Sprintf("v%d", daemonProtocolVersion)
	}
	data := fmt.Sprintf("%d|%s|%d|%d|%d", daemonProtocolVersion, exe, info.Size(), info.ModTime().UnixNano(), os.Getuid())
	sum := sha256.Sum256([]byte(data))
	return fmt.Sprintf("v%d-%s", daemonProtocolVersion, hex.EncodeToString(sum[:6]))
}

func daemonIdleTimeout() time.Duration {
	value := os.Getenv("GOCURL_DAEMON_IDLE")
	if value == "" {
		return defaultDaemonIdle
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return defaultDaemonIdle
	}
	return d
}

const (
	daemonChunkEnd   uint32 = 0
	daemonChunkError uint32 = 0xffffffff
	maxDaemonChunk   uint32 = 1 << 20
)

const (
	chunkErrTruncated byte = 1
	chunkErrReset     byte = 2
	chunkErrTimeout   byte = 3
	chunkErrOther     byte = 4
)
