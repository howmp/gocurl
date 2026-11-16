package daemon

import (
	"time"

	"gocurl/internal/config"
)

func transportFromOptions(dst *daemonTransport, o *config.Options) {
	*dst = daemonTransport{
		HTTPVersion:      o.HttpVersion,
		Insecure:         o.Insecure,
		Proxy:            o.Proxy,
		ProxyUser:        o.ProxyUser,
		NoProxy:          o.NoProxy,
		IPv4:             o.Ipv4,
		IPv6:             o.Ipv6,
		ConnectTimeoutMS: o.ConnectTimeout.Milliseconds(),
		Location:         o.Location,
		LocationTrusted:  o.LocationTrusted,
		Post301:          o.Post301,
		Post302:          o.Post302,
		Post303:          o.Post303,
		MaxRedirs:        o.MaxRedirs,
		MaxTimeMS:        o.MaxTime.Milliseconds(),
		CertFile:         o.CertFile,
		KeyFile:          o.KeyFile,
		CertType:         o.CertType,
		KeyType:          o.KeyType,
		KeyPass:          o.KeyPass,
		CACert:           o.CaCert,
		CAPath:           o.CaPath,
		Resolve:          o.Resolves,
		ConnectTo:        o.ConnectTo,
		UnixSocket:       o.UnixSocket,
		AbstractUnix:     o.AbstractUnix,
		Interface:        o.InterfaceName,
		ProxyInsecure:    o.ProxyInsecure,
		PreProxy:         o.PreProxy,
		DnsServers:       o.DnsServers,
		DohURL:           o.DohURL,
		DohInsecure:      o.DohInsecure,
	}
}

func optionsFromTransport(t daemonTransport) *config.Options {
	o := config.NewOptions()
	o.HttpVersion = t.HTTPVersion
	if o.HttpVersion == "" {
		o.HttpVersion = "auto"
	}
	o.Insecure = t.Insecure
	o.Proxy = t.Proxy
	o.ProxyUser = t.ProxyUser
	o.NoProxy = t.NoProxy
	o.Ipv4 = t.IPv4
	o.Ipv6 = t.IPv6
	o.ConnectTimeout = time.Duration(t.ConnectTimeoutMS) * time.Millisecond
	o.Location = t.Location
	o.LocationTrusted = t.LocationTrusted
	o.Post301 = t.Post301
	o.Post302 = t.Post302
	o.Post303 = t.Post303
	o.MaxRedirs = t.MaxRedirs
	o.MaxTime = time.Duration(t.MaxTimeMS) * time.Millisecond
	o.CertFile = t.CertFile
	o.KeyFile = t.KeyFile
	o.CertType = t.CertType
	o.KeyType = t.KeyType
	o.KeyPass = t.KeyPass
	o.CaCert = t.CACert
	o.CaPath = t.CAPath
	if t.Resolve != nil {
		o.Resolves = t.Resolve
	}
	if t.ConnectTo != nil {
		o.ConnectTo = t.ConnectTo
	}
	o.UnixSocket = t.UnixSocket
	o.AbstractUnix = t.AbstractUnix
	o.InterfaceName = t.Interface
	o.ProxyInsecure = t.ProxyInsecure
	o.PreProxy = t.PreProxy
	o.DnsServers = t.DnsServers
	o.DohURL = t.DohURL
	o.DohInsecure = t.DohInsecure
	for name, values := range t.ProxyHeaders {
		for _, value := range values {
			o.ProxyHeaders = append(o.ProxyHeaders, config.HeaderArg{Name: name, Value: value})
		}
	}
	return o
}
