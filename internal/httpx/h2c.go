package httpx

import (
	"context"
	"gocurl/internal/config"
	"gocurl/internal/transport"
	"net"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	h2 "github.com/bogdanfinn/fhttp/http2"
	"github.com/bogdanfinn/tls-client/profiles"
	utls "github.com/bogdanfinn/utls"
)

func newH2CClient(o *config.Options) (*fhttp.Client, error) {
	profile := profiles.Chrome_152
	transport := &h2.Transport{
		AllowHTTP:          true,
		Settings:           profile.GetSettings(),
		SettingsOrder:      profile.GetSettingsOrder(),
		PseudoHeaderOrder:  profile.GetPseudoHeaderOrder(),
		ConnectionFlow:     profile.GetConnectionFlow(),
		InitialStreamID:    profile.GetStreamID(),
		HeaderPriority:     profile.GetHeaderPriority(),
		DisableCompression: true,
		DialTLS: func(network, addr string, _ *utls.Config) (net.Conn, error) {
			ctx := context.Background()
			if o.MaxTime > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, o.MaxTime)
				defer cancel()
			}
			if o.UnixSocket != "" {
				return transport.DialUnixContext(o.UnixSocket, o.AbstractUnix)(ctx, network, addr)
			}
			return transport.DialHTTPContext(o, ctx, network, addr)
		},
	}
	client := &fhttp.Client{
		Transport: transport,
		Jar:       o.CookieJar,
		Timeout:   24 * time.Hour,
	}
	if !o.Location {
		client.CheckRedirect = func(*fhttp.Request, []*fhttp.Request) error {
			return fhttp.ErrUseLastResponse
		}
	} else {
		client.CheckRedirect = redirectFunc(o)
	}
	return client, nil
}
