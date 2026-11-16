package config

import (
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type HeaderArg struct {
	Name   string
	Value  string
	Remove bool
	Empty  bool
}

type dataKind int

const (
	DataForm dataKind = iota
	DataRaw
	DataBinary
	DataURLEncode
)

type DataPart struct {
	Kind  dataKind
	Value string
}

// RequestOptions groups ...
type RequestOptions struct {
	Urls            []string
	Method          string
	Headers         []HeaderArg
	Data            []DataPart
	Get             bool
	JsonData        string
	JsonSet         bool
	Location        bool
	LocationTrusted bool
	Post301         bool
	Post302         bool
	Post303         bool
	MaxRedirs       int
	UserAgent       string
	Referer         string
	Cookie          string
	User            string
	Forms           []FormField
	RequestTarget   string
	HeaderOrder     []string
	AuthMode        string
	Netrc           bool
	NetrcFile       string
}

// TransferOptions groups ...
type TransferOptions struct {
	Fail             bool
	Compressed       bool
	Raw              bool
	TrEncoding       bool
	RawHTTP          bool
	RawRequestFile   string
	HttpVersion      string
	MaxTime          time.Duration
	ConnectTimeout   time.Duration
	FailWithBody     bool
	RangeSpec        string
	Retries          int
	RetryDelay       time.Duration
	RetryMaxTime     time.Duration
	RetryAllErrors   bool
	RetryConnrefused bool
	RetryIdempotent  bool
	RetryMaxPerHost  int
	LimitRate        int64
	MaxFileSize      int64
	ResumeOffset     int64
	ContinueAtSet    bool
}

// NetworkOptions groups ...
type NetworkOptions struct {
	Insecure             bool
	Ipv4                 bool
	Ipv6                 bool
	Resolves             map[string][]string
	DnsServers           []string
	DohURL               string
	DohInsecure          bool
	DenyPrivateAddresses bool
	CaPath               string
	Proxy                string
	ProxyUser            string
	NoProxy              string
	ProxyHeaders         []HeaderArg
	ProxyInsecure        bool
	PreProxy             string
	ConnectTo            map[string]string
	UnixSocket           string
	AbstractUnix         bool
	InterfaceName        string
	CertFile             string
	KeyFile              string
	CertType             string
	KeyType              string
	KeyPass              string
	CaCert               string
}

// OutputOptions groups ...
type OutputOptions struct {
	Outputs      []string
	RemoteName   bool
	CreateDirs   bool
	Include      bool
	Head         bool
	Silent       bool
	ShowError    bool
	Verbose      bool
	WriteOut     string
	DumpHeader   string
	NoBuffer     bool
	JsonOutput   bool
	JsonEnvelope bool
	ShowSecrets  bool
	Help         bool
	Version      bool
}

// FTPOptions groups ...
type FTPOptions struct {
	ListOnly   bool
	FtpPort    string
	FtpPasv    bool
	FtpSSL     bool
	UploadFile string
}

// RuntimeOptions groups ...
type RuntimeOptions struct {
	RetryBudget   *RetryBudget
	Session       string
	AppendOutput  bool
	CookieJar     CookieJar
	CookieHeader  string
	CookieFile    string
	CookieJarFile string
	CookieHosts   *CookieHostSet
	Parallel      bool
	ParallelMax   int
	Persist       bool
	DaemonSocket  string
	ProxyDisabled bool
	TLSState      *TLSStateStore
}

type Options struct {
	RequestOptions
	TransferOptions
	NetworkOptions
	OutputOptions
	FTPOptions
	RuntimeOptions
}

type optionError struct{ msg string }

func (e *optionError) Error() string { return e.msg }

func NewOptions() *Options {
	o := &Options{}
	o.MaxRedirs = 50
	o.HttpVersion = "auto"
	o.ParallelMax = 50
	o.Persist = true
	o.Resolves = make(map[string][]string)
	o.ConnectTo = make(map[string]string)
	return o
}

func ParseArgs(args []string) (*Options, error) {
	o := NewOptions()
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			o.Urls = append(o.Urls, args[i+1:]...)
			return o, nil
		case strings.HasPrefix(arg, "--"):
			if err := parseLongOption(o, args, &i, arg[2:]); err != nil {
				return nil, err
			}
		case strings.HasPrefix(arg, "-") && arg != "-":
			if err := parseShortOptions(o, args, &i, arg[1:]); err != nil {
				return nil, err
			}
		default:
			o.Urls = append(o.Urls, arg)
		}
	}
	if o.Fail && o.FailWithBody {
		return nil, &optionError{"option --fail-with-body: is badly used here"}
	}
	return o, nil
}

