package app

import (
	"bytes"
	"crypto/tls"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	fhttptest "github.com/bogdanfinn/fhttp/httptest"
	"github.com/bogdanfinn/websocket"
	ftpserver "github.com/fclairamb/ftpserverlib"
	"github.com/spf13/afero"
)

func TestWebSocketEcho(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := fhttptest.NewServer(fhttp.HandlerFunc(func(w fhttp.ResponseWriter, r *fhttp.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(mt, data); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/echo"
	code, out, stderr := runGocurl(t, "-sS", "-d", "hello", wsURL)
	if code != 0 || out != "hello\n" {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestWebSocketCompressedEcho(t *testing.T) {
	upgrader := websocket.Upgrader{EnableCompression: true}
	srv := fhttptest.NewServer(fhttp.HandlerFunc(func(w fhttp.ResponseWriter, r *fhttp.Request) {
		if !strings.Contains(r.Header.Get("Sec-WebSocket-Extensions"), "permessage-deflate") {
			fhttp.Error(w, "compression not offered", fhttp.StatusBadRequest)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(mt, data); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/echo"
	code, out, stderr := runGocurl(t, "-sS", "--compressed", "-d", "hello", wsURL)
	if code != 0 || out != "hello\n" {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestWebSocketCompressedMessageTooLarge(t *testing.T) {
	payload := bytes.Repeat([]byte("A"), 4<<20)
	upgrader := websocket.Upgrader{EnableCompression: true}
	srv := fhttptest.NewServer(fhttp.HandlerFunc(func(w fhttp.ResponseWriter, r *fhttp.Request) {
		if !strings.Contains(r.Header.Get("Sec-WebSocket-Extensions"), "permessage-deflate") {
			fhttp.Error(w, "compression not offered", fhttp.StatusBadRequest)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteMessage(websocket.BinaryMessage, payload)
		_, _, _ = conn.ReadMessage()
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/bomb"
	code, _, stderr := runGocurl(t, "-sS", "--compressed", "--max-filesize", "1M", "-d", "seed", wsURL)
	if code != 63 || !strings.Contains(stderr, "maximum size") {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
}

func TestWebSocketCumulativeMaxFilesize(t *testing.T) {
	payload := bytes.Repeat([]byte("A"), 600<<10)
	upgrader := websocket.Upgrader{EnableCompression: true}
	srv := fhttptest.NewServer(fhttp.HandlerFunc(func(w fhttp.ResponseWriter, r *fhttp.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for i := 0; i < 2; i++ {
			if err := conn.WriteMessage(websocket.BinaryMessage, payload); err != nil {
				return
			}
		}
		_, _, _ = conn.ReadMessage()
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/stream"
	code, out, stderr := runGocurl(t, "-sS", "--compressed", "--max-filesize", "1M", "--max-time", "5", "-d", "seed", wsURL)
	if code != 63 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if len(out) > 1<<20 {
		t.Fatalf("output length = %d, want <= 1 MiB", len(out))
	}
}

func TestWebSocketResolve(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := fhttptest.NewServer(fhttp.HandlerFunc(func(w fhttp.ResponseWriter, r *fhttp.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(mt, data); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port := u.Port()
	wsURL := "ws://example.test:" + port + "/echo"
	code, out, stderr := runGocurl(t, "-sS",
		"--resolve", "example.test:"+port+":127.0.0.1",
		"-d", "resolved", wsURL)
	if code != 0 || out != "resolved\n" {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestWebSocketTLSEcho(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := fhttptest.NewTLSServer(fhttp.HandlerFunc(func(w fhttp.ResponseWriter, r *fhttp.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(mt, data); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	wssURL := "wss" + strings.TrimPrefix(srv.URL, "https") + "/echo"
	code, out, stderr := runGocurl(t, "-sS", "-k", "-d", "secure", wssURL)
	if code != 0 || out != "secure\n" {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestWebSocketProxy(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := fhttptest.NewServer(fhttp.HandlerFunc(func(w fhttp.ResponseWriter, r *fhttp.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(mt, data); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	proxySrv, seen, _ := newConnectProxy(t)
	defer proxySrv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/echo"
	code, out, stderr := runGocurl(t, "-sS", "-x", proxySrv.URL, "-d", "proxied", wsURL)
	if code != 0 || out != "proxied\n" {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
	if !seen.Load() {
		t.Fatal("proxy was not used")
	}
}

func TestWebSocketTLSProxy(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := fhttptest.NewTLSServer(fhttp.HandlerFunc(func(w fhttp.ResponseWriter, r *fhttp.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(mt, data); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	proxySrv, seen, _ := newConnectProxy(t)
	defer proxySrv.Close()

	wssURL := "wss" + strings.TrimPrefix(srv.URL, "https") + "/echo"
	code, out, stderr := runGocurl(t, "-sS", "-k", "-x", proxySrv.URL, "-d", "secure-proxied", wssURL)
	if code != 0 || out != "secure-proxied\n" {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
	if !seen.Load() {
		t.Fatal("proxy was not used")
	}
}

type testFTPDriver struct {
	fs  afero.Fs
	tls *tls.Config
}

func (d *testFTPDriver) GetSettings() (*ftpserver.Settings, error) {
	return &ftpserver.Settings{
		ListenAddr:              "127.0.0.1:0",
		Banner:                  "gocurl-test",
		PublicHost:              "127.0.0.1",
		ActiveTransferPortNon20: true,
	}, nil
}

func (d *testFTPDriver) ClientConnected(ftpserver.ClientContext) (string, error) {
	return "gocurl-test", nil
}

func (d *testFTPDriver) ClientDisconnected(ftpserver.ClientContext) {}

func (d *testFTPDriver) AuthUser(_ ftpserver.ClientContext, user, pass string) (ftpserver.ClientDriver, error) {
	if user == "test" && pass == "test" {
		return d.fs, nil
	}
	return nil, os.ErrPermission
}

func (d *testFTPDriver) GetTLSConfig() (*tls.Config, error) {
	return d.tls, nil
}

func startTestFTPServer(t *testing.T) string {
	t.Helper()
	return startTestFTPServerWithTLS(t, nil)
}

func startTestFTPServerWithTLS(t *testing.T, tlsConfig *tls.Config) string {
	t.Helper()
	fs := afero.NewMemMapFs()
	if err := afero.WriteFile(fs, "/hello.txt", []byte("ftp-body"), 0o644); err != nil {
		t.Fatal(err)
	}
	driver := &testFTPDriver{fs: fs, tls: tlsConfig}
	srv := ftpserver.NewFtpServer(driver)
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { _ = srv.Stop() })
	return srv.Addr()
}

func TestFTPDownloadThroughProxy(t *testing.T) {
	addr := startTestFTPServer(t)
	proxySrv, seen, _ := newConnectProxy(t)
	defer proxySrv.Close()
	code, out, stderr := runGocurl(t, "-sS", "-x", proxySrv.URL,
		"ftp://test:test@"+addr+"/hello.txt")
	if code != 0 || out != "ftp-body" {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
	if !seen.Load() {
		t.Fatal("proxy was not used")
	}
}

func TestFTPDownloadListUpload(t *testing.T) {
	addr := startTestFTPServer(t)
	base := "ftp://test:test@" + addr

	code, out, stderr := runGocurl(t, "-sS", base+"/hello.txt")
	if code != 0 || out != "ftp-body" {
		t.Fatalf("download exit=%d out=%q stderr=%q", code, out, stderr)
	}

	code, out, stderr = runGocurl(t, "-sS", base+"/")
	if code != 0 || !strings.Contains(out, "hello.txt") {
		t.Fatalf("list exit=%d out=%q stderr=%q", code, out, stderr)
	}

	code, out, stderr = runGocurl(t, "-sS", "-l", base+"/")
	if code != 0 || !strings.Contains(out, "hello.txt") {
		t.Fatalf("name list exit=%d out=%q stderr=%q", code, out, stderr)
	}

	local := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(local, []byte("upload-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runGocurl(t, "-sS", "-T", local, base+"/upload.txt")
	if code != 0 {
		t.Fatalf("upload exit=%d stderr=%q", code, stderr)
	}
	code, out, stderr = runGocurl(t, "-sS", base+"/upload.txt")
	if code != 0 || out != "upload-data" {
		t.Fatalf("uploaded download exit=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestFTPActiveMode(t *testing.T) {
	addr := startTestFTPServer(t)
	base := "ftp://test:test@" + addr

	code, out, stderr := runGocurl(t, "-sS", "-P", "-", base+"/hello.txt")
	if code != 0 || out != "ftp-body" {
		t.Fatalf("active download exit=%d out=%q stderr=%q", code, out, stderr)
	}
	code, out, stderr = runGocurl(t, "-sS", "-P", "-", base+"/")
	if code != 0 || !strings.Contains(out, "hello.txt") {
		t.Fatalf("active list exit=%d out=%q stderr=%q", code, out, stderr)
	}

	local := filepath.Join(t.TempDir(), "active-upload.txt")
	if err := os.WriteFile(local, []byte("active-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runGocurl(t, "-sS", "-P", "-", "-T", local, base+"/active.txt")
	if code != 0 {
		t.Fatalf("active upload exit=%d stderr=%q", code, stderr)
	}
	code, out, stderr = runGocurl(t, "-sS", "-P", "-", base+"/active.txt")
	if code != 0 || out != "active-data" {
		t.Fatalf("active uploaded download exit=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestFTPExplicitTLS(t *testing.T) {
	certPEM, keyPEM := makeClientCert(t)
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	addr := startTestFTPServerWithTLS(t, &tls.Config{Certificates: []tls.Certificate{cert}})
	base := "ftp://test:test@" + addr

	code, out, stderr := runGocurl(t, "-sS", "-k", "--ftp-ssl", base+"/hello.txt")
	if code != 0 || out != "ftp-body" {
		t.Fatalf("explicit FTPS exit=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestFTPResume(t *testing.T) {
	addr := startTestFTPServer(t)
	base := "ftp://test:test@" + addr

	outPath := filepath.Join(t.TempDir(), "resume.txt")
	if err := os.WriteFile(outPath, []byte("ftp"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runGocurl(t, "-sS", "-C", "-", "-o", outPath, base+"/hello.txt")
	if code != 0 {
		t.Fatalf("resume download exit=%d stderr=%q", code, stderr)
	}
	data, err := os.ReadFile(outPath)
	if err != nil || string(data) != "ftp-body" {
		t.Fatalf("resumed file=%q err=%v", data, err)
	}
	activePath := filepath.Join(t.TempDir(), "active-resume.txt")
	if err := os.WriteFile(activePath, []byte("ftp"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runGocurl(t, "-sS", "-P", "-", "-C", "-", "-o", activePath, base+"/hello.txt")
	if code != 0 {
		t.Fatalf("active resume download exit=%d stderr=%q", code, stderr)
	}
	activeData, err := os.ReadFile(activePath)
	if err != nil || string(activeData) != "ftp-body" {
		t.Fatalf("active resumed file=%q err=%v", activeData, err)
	}

	local := filepath.Join(t.TempDir(), "upload.txt")
	prefix := filepath.Join(t.TempDir(), "upload-prefix.txt")
	if err := os.WriteFile(prefix, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runGocurl(t, "-sS", "-T", prefix, base+"/upload-resume.txt")
	if code != 0 {
		t.Fatalf("initial upload exit=%d stderr=%q", code, stderr)
	}
	if err := os.WriteFile(local, []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runGocurl(t, "-sS", "-C", "-", "-T", local, base+"/upload-resume.txt")
	if code != 0 {
		t.Fatalf("resume upload exit=%d stderr=%q", code, stderr)
	}
	code, out, stderr := runGocurl(t, "-sS", base+"/upload-resume.txt")
	if code != 0 || out != "abcdef" {
		t.Fatalf("resumed upload content=%q stderr=%q", out, stderr)
	}
}

func TestFTPSDownload(t *testing.T) {
	certPEM, keyPEM := makeClientCert(t)
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}}
	rawLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tlsLn := tls.NewListener(rawLn, tlsConfig)

	fs := afero.NewMemMapFs()
	if err := afero.WriteFile(fs, "/secure.txt", []byte("ftps-body"), 0o644); err != nil {
		t.Fatal(err)
	}
	driver := &testFTPDriver{fs: fs, tls: tlsConfig}
	srv := ftpserver.NewFtpServer(driver)
	settings, err := driver.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	settings.Listener = tlsLn
	driverWithListener := &listenerFTPDriver{testFTPDriver: driver, settings: settings}
	srv = ftpserver.NewFtpServer(driverWithListener)
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	defer srv.Stop()

	addr := srv.Addr()
	u, _ := url.Parse("ftps://test:test@" + addr + "/secure.txt")
	code, out, stderr := runGocurl(t, "-sS", "-k", u.String())
	if code != 0 || out != "ftps-body" {
		t.Fatalf("ftps exit=%d out=%q stderr=%q", code, out, stderr)
	}
	proxySrv, seen, _ := newConnectProxy(t)
	defer proxySrv.Close()
	code, out, stderr = runGocurl(t, "-sS", "-k", "-x", proxySrv.URL, u.String())
	if code != 0 || out != "ftps-body" {
		t.Fatalf("ftps proxy exit=%d out=%q stderr=%q", code, out, stderr)
	}
	if !seen.Load() {
		t.Fatal("proxy was not used")
	}
}

type listenerFTPDriver struct {
	*testFTPDriver
	settings *ftpserver.Settings
}

func (d *listenerFTPDriver) GetSettings() (*ftpserver.Settings, error) {
	return d.settings, nil
}

func TestWebSocketTimeout(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := fhttptest.NewServer(fhttp.HandlerFunc(func(w fhttp.ResponseWriter, r *fhttp.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/silent"
	code, out, stderr := runGocurl(t, "-sS", "--max-time", "0.2", wsURL)
	if code != 28 || out != "" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
}
