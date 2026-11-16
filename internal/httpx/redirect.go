package httpx

import (
	"gocurl/internal/config"
	"strings"

	http "github.com/bogdanfinn/fhttp"
)

func normalizeRedirectHost(host string) string {
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

func redirectFunc(o *config.Options) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= o.MaxRedirs {
			return &TooManyRedirectsError{Max: o.MaxRedirs}
		}
		if len(via) == 0 {
			return nil
		}
		prev := via[len(via)-1]
		prevHost := normalizeRedirectHost(prev.URL.Hostname())
		nextHost := normalizeRedirectHost(req.URL.Hostname())
		sameHost := prevHost == nextHost
		downgrade := strings.EqualFold(prev.URL.Scheme, "https") && strings.EqualFold(req.URL.Scheme, "http")

		if !sameHost || downgrade {
			req.Header.Del("Cookie")
			req.Header.Del("Cookie2")
		}
		if !sameHost || downgrade {
			req.Header.Del("Authorization")
		}
		if o.LocationTrusted && !downgrade {
			if value := prev.Header.Get("Authorization"); value != "" && req.Header.Get("Authorization") == "" {
				req.Header.Set("Authorization", value)
				if order := prev.Header[http.HeaderOrderKey]; len(order) > 0 {
					req.Header[http.HeaderOrderKey] = append([]string(nil), order...)
				}
			}
		}
		status := 0
		if req.Response != nil {
			status = req.Response.StatusCode
		}
		preserveMethod := (status == 301 && o.Post301) ||
			(status == 302 && o.Post302) ||
			(status == 303 && o.Post303)
		if preserveMethod && prev.Method != http.MethodHead {
			req.Method = prev.Method
			req.ContentLength = prev.ContentLength
			req.GetBody = prev.GetBody
			if prev.GetBody != nil {
				body, err := prev.GetBody()
				if err == nil {
					req.Body = body
				}
			}
			if contentType := prev.Header.Get("Content-Type"); contentType != "" {
				req.Header.Set("Content-Type", contentType)
			}
		}
		return nil
	}
}