type boolOption struct {
	apply func(*Options)
}

var longBoolOptions = map[string]boolOption{
	"help":                   {func(o *Options) { o.Help = true }},
	"version":                {func(o *Options) { o.Version = true }},
	"get":                    {func(o *Options) { o.Get = true }},
	"remote-name":            {func(o *Options) { o.RemoteName = true }},
	"create-dirs":            {func(o *Options) { o.CreateDirs = true }},
	"include":                {func(o *Options) { o.Include = true }},
	"head":                   {func(o *Options) { o.Head = true }},
	"location":               {func(o *Options) { o.Location = true }},
	"location-trusted":       {func(o *Options) { o.Location = true; o.LocationTrusted = true }},
	"post301":                {func(o *Options) { o.Post301 = true }},
	"post302":                {func(o *Options) { o.Post302 = true }},
	"post303":                {func(o *Options) { o.Post303 = true }},
	"insecure":               {func(o *Options) { o.Insecure = true }},
	"silent":                 {func(o *Options) { o.Silent = true }},
	"show-error":             {func(o *Options) { o.ShowError = true }},
	"verbose":                {func(o *Options) { o.Verbose = true }},
	"fail":                   {func(o *Options) { o.Fail = true }},
	"fail-with-body":         {func(o *Options) { o.FailWithBody = true }},
	"no-fail-with-body":      {func(o *Options) { o.FailWithBody = false }},
	"compressed":             {func(o *Options) { o.Compressed = true }},
	"deny-private-addresses": {func(o *Options) { o.DenyPrivateAddresses = true }},
	"raw":                    {func(o *Options) { o.Raw = true }},
	"tr-encoding":            {func(o *Options) { o.TrEncoding = true }},
	"raw-http":               {func(o *Options) { o.RawHTTP = true }},
	"proxy-insecure":         {func(o *Options) { o.ProxyInsecure = true }},
	"parallel":               {func(o *Options) { o.Parallel = true }},
	"persist":                {func(o *Options) { o.Persist = true }},
	"no-persist":             {func(o *Options) { o.Persist = false }},
	"retry-all-errors":       {func(o *Options) { o.RetryAllErrors = true }},
	"retry-connrefused":      {func(o *Options) { o.RetryConnrefused = true }},
	"retry-idempotent":       {func(o *Options) { o.RetryIdempotent = true }},
	"json-output":            {func(o *Options) { o.JsonOutput = true }},
	"json-envelope":          {func(o *Options) { o.JsonEnvelope = true }},
	"show-secrets":           {func(o *Options) { o.ShowSecrets = true }},
	"ipv4":                   {func(o *Options) { o.Ipv4 = true }},
	"ipv6":                   {func(o *Options) { o.Ipv6 = true }},
	"ftp-pasv":               {func(o *Options) { o.FtpPasv = true }},
	"ftp-ssl":                {func(o *Options) { o.FtpSSL = true }},
	"ssl":                    {func(o *Options) { o.FtpSSL = true }},
	"ssl-reqd":               {func(o *Options) { o.FtpSSL = true }},
	"doh-insecure":           {func(o *Options) { o.DohInsecure = true }},
	"list-only":              {func(o *Options) { o.ListOnly = true }},
	"no-buffer":              {func(o *Options) { o.NoBuffer = true }},
	"netrc":                  {func(o *Options) { o.Netrc = true }},
	"no-location":            {func(o *Options) { o.Location = false }},
	"no-include":             {func(o *Options) { o.Include = false }},
	"no-head":                {func(o *Options) { o.Head = false }},
	"no-insecure":            {func(o *Options) { o.Insecure = false }},
	"no-silent":              {func(o *Options) { o.Silent = false }},
	"no-show-error":          {func(o *Options) { o.ShowError = false }},
	"no-verbose":             {func(o *Options) { o.Verbose = false }},
	"no-fail":                {func(o *Options) { o.Fail = false }},
	"no-compressed":          {func(o *Options) { o.Compressed = false }},
	"no-get":                 {func(o *Options) { o.Get = false }},
	"no-remote-name":         {func(o *Options) { o.RemoteName = false }},
	"globoff":                {func(*Options) {}},
	"path-as-is":             {func(*Options) {}},
	"no-progress-meter":      {func(*Options) {}},
	"tcp-nodelay":            {func(*Options) {}},
	"http1.0":                {func(o *Options) { o.HttpVersion = "1.0"; o.RawHTTP = true }},
	"http1.1":                {func(o *Options) { o.HttpVersion = "1.1"; o.RawHTTP = false }},
	"http2":                  {func(o *Options) { o.HttpVersion = "2"; o.RawHTTP = false }},
	"http2-prior-knowledge":  {func(o *Options) { o.HttpVersion = "2-prior"; o.RawHTTP = false }},
	"basic":                  {func(o *Options) { o.AuthMode = "basic" }},
	"digest":                 {func(o *Options) { o.AuthMode = "digest" }},
	"ntlm":                   {func(o *Options) { o.AuthMode = "ntlm" }},
	"negotiate":              {func(o *Options) { o.AuthMode = "negotiate" }},
	"anyauth":                {func(o *Options) { o.AuthMode = "anyauth" }},
}

