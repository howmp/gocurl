package ftp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"gocurl/internal/config"
	"gocurl/internal/exitcode"
	"gocurl/internal/output"
	"gocurl/internal/transport"
	"io"
	"net"
	"net/textproto"
	"net/url"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jlaffaye/ftp"
)

func Run(o *config.Options, u *url.URL, idx int, stdout, stderr io.Writer) (retErr error) {
	if o.Netrc || o.NetrcFile != "" {
		return fmt.Errorf("option --netrc is not supported for FTP")
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = transport.DefaultPort(u.Scheme)
	}
	addr := net.JoinHostPort(host, port)
	proxyRaw, err := transport.ProxyURLFor(o, u)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if o.MaxTime > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.MaxTime)
		defer cancel()
	}
	tracker := &ftpConnTracker{}
	var timedOut atomic.Bool
	if o.MaxTime > 0 {
		timer := time.AfterFunc(o.MaxTime, func() {
			timedOut.Store(true)
			tracker.CloseAll()
		})
		defer timer.Stop()
	}
	defer func() {
		if timedOut.Load() && retErr != nil {
			retErr = &exitcode.Coded{Code: exitcode.Timeout, Msg: "operation timed out"}
		}
	}()
	dialAddr := addr
	if proxyRaw == "" {
		resolved, err := transport.ResolveDialAddr(ctx, o, addr)
		if err != nil {
			return err
		}
		dialAddr = resolved
	}

	tlsConfig, explicitTLS, implicitTLS, err := ftpTLSConfig(o, host, u.Scheme == "ftps")
	if err != nil {
		return err
	}
	remotePath := ftpRemotePath(u)
	if err := validateFTPArg(remotePath); err != nil {
		return err
	}
	if o.FtpPort != "" {
		if o.FtpPasv {
			return fmt.Errorf("option --ftp-port cannot be combined with --ftp-pasv")
		}
		if proxyRaw != "" {
			return fmt.Errorf("active FTP mode does not support a proxy")
		}
		if explicitTLS || implicitTLS {
			return fmt.Errorf("active FTP mode does not support FTPS")
		}
		if o.InterfaceName != "" {
			return fmt.Errorf("active FTP mode does not support --interface")
		}
		return runFTPActive(o, u, idx, stdout, stderr, addr, dialAddr, remotePath, tlsConfig, explicitTLS, implicitTLS)
	}

	connectTimeout := o.ConnectTimeout
	if connectTimeout <= 0 {
		connectTimeout = 30 * time.Second
	}
	dialOpts := []ftp.DialOption{
		ftp.DialWithTimeout(connectTimeout),
		ftp.DialWithContext(ctx),
	}
	localAddr, err := transport.LocalTCPAddr(o.InterfaceName)
	if err != nil {
		return err
	}
	var baseDial func(context.Context, string, string) (net.Conn, error)
	if proxyRaw != "" {
		baseDial, err = transport.NewChainProxyContext(o, proxyRaw)
		if err != nil {
			return err
		}
	} else {
		dialer := &net.Dialer{Timeout: connectTimeout, LocalAddr: localAddr}
		baseDial = dialer.DialContext
	}
	dialOpts = append(dialOpts, ftp.DialWithDialFunc(wrapFTPDial(baseDial, tracker, tlsConfig, explicitTLS, implicitTLS)))
	if explicitTLS {
		dialOpts = append(dialOpts, ftp.DialWithExplicitTLS(tlsConfig))
	} else if implicitTLS {
		dialOpts = append(dialOpts, ftp.DialWithTLS(tlsConfig))
	}

	start := time.Now()
	conn, err := ftp.Dial(dialAddr, dialOpts...)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Quit() }()

	user, pass := ftpCredentials(o, u)
	if err := validateFTPArg(user); err != nil {
		return err
	}
	if err := validateFTPArg(pass); err != nil {
		return err
	}
	if o.Verbose {
		fmt.Fprintf(stderr, "* Connected to %s (FTP)\n", addr)
		fmt.Fprintf(stderr, "> USER %s\n", user)
	}
	if err := conn.Login(user, pass); err != nil {
		return err
	}

	session := &passiveSession{conn: conn}
	return runFTPSession(o, u, idx, stdout, stderr, session, start, addr, remotePath)
}

