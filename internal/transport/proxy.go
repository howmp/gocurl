package transport

import (
	"fmt"
	"gocurl/internal/config"
	"net"
	"net/textproto"
	"net/url"
	"strings"
)

func bypassProxy(host, port, noProxy string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	ip := net.ParseIP(strings.Trim(host, "[]"))
	for _, entry := range strings.Split(noProxy, ",") {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry == "" {
			continue
		}
		if entry == "*" {
			return true
		}
		if strings.Contains(entry, "/") {
			if ip != nil {
				if _, network, err := net.ParseCIDR(entry); err == nil && network.Contains(ip) {
					return true
				}
			}
			continue
		}
		if entryHost, entryPort, err := net.SplitHostPort(entry); err == nil {
			if entryPort != "" && port != "" && entryPort != port {
				continue
			}
			entry = entryHost
		} else if strings.Count(entry, ":") == 1 {
			if name, value, ok := strings.Cut(entry, ":"); ok && value != "" {
				if port != "" && value != port {
					continue
				}
				entry = name
			}
		}
		entry = strings.TrimPrefix(entry, "*.")
		entry = strings.TrimPrefix(entry, ".")
		entry = strings.TrimSuffix(entry, ".")
		if entry == "" {
			continue
		}
		if host == entry || strings.HasSuffix(host, "."+entry) {
			return true
		}
		if ip != nil {
			if entryIP := net.ParseIP(strings.Trim(entry, "[]")); entryIP != nil && entryIP.Equal(ip) {
				return true
			}
		}
	}
	return false
}

func ProxyURLFor(o *config.Options, target *url.URL) (string, error) {
	if target == nil {
		return "", nil
	}
	if o != nil && o.ProxyDisabled {
		return "", nil
	}
	port := target.Port()
	if port == "" {
		port = DefaultPort(target.Scheme)
	}
	if o.Proxy != "" {
		if bypassProxy(target.Hostname(), port, o.NoProxy) {
			return "", nil
		}
		return normalizeProxyValue(o, o.Proxy)
	}

	httpProxy := parseEnvProxy("HTTP_PROXY")
	httpsProxy := parseEnvProxy("HTTPS_PROXY")
	ftpProxy := parseEnvProxy("FTP_PROXY")
	allProxy := parseEnvProxy("ALL_PROXY")
	if httpProxy == "" {
		httpProxy = allProxy
	}
	if httpsProxy == "" {
		httpsProxy = allProxy
	}
	if ftpProxy == "" {
		ftpProxy = allProxy
	}
	noProxy := o.NoProxy
	if noProxy == "" {
		noProxy = parseEnvProxy("NO_PROXY")
	}
	proxy := httpProxy
	switch strings.ToLower(target.Scheme) {
	case "https", "wss":
		proxy = httpsProxy
	case "ftp", "ftps":
		proxy = ftpProxy
	}
	if proxy == "" || bypassProxy(target.Hostname(), port, noProxy) {
		return "", nil
	}
	return normalizeProxyValue(o, proxy)
}

func normalizeProxyValue(o *config.Options, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid proxy URL: %w", err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("invalid proxy URL: missing host")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks4", "socks4a", "socks5", "socks5h":
	default:
		return "", fmt.Errorf("proxy scheme %q not supported", u.Scheme)
	}
	if o.ProxyUser != "" {
		user, pass, hasPass := strings.Cut(o.ProxyUser, ":")
		if hasPass {
			u.User = url.UserPassword(user, pass)
		} else {
			u.User = url.User(user)
		}
	}
	return u.String(), nil
}

func ProxyHeaderMap(o *config.Options) map[string][]string {
	out := make(map[string][]string)
	for _, h := range o.ProxyHeaders {
		name := textproto.CanonicalMIMEHeaderKey(h.Name)
		if h.Remove {
			delete(out, name)
			continue
		}
		if h.Empty {
			out[name] = append(out[name], "")
			continue
		}
		if _, ok := out[name]; !ok {
			out[name] = []string{h.Value}
		} else {
			out[name] = append(out[name], h.Value)
		}
	}
	return out
}