var longValueOptions = map[string]func(*Options, string) error{
	"url":                  func(o *Options, v string) error { o.Urls = append(o.Urls, v); return nil },
	"request":              func(o *Options, v string) error { o.Method = v; return nil },
	"json":                 func(o *Options, v string) error { o.JsonData = v; o.JsonSet = true; return nil },
	"output":               func(o *Options, v string) error { o.Outputs = append(o.Outputs, v); return nil },
	"user-agent":           func(o *Options, v string) error { o.UserAgent = v; return nil },
	"referer":              func(o *Options, v string) error { o.Referer = v; return nil },
	"cookie":               func(o *Options, v string) error { o.Cookie = v; return nil },
	"cookie-jar":           func(o *Options, v string) error { o.CookieJarFile = v; return nil },
	"user":                 func(o *Options, v string) error { o.User = v; return nil },
	"proxy":                func(o *Options, v string) error { o.Proxy = v; return nil },
	"proxy-user":           func(o *Options, v string) error { o.ProxyUser = v; return nil },
	"noproxy":              func(o *Options, v string) error { o.NoProxy = v; return nil },
	"preproxy":             func(o *Options, v string) error { o.PreProxy = v; return nil },
	"unix-socket":          func(o *Options, v string) error { o.UnixSocket = v; return nil },
	"abstract-unix-socket": func(o *Options, v string) error { o.UnixSocket = v; o.AbstractUnix = true; return nil },
	"interface":            func(o *Options, v string) error { o.InterfaceName = v; return nil },
	"cert":                 func(o *Options, v string) error { o.CertFile = v; return nil },
	"key":                  func(o *Options, v string) error { o.KeyFile = v; return nil },
	"cert-type":            func(o *Options, v string) error { o.CertType = v; return nil },
	"key-type":             func(o *Options, v string) error { o.KeyType = v; return nil },
	"pass":                 func(o *Options, v string) error { o.KeyPass = v; return nil },
	"cacert":               func(o *Options, v string) error { o.CaCert = v; return nil },
	"request-target":       func(o *Options, v string) error { o.RequestTarget = v; return nil },
	"session":              func(o *Options, v string) error { o.Session = v; return nil },
	"capath":               func(o *Options, v string) error { o.CaPath = v; return nil },
	"write-out":            func(o *Options, v string) error { o.WriteOut = v; return nil },
	"dump-header":          func(o *Options, v string) error { o.DumpHeader = v; return nil },
	"ftp-port":             func(o *Options, v string) error { o.FtpPort = v; return nil },
	"doh-url": func(o *Options, v string) error {
		if err := validateDohURL(v); err != nil {
			return err
		}
		o.DohURL = v
		return nil
	},
	"upload-file": func(o *Options, v string) error { o.UploadFile = v; return nil },
	"header": func(o *Options, v string) error {
		headers, err := parseHeaderOption(v)
		if err != nil {
			return err
		}
		o.Headers = append(o.Headers, headers...)
		return nil
	},
	"data": func(o *Options, v string) error {
		o.Data = append(o.Data, DataPart{Kind: DataForm, Value: v})
		return nil
	},
	"data-ascii": func(o *Options, v string) error {
		o.Data = append(o.Data, DataPart{Kind: DataForm, Value: v})
		return nil
	},
	"data-raw": func(o *Options, v string) error {
		o.Data = append(o.Data, DataPart{Kind: DataRaw, Value: v})
		return nil
	},
	"data-binary": func(o *Options, v string) error {
		o.Data = append(o.Data, DataPart{Kind: DataBinary, Value: v})
		return nil
	},
	"data-urlencode": func(o *Options, v string) error {
		o.Data = append(o.Data, DataPart{Kind: DataURLEncode, Value: v})
		return nil
	},
	"form": func(o *Options, v string) error {
		f, err := ParseFormOption(v, false)
		if err != nil {
			return err
		}
		o.Forms = append(o.Forms, f)
		return nil
	},
	"form-string": func(o *Options, v string) error {
		f, err := ParseFormOption(v, true)
		if err != nil {
			return err
		}
		o.Forms = append(o.Forms, f)
		return nil
	},
	"header-order": func(o *Options, v string) error {
		o.HeaderOrder = append([]string(nil), splitList(v)...)
		for i := range o.HeaderOrder {
			o.HeaderOrder[i] = strings.ToLower(strings.TrimSpace(o.HeaderOrder[i]))
		}
		return nil
	},
	"max-redirs": func(o *Options, v string) error {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return &optionError{fmt.Sprintf("option --max-redirs: invalid number %q", v)}
		}
		o.MaxRedirs = n
		return nil
	},
	"resolve": func(o *Options, v string) error { return parseResolveOption(o, v) },
	"range": func(o *Options, v string) error {
		if err := validateRange(v); err != nil {
			return err
		}
		o.RangeSpec = v
		return nil
	},
	"raw-request": func(o *Options, v string) error {
		o.RawRequestFile = strings.TrimPrefix(v, "@")
		o.RawHTTP = true
		return nil
	},
	"proxy-header": func(o *Options, v string) error {
		headers, err := parseHeaderOption(v)
		if err != nil {
			return err
		}
		o.ProxyHeaders = append(o.ProxyHeaders, headers...)
		return nil
	},
	"connect-to": func(o *Options, v string) error { return parseConnectToOption(o, v) },
	"parallel-max": func(o *Options, v string) error {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return &optionError{fmt.Sprintf("option --parallel-max: invalid number %q", v)}
		}
		o.ParallelMax = n
		return nil
	},
	"max-time": func(o *Options, v string) error {
		d, err := parseSeconds(v)
		if err != nil {
			return &optionError{fmt.Sprintf("option --max-time: invalid number %q", v)}
		}
		o.MaxTime = d
		return nil
	},
	"connect-timeout": func(o *Options, v string) error {
		d, err := parseSeconds(v)
		if err != nil {
			return &optionError{fmt.Sprintf("option --connect-timeout: invalid number %q", v)}
		}
		o.ConnectTimeout = d
		return nil
	},
	"retry": func(o *Options, v string) error {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return &optionError{fmt.Sprintf("option --retry: invalid number %q", v)}
		}
		o.Retries = n
		return nil
	},
	"retry-delay": func(o *Options, v string) error {
		d, err := parseSeconds(v)
		if err != nil {
			return &optionError{fmt.Sprintf("option --retry-delay: invalid number %q", v)}
		}
		o.RetryDelay = d
		return nil
	},
	"retry-max-time": func(o *Options, v string) error {
		d, err := parseSeconds(v)
		if err != nil {
			return &optionError{fmt.Sprintf("option --retry-max-time: invalid number %q", v)}
		}
		o.RetryMaxTime = d
		return nil
	},
	"retry-max-per-host": func(o *Options, v string) error {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return &optionError{fmt.Sprintf("option --retry-max-per-host: invalid number %q", v)}
		}
		o.RetryMaxPerHost = n
		return nil
	},
	"limit-rate": func(o *Options, v string) error {
		n, err := parseSize(v)
		if err != nil || n <= 0 {
			return &optionError{fmt.Sprintf("option --limit-rate: invalid rate %q", v)}
		}
		o.LimitRate = n
		return nil
	},
	"max-filesize": func(o *Options, v string) error {
		n, err := parseSize(v)
		if err != nil || n < 0 {
			return &optionError{fmt.Sprintf("option --max-filesize: invalid size %q", v)}
		}
		o.MaxFileSize = n
		return nil
	},
	"continue-at": func(o *Options, v string) error {
		if v == "-" {
			o.ContinueAtSet = true
			o.ResumeOffset = -1
			return nil
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return &optionError{fmt.Sprintf("option --continue-at: invalid offset %q", v)}
		}
		o.ContinueAtSet = true
		o.ResumeOffset = n
		return nil
	},
	"dns-servers": func(o *Options, v string) error {
		servers, err := parseDNSServers(v)
		if err != nil {
			return err
		}
		o.DnsServers = append(o.DnsServers, servers...)
		return nil
	},
	"netrc-file": func(o *Options, v string) error { o.Netrc = true; o.NetrcFile = v; return nil },
}

