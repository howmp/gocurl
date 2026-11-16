package httpx

import (
	"fmt"
	"strings"

	"gocurl/internal/config"

	http "github.com/bogdanfinn/fhttp"
	"golang.org/x/net/http/httpguts"
)

func DefaultHeaderOrder() []string      { return append([]string(nil), defaultHeaderOrder...) }
func ChromePseudoHeaderOrder() []string { return append([]string(nil), chromePseudoHeaderOrder...) }

func ValidateUserHeaders(o *config.Options) error {
	for _, h := range o.Headers {
		if !httpguts.ValidHeaderFieldName(h.Name) {
			return fmt.Errorf("invalid header name %q", h.Name)
		}
		if !httpguts.ValidHeaderFieldValue(h.Value) {
			return fmt.Errorf("invalid value for header %q", h.Name)
		}
		if h.Remove {
			continue
		}
		switch {
		case strings.EqualFold(h.Name, "Content-Length"):
			return fmt.Errorf("header Content-Length is not allowed; use --raw-request for exact framing")
		case strings.EqualFold(h.Name, "Transfer-Encoding"):
			return fmt.Errorf("header Transfer-Encoding is not allowed; use --tr-encoding or --raw-request")
		}
	}
	return nil
}

func ValidateRequestTarget(target string) error {
	for i := 0; i < len(target); i++ {
		if target[i] <= 0x20 || target[i] >= 0x7f {
			return fmt.Errorf("invalid request target %q", target)
		}
	}
	return nil
}

func MergeUserHeader(h http.Header, userSet map[string]bool, hdr config.HeaderArg) (string, bool) {
	name := http.CanonicalHeaderKey(hdr.Name)
	lower := strings.ToLower(hdr.Name)
	wasNew := !userSet[name]
	switch {
	case hdr.Remove:
		h.Del(name)
	case hdr.Empty:
		h.Set(name, "")
	case userSet[name]:
		h.Add(name, hdr.Value)
	default:
		h.Set(name, hdr.Value)
	}
	userSet[name] = true
	return lower, wasNew
}
