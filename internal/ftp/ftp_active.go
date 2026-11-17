package ftp

import (
	"crypto/tls"
	"errors"
	"fmt"
	"gocurl/internal/config"
	"gocurl/internal/transport"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	goftp "github.com/secsy/goftp"
)

type activeFTPSession struct {
	client *goftp.Client
	raw    goftp.RawConn
}

func dialActiveFTP(o *config.Options, u *url.URL, addr, dialAddr string, tlsConfig *tls.Config, explicitTLS, implicitTLS bool) (*activeFTPSession, error) {
	user, pass := ftpCredentials(o, u)
	if err := validateFTPArg(user); err != nil {
		return nil, err
	}
	if err := validateFTPArg(pass); err != nil {
		return nil, err
	}
	listenAddr, err := activeFTPListenAddr(o, dialAddr)
	if err != nil {
		return nil, err
	}
	timeout := o.ConnectTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if o.MaxTime > 0 && o.MaxTime < timeout {
		timeout = o.MaxTime
	}
	cfg := goftp.Config{
		User:               user,
		Password:           pass,
		Timeout:            timeout,
		ConnectionsPerHost: 1,
		ActiveTransfers:    true,
		ActiveListenAddr:   listenAddr,
		TLSConfig:          tlsConfig,
	}
	if implicitTLS {
		cfg.TLSMode = goftp.TLSImplicit
	} else if explicitTLS {
		cfg.TLSMode = goftp.TLSExplicit
	}
	client, err := goftp.DialConfig(cfg, dialAddr)
	if err != nil {
		return nil, err
	}
	raw, err := client.OpenRawConn()
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	session := &activeFTPSession{client: client, raw: raw}
	if err := session.expect("TYPE I", 200); err != nil {
		_ = session.Close()
		return nil, err
	}
	return session, nil
}

func activeFTPListenAddr(o *config.Options, dialAddr string) (string, error) {
	value := strings.TrimSpace(o.FtpPort)
	if value == "" {
		if ip := localIPForDial(dialAddr); ip != nil {
			return net.JoinHostPort(ip.String(), "0"), nil
		}
		return ":0", nil
	}
	if value == "-" {
		if ip := localIPForDial(dialAddr); ip != nil {
			return net.JoinHostPort(ip.String(), "0"), nil
		}
		return ":0", nil
	}
	value = strings.TrimPrefix(value, "if!")
	if ip := net.ParseIP(strings.Trim(value, "[]")); ip != nil {
		return net.JoinHostPort(ip.String(), "0"), nil
	}
	if local, err := transport.LocalTCPAddr(value); err == nil && local != nil {
		return net.JoinHostPort(local.IP.String(), "0"), nil
	}
	if host, port, err := net.SplitHostPort(value); err == nil {
		return net.JoinHostPort(host, port), nil
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		return net.JoinHostPort(strings.Trim(value, "[]"), "0"), nil
	}
	return "", fmt.Errorf("option --ftp-port: invalid listen address %q", o.FtpPort)
}

func localIPForDial(addr string) net.IP {
	conn, err := net.Dial("udp", addr)
	if err != nil {
		return nil
	}
	defer conn.Close()
	if udp, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return udp.IP
	}
	return nil
}

func (s *activeFTPSession) Close() error {
	if s == nil {
		return nil
	}
	if s.raw != nil {
		_ = s.raw.Close()
	}
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}

func (s *activeFTPSession) expect(command string, expected int) error {
	code, message, err := s.raw.SendCommand("%s", command)
	if err != nil {
		return err
	}
	if code != expected {
		return fmt.Errorf("FTP %s: %d %s", command, code, message)
	}
	return nil
}

func (s *activeFTPSession) size(path string) (int64, error) {
	code, message, err := s.raw.SendCommand("SIZE %s", path)
	if err != nil {
		return 0, err
	}
	if code != 213 {
		return 0, fmt.Errorf("FTP SIZE: %d %s", code, message)
	}
	return strconv.ParseInt(strings.TrimSpace(message), 10, 64)
}

func (s *activeFTPSession) mdtm(path string) (time.Time, error) {
	code, message, err := s.raw.SendCommand("MDTM %s", path)
	if err != nil {
		return time.Time{}, err
	}
	if code != 213 {
		return time.Time{}, fmt.Errorf("FTP MDTM: %d %s", code, message)
	}
	return time.ParseInLocation("20060102150405", strings.TrimSpace(message), time.UTC)
}

func (s *activeFTPSession) dataTransfer(command, path string, w io.Writer, r io.Reader) (int64, error) {
	getter, err := s.raw.PrepareDataConn()
	if err != nil {
		return 0, err
	}
	code, message, err := s.raw.SendCommand("%s %s", command, path)
	if err != nil {
		return 0, err
	}
	if code/100 != 1 {
		return 0, fmt.Errorf("FTP %s: %d %s", command, code, message)
	}
	conn, err := getter()
	if err != nil {
		return 0, err
	}
	var (
		n       int64
		copyErr error
	)
	if w != nil {
		n, copyErr = io.Copy(w, conn)
		copyErr = ftpWriteError(copyErr)
	} else {
		n, copyErr = io.Copy(conn, r)
	}
	closeErr := conn.Close()
	code, message, responseErr := s.raw.ReadResponse()
	if copyErr != nil {
		return n, copyErr
	}
	if closeErr != nil {
		return n, closeErr
	}
	if responseErr != nil {
		return n, responseErr
	}
	if code/100 != 2 {
		return n, fmt.Errorf("FTP %s: %d %s", command, code, message)
	}
	return n, nil
}

func (s *activeFTPSession) retrieve(path string, offset int64, w io.Writer) (int64, error) {
	if offset > 0 {
		if err := s.expect(fmt.Sprintf("REST %d", offset), 350); err != nil {
			return 0, err
		}
	}
	return s.dataTransfer("RETR", path, w, nil)
}

func (s *activeFTPSession) store(path string, offset int64, r io.Reader) error {
	if offset > 0 {
		if err := s.expect(fmt.Sprintf("REST %d", offset), 350); err != nil {
			return err
		}
	}
	_, err := s.dataTransfer("STOR", path, nil, r)
	return err
}

func (s *activeFTPSession) list(path string, names bool, w io.Writer) (int64, error) {
	command := "LIST"
	if names {
		command = "NLST"
	}
	return s.dataTransfer(command, path, w, nil)
}

func ftpResumeReader(path string, offset int64) (io.ReadCloser, error) {
	if path == "-" {
		if offset > 0 {
			return nil, errors.New("cannot resume an upload from stdin")
		}
		return io.NopCloser(os.Stdin), nil
	}
	f, err := openUploadFile(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if offset < 0 || offset > info.Size() {
		_ = f.Close()
		return nil, fmt.Errorf("resume offset %d is outside upload file size %d", offset, info.Size())
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	return f, nil
}

func runFTPActive(o *config.Options, u *url.URL, idx int, stdout, stderr io.Writer, addr, dialAddr, remotePath string, tlsConfig *tls.Config, explicitTLS, implicitTLS bool) (retErr error) {
	start := time.Now()
	session, err := dialActiveFTP(o, u, addr, dialAddr, tlsConfig, explicitTLS, implicitTLS)
	if err != nil {
		return err
	}
	defer session.Close()
	if o.Verbose {
		fmt.Fprintf(stderr, "* Connected to %s (FTP active)\n", addr)
	}
	return runFTPSession(o, u, idx, stdout, stderr, session, start, addr, remotePath)
}