func wrapFTPDial(base func(context.Context, string, string) (net.Conn, error), tracker *ftpConnTracker, tlsConfig *tls.Config, explicitTLS, implicitTLS bool) func(string, string) (net.Conn, error) {
	controlDial := true
	return func(network, address string) (net.Conn, error) {
		conn, err := base(context.Background(), network, address)
		if err != nil {
			return nil, err
		}
		tracker.Add(conn)
		if implicitTLS {
			return tls.Client(conn, tlsConfig), nil
		}
		if explicitTLS {
			if controlDial {
				controlDial = false
				return conn, nil
			}
			return tls.Client(conn, tlsConfig), nil
		}
		return conn, nil
	}
}

func ftpTLSConfig(o *config.Options, host string, implicit bool) (*tls.Config, bool, bool, error) {
	explicit := o.FtpSSL && !implicit
	if !implicit && !explicit {
		return nil, false, false, nil
	}
	cfg := &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: o.Insecure,
		MinVersion:         tls.VersionTLS12,
	}
	if o.CaCert != "" || o.CaPath != "" {
		pool, err := transport.LoadCACertPool(o.CaCert, o.CaPath)
		if err != nil {
			return nil, false, false, err
		}
		cfg.RootCAs = pool
	}
	if o.CertFile != "" {
		certs, err := transport.LoadTLSCertificates(o)
		if err != nil {
			return nil, false, false, err
		}
		cfg.Certificates = certs
	}
	return cfg, explicit, implicit, nil
}

func ftpRemotePath(u *url.URL) string {
	remotePath := u.Path
	if idx := strings.Index(remotePath, ";type="); idx >= 0 {
		remotePath = remotePath[:idx]
	}
	if remotePath == "" {
		remotePath = "/"
	}
	return remotePath
}

func ftpUploadTarget(remotePath, uploadFile string) string {
	target := remotePath
	if target == "" || strings.HasSuffix(target, "/") {
		name := path.Base(uploadFile)
		if uploadFile == "-" || name == "." || name == "/" {
			name = "stdin"
		}
		target = path.Join(target, name)
	}
	return target
}

func ftpUploadOffset(o *config.Options, remoteSize func() (int64, error)) (int64, error) {
	if !o.ContinueAtSet {
		return 0, nil
	}
	if o.ResumeOffset >= 0 {
		return o.ResumeOffset, nil
	}
	if o.UploadFile == "-" {
		return 0, fmt.Errorf("option --continue-at: cannot resume an upload from stdin")
	}
	size, err := remoteSize()
	if err != nil {
		var protoErr *textproto.Error
		if errors.As(err, &protoErr) && protoErr.Code == ftp.StatusFileUnavailable {
			return 0, nil
		}
		return 0, fmt.Errorf("option --continue-at: cannot determine remote file size: %w", err)
	}
	return size, nil
}

func ftpDownloadResume(o *config.Options, u *url.URL, idx int) (int64, *config.Options, error) {
	if !o.ContinueAtSet {
		return 0, o, nil
	}
	offset, err := output.ResolveResumeOffset(o, u, idx)
	if err != nil {
		return 0, nil, err
	}
	if offset <= 0 {
		return 0, o, nil
	}
	local, err := output.ResumeOutput(o, u, idx, offset, true)
	if err != nil {
		return 0, nil, err
	}
	return offset, local, nil
}

func validateFTPArg(value string) error {
	if strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("invalid FTP command argument")
	}
	return nil
}

func ftpCredentials(o *config.Options, u *url.URL) (string, string) {
	if u.User != nil {
		user := u.User.Username()
		pass, _ := u.User.Password()
		return user, pass
	}
	if o.User != "" {
		user, pass, _ := strings.Cut(o.User, ":")
		return user, pass
	}
	return "anonymous", "anonymous@"
}

type ftpConnTracker struct {
	mu    sync.Mutex
	conns []net.Conn
}

func (t *ftpConnTracker) Add(conn net.Conn) {
	t.mu.Lock()
	t.conns = append(t.conns, conn)
	t.mu.Unlock()
}

func (t *ftpConnTracker) CloseAll() {
	t.mu.Lock()
	conns := append([]net.Conn(nil), t.conns...)
	t.conns = nil
	t.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}