func parseLongOption(o *Options, args []string, i *int, raw string) error {
	name := raw
	value := ""
	hasValue := false
	if idx := strings.IndexByte(raw, '='); idx >= 0 {
		name = raw[:idx]
		value = raw[idx+1:]
		hasValue = true
	}

	take := func() (string, error) {
		if hasValue {
			return value, nil
		}
		if *i+1 >= len(args) {
			return "", &optionError{fmt.Sprintf("option --%s: requires parameter", name)}
		}
		*i++
		return args[*i], nil
	}
	noValue := func() error {
		if hasValue {
			return &optionError{fmt.Sprintf("option --%s: doesn't allow a parameter", name)}
		}
		return nil
	}

	if spec, ok := longBoolOptions[name]; ok {
		if err := noValue(); err != nil {
			return err
		}
		spec.apply(o)
		return nil
	}
	if apply, ok := longValueOptions[name]; ok {
		v, err := take()
		if err != nil {
			return err
		}
		return apply(o, v)
	}
	switch name {
	case "http3", "http3-only",
		"remote-header-name", "remote-time", "output-dir",
		"socks4", "socks4a", "socks5", "socks5-hostname",
		"tlsv1.0", "tlsv1.1", "tlsv1.2", "tlsv1.3", "tls-max", "ciphers", "curves",
		"pinnedpubkey", "cert-status", "trace", "trace-ascii", "stderr",
		"next":
		return &optionError{fmt.Sprintf("option --%s: not supported", name)}
	default:
		return &optionError{fmt.Sprintf("option --%s: is unknown", name)}

	}
}

