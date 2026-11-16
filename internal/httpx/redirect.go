package httpx

import (
	"net/url"
	"strings"

	"gocurl/internal/config"

	http "github.com/bogdanfinn/fhttp"
)

func normalizeRedirectHost(host string) string {
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

func redirectFunc(o *config.Options) func(req *http.Request, via []*http.Request) error {
	return redirectFuncWithRecorder(o, nil)
}

func redirectFuncWithRecorder(o *config.Options, record func(*url.URL)) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= o.MaxRedirs {
			return &TooManyRedirectsError{Max: o.MaxRedirs}
		}
		if len(via) == 0 {
			return nil
		}
		prev := via[len(via)-1]
		prevScheme := strings.ToLower(prev.URL.Scheme)
		nextScheme := strings.ToLower(req.URL.Scheme)
		downgrade := (prevScheme == "https" && nextScheme == "http") ||
			(prevScheme == "wss" && nextScheme == "ws")
		crossOrigin := !SameOriginURL(prev.URL, req.URL)

		if crossOrigin || downgrade {
			req.Header.Del("Cookie")
			req.Header.Del("Cookie2")
			req.Header.Del("Authorization")
			req.Header.Del("Proxy-Authorization")
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
		if record != nil {
			record(req.URL)
		}
		return nil
	}
}
