package httpx

import (
	"net/url"
	"strings"
)

func defaultPortForScheme(scheme string) string {
	switch scheme {
	case "http", "ws":
		return "80"
	case "https", "wss":
		return "443"
	case "ftp":
		return "21"
	case "ftps":
		return "990"
	default:
		return ""
	}
}

func originOf(u *url.URL) (scheme, host, port string) {
	if u == nil {
		return "", "", ""
	}
	scheme = strings.ToLower(u.Scheme)
	host = normalizeRedirectHost(u.Hostname())
	port = u.Port()
	if port == "" {
		port = defaultPortForScheme(scheme)
	}
	return scheme, host, port
}

func SameOriginURL(a, b *url.URL) bool {
	aScheme, aHost, aPort := originOf(a)
	bScheme, bHost, bPort := originOf(b)
	return aScheme == bScheme && aHost == bHost && aPort == bPort
}

func URLOriginKey(u *url.URL) string {
	scheme, host, port := originOf(u)
	return scheme + "://" + host + ":" + port
}