var shortOptionNames = map[byte]string{
	'h': "help",
	'V': "version",
	'X': "request",
	'H': "header",
	'd': "data",
	'F': "form",
	'r': "range",
	'o': "output",
	'O': "remote-name",
	'i': "include",
	'I': "head",
	'L': "location",
	'k': "insecure",
	's': "silent",
	'S': "show-error",
	'v': "verbose",
	'f': "fail",
	'G': "get",
	'A': "user-agent",
	'e': "referer",
	'b': "cookie",
	'c': "cookie-jar",
	'u': "user",
	'x': "proxy",
	'U': "proxy-user",
	'E': "cert",
	'P': "ftp-port",
	'Z': "parallel",
	'm': "max-time",
	'w': "write-out",
	'D': "dump-header",
	'4': "ipv4",
	'6': "ipv6",
	'l': "list-only",
	'T': "upload-file",
	'N': "no-buffer",
	'C': "continue-at",
}

const (
	shortNoOps       = "qg#"
	shortUnsupported = "0123KQyYztBanpRjJ"
)

func parseShortOptions(o *Options, args []string, i *int, raw string) error {
	for pos := 0; pos < len(raw); pos++ {
		ch := raw[pos]
		if strings.ContainsRune(shortNoOps, rune(ch)) {
			continue
		}
		if strings.ContainsRune(shortUnsupported, rune(ch)) {
			return &optionError{fmt.Sprintf("option -%c: not supported", ch)}
		}
		long, ok := shortOptionNames[ch]
		if !ok {
			return &optionError{fmt.Sprintf("option -%c: is unknown", ch)}
		}
		if opt, ok := longBoolOptions[long]; ok {
			opt.apply(o)
			continue
		}
		apply, ok := longValueOptions[long]
		if !ok {
			return &optionError{fmt.Sprintf("option -%c: is unknown", ch)}
		}
		value, err := shortOptionValue(args, i, raw, pos, ch)
		if err != nil {
			return err
		}
		if err := apply(o, value); err != nil {
			return err
		}
		pos = len(raw)
	}
	return nil
}

