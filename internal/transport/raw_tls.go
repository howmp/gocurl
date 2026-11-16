package transport

import (
	"gocurl/internal/config"

	utls "github.com/bogdanfinn/utls"
)

func RawTLSConfig(o *config.Options, host string) (*utls.Config, error) {
	cfg := &utls.Config{
		ServerName:         host,
		InsecureSkipVerify: o.Insecure,
		NextProtos:         []string{"http/1.1"},
		MinVersion:         utls.VersionTLS12,
		MaxVersion:         utls.VersionTLS13,
	}
	if o.CaCert != "" || o.CaPath != "" {
		pool, err := LoadCACertPool(o.CaCert, o.CaPath)
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = pool
	}
	if o.CertFile != "" {
		certs, err := loadCertificates(o)
		if err != nil {
			return nil, err
		}
		cfg.Certificates = certs
	}
	return cfg, nil
}
