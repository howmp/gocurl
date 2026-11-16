package transport

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"gocurl/internal/config"
	"os"
	"path/filepath"
	"strings"

	tls_client "github.com/bogdanfinn/tls-client"
	utls "github.com/bogdanfinn/utls"
	"software.sslmate.com/src/go-pkcs12"
)

func LoadTransportOptions(o *config.Options) (*tls_client.TransportOptions, error) {
	if o.CertFile == "" && o.CaCert == "" && o.CaPath == "" {
		return nil, nil
	}
	opts := &tls_client.TransportOptions{}
	if o.CaCert != "" || o.CaPath != "" {
		pool, err := LoadCACertPool(o.CaCert, o.CaPath)
		if err != nil {
			return nil, err
		}
		opts.RootCAs = pool
	}
	if o.CertFile != "" {
		certs, err := loadCertificates(o)
		if err != nil {
			return nil, err
		}
		opts.Certificates = certs
	}
	return opts, nil
}

func LoadCACertPool(caCert, caPath string) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	if caCert != "" {
		info, err := os.Stat(caCert)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > 16<<20 {
			return nil, fmt.Errorf("CA file %s is not a regular file or is too large", caCert)
		}
		data, err := config.ReadFile(caCert)
		if err != nil {
			return nil, err
		}
		if !pool.AppendCertsFromPEM(data) {
			return nil, fmt.Errorf("failed to load CA certificates from %s", caCert)
		}
	}
	if caPath != "" {
		entries, err := os.ReadDir(caPath)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() || info.Size() > 8<<20 {
				continue
			}
			data, err := config.ReadFile(filepath.Join(caPath, entry.Name()))
			if err != nil {
				continue
			}
			pool.AppendCertsFromPEM(data)
		}
	}
	return pool, nil
}

func LoadTLSCertificates(o *config.Options) ([]tls.Certificate, error) {
	certs, err := loadCertificates(o)
	if err != nil {
		return nil, err
	}
	out := make([]tls.Certificate, len(certs))
	for i, cert := range certs {
		out[i] = tls.Certificate{
			Certificate: cert.Certificate,
			PrivateKey:  cert.PrivateKey,
			Leaf:        cert.Leaf,
		}
	}
	return out, nil
}

func loadCertificates(o *config.Options) ([]utls.Certificate, error) {
	certType := strings.ToUpper(o.CertType)
	if certType == "" {
		certType = "PEM"
	}
	keyType := strings.ToUpper(o.KeyType)
	if keyType == "" {
		keyType = "PEM"
	}

	if certType == "P12" || certType == "PFX" {
		data, err := config.ReadFile(o.CertFile)
		if err != nil {
			return nil, err
		}
		key, leaf, caCerts, err := pkcs12.DecodeChain(data, o.KeyPass)
		if err != nil {
			return nil, err
		}
		chain := make([][]byte, 0, len(caCerts)+1)
		chain = append(chain, leaf.Raw)
		for _, ca := range caCerts {
			chain = append(chain, ca.Raw)
		}
		return []utls.Certificate{{Certificate: chain, PrivateKey: key, Leaf: leaf}}, nil
	}

	chain, leaf, err := loadCertificateChain(o.CertFile, certType)
	if err != nil {
		return nil, err
	}
	keyFile := o.KeyFile
	if keyFile == "" {
		keyFile = o.CertFile
	}
	key, err := loadPrivateKey(keyFile, keyType, o.KeyPass)
	if err != nil {
		return nil, err
	}
	return []utls.Certificate{{Certificate: chain, PrivateKey: key, Leaf: leaf}}, nil
}

func loadCertificateChain(path, certType string) ([][]byte, *x509.Certificate, error) {
	data, err := config.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	switch certType {
	case "PEM", "PEMCERT":
		var chain [][]byte
		var leaf *x509.Certificate
		rest := data
		for {
			var block *pem.Block
			block, rest = pem.Decode(rest)
			if block == nil {
				break
			}
			if block.Type != "CERTIFICATE" {
				continue
			}
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return nil, nil, err
			}
			if leaf == nil {
				leaf = cert
			}
			chain = append(chain, block.Bytes)
		}
		if leaf == nil {
			return nil, nil, fmt.Errorf("no certificate found in %s", path)
		}
		return chain, leaf, nil
	case "DER":
		cert, err := x509.ParseCertificate(data)
		if err != nil {
			return nil, nil, err
		}
		return [][]byte{data}, cert, nil
	default:
		return nil, nil, fmt.Errorf("certificate type %q not supported", certType)
	}
}

func loadPrivateKey(path, keyType, password string) (any, error) {
	data, err := config.ReadFile(path)
	if err != nil {
		return nil, err
	}
	switch keyType {
	case "PEM":
		rest := data
		for {
			var block *pem.Block
			block, rest = pem.Decode(rest)
			if block == nil {
				break
			}
			if !strings.Contains(block.Type, "PRIVATE KEY") {
				continue
			}
			if x509.IsEncryptedPEMBlock(block) {
				if password == "" {
					return nil, fmt.Errorf("encrypted private key requires --pass")
				}
				der, err := x509.DecryptPEMBlock(block, []byte(password))
				if err != nil {
					return nil, err
				}
				block.Bytes = der
			}
			return parsePrivateKey(block.Bytes)
		}
		return nil, fmt.Errorf("no private key found in %s", path)
	case "DER":
		return parsePrivateKey(data)
	default:
		return nil, fmt.Errorf("private key type %q not supported", keyType)
	}
}

func parsePrivateKey(der []byte) (any, error) {
	if key, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		return key, nil
	}
	if key, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return key, nil
	}
	if key, err := x509.ParseECPrivateKey(der); err == nil {
		return key, nil
	}
	return nil, fmt.Errorf("failed to parse private key")
}