func shortOptionValue(args []string, i *int, raw string, pos int, ch byte) (string, error) {
	if rest := raw[pos+1:]; rest != "" {
		return rest, nil
	}
	if *i+1 >= len(args) {
		return "", &optionError{fmt.Sprintf("option -%c: requires parameter", ch)}
	}
	*i++
	return args[*i], nil
}

func validateDohURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return &optionError{"option --doh-url: invalid URL"}
	}
	return nil
}

func parseHeaderOption(v string) ([]HeaderArg, error) {
	if !strings.HasPrefix(v, "@") {
		h, err := parseHeaderArg(v)
		if err != nil {
			return nil, err
		}
		return []HeaderArg{h}, nil
	}
	data, err := ReadLocalFile(v[1:])
	if err != nil {
		return nil, err
	}
	var headers []HeaderArg
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		h, err := parseHeaderArg(line)
		if err != nil {
			return nil, err
		}
		headers = append(headers, h)
	}
	return headers, nil
}

func parseHeaderArg(v string) (HeaderArg, error) {
	h := HeaderArg{}
	if idx := strings.IndexByte(v, ':'); idx >= 0 {
		h.Name = strings.TrimSpace(v[:idx])
		h.Value = strings.TrimLeft(v[idx+1:], " \t")
		if h.Name == "" {
			return h, &optionError{"invalid header"}
		}
		if h.Value == "" {
			h.Remove = true
		}
		return h, nil
	}
	if strings.HasSuffix(v, ";") {
		h.Name = strings.TrimSpace(strings.TrimSuffix(v, ";"))
		h.Empty = true
		if h.Name == "" {
			return h, &optionError{"invalid header"}
		}
		return h, nil
	}
	return h, &optionError{"invalid header"}
}

func parseSeconds(v string) (time.Duration, error) {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0, fmt.Errorf("invalid seconds")
	}
	if f > float64(math.MaxInt64)/float64(time.Second) {
		return 0, fmt.Errorf("seconds too large")
	}
	return time.Duration(f * float64(time.Second)), nil
}

func splitList(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool {
		return r == ':' || r == ','
	})
}

// OptionNames returns supported long option names for CLI help coverage checks.
func OptionNames() []string {
	names := make([]string, 0, len(longBoolOptions)+len(longValueOptions))
	for name := range longBoolOptions {
		names = append(names, name)
	}
	for name := range longValueOptions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
