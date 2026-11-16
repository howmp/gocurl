package daemon

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadRequestBody(t *testing.T) {
	br := bufio.NewReader(strings.NewReader("hello"))
	body, err := readRequestBody(br, 5)
	if err != nil || string(body) != "hello" {
		t.Fatalf("body=%q err=%v", body, err)
	}
	if body, err := readRequestBody(br, 0); err != nil || body != nil {
		t.Fatalf("zero body = %q, err=%v", body, err)
	}
}

func TestReadRequestBodyRejectsShortRead(t *testing.T) {
	br := bufio.NewReader(bytes.NewReader([]byte("short")))
	if _, err := readRequestBody(br, 1<<20); err == nil {
		t.Fatal("expected short body error")
	}
}

func TestReadRequestBodyRejectsOversizeAndNegative(t *testing.T) {
	br := bufio.NewReader(strings.NewReader(""))
	if _, err := readRequestBody(br, maxDaemonRequestBody+1); err == nil {
		t.Fatal("expected oversize body error")
	}
	if _, err := readRequestBody(br, -1); err == nil {
		t.Fatal("expected negative body error")
	}
}

func TestDaemonBodyDetectsTruncation(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	go func() {
		_, _ = server.Write([]byte("0123456789"))
		_ = server.Close()
	}()

	body := &daemonBody{reader: io.LimitReader(client, 100), conn: client, remaining: 100}
	data, err := io.ReadAll(body)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want ErrUnexpectedEOF", err)
	}
	if len(data) != 10 {
		t.Fatalf("read %d bytes, want 10", len(data))
	}
}

func TestDaemonBodyEmptyResponse(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	defer server.Close()

	body := &daemonBody{reader: client, conn: client, remaining: 0}
	if _, err := body.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want EOF", err)
	}
}

func TestCheckDaemonBodySize(t *testing.T) {
	if err := checkDaemonBodySize(maxRequestBodySize); err != nil {
		t.Fatalf("limit rejected: %v", err)
	}
	if err := checkDaemonBodySize(maxRequestBodySize + 1); !errors.Is(err, ErrRequestBodyTooLarge) {
		t.Fatalf("err = %v, want ErrRequestBodyTooLarge", err)
	}
}

func TestRedirectResponseChain(t *testing.T) {
	chain := redirectResponseChain([]string{"https://one.test/", "https://two.test/", "https://three.test/"})
	if chain == nil || chain.Request == nil || chain.Request.URL == nil {
		t.Fatal("missing redirect chain")
	}
	if got := chain.Request.URL.String(); got != "https://three.test/" {
		t.Fatalf("last redirect URL = %q", got)
	}
}

func TestValidateDaemonResponse(t *testing.T) {
	valid := []daemonResponse{
		{BodyLength: 0},
		{BodyLength: 10},
		{BodyLength: -1, BodyChunked: true},
		{BodyLength: 5, Headers: map[string][]string{"content-type": {"text/plain"}}, Redirects: []string{"https://example.test/"}},
	}
	for _, resp := range valid {
		if err := validateDaemonResponse(&resp); err != nil {
			t.Fatalf("valid response rejected: %+v: %v", resp, err)
		}
	}
	invalid := []daemonResponse{
		{BodyLength: -2},
		{BodyLength: -1},
		{BodyLength: 0, BodyChunked: true},
		{BodyLength: 1, Headers: map[string][]string{"bad header": {"x"}}},
		{BodyLength: 1, Headers: map[string][]string{"x": {"bad\r\nvalue"}}},
		{BodyLength: 1, Redirects: []string{"file:///etc/passwd"}},
		{BodyLength: 1, Redirects: make([]string, maxDaemonRedirects+1)},
	}
	for _, resp := range invalid {
		if err := validateDaemonResponse(&resp); err == nil {
			t.Fatalf("invalid response accepted: %+v", resp)
		}
	}
}

func TestDaemonBodyRejectsNegativeRemaining(t *testing.T) {
	body := &daemonBody{reader: strings.NewReader(""), remaining: -1}
	if _, err := body.Read(make([]byte, 1)); err == nil {
		t.Fatal("expected invalid body length error")
	}
}

func dumpCookiesServer(t *testing.T, resp daemonResponse, body string) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "daemon.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var req daemonRequest
		if err := readDaemonFrame(bufio.NewReader(conn), &req); err != nil {
			return
		}
		bw := bufio.NewWriter(conn)
		if err := writeDaemonFrame(bw, resp); err != nil {
			return
		}
		_, _ = bw.WriteString(body)
		_ = bw.Flush()
	}()
	return socket
}

func TestDumpCookiesBoundsResponse(t *testing.T) {
	socket := dumpCookiesServer(t, daemonResponse{
		Version:    daemonProtocolVersion,
		BodyLength: maxDumpCookiesBytes + 1,
	}, "")
	if _, err := DumpCookies(socket, nil); err == nil {
		t.Fatal("expected oversized cookie dump error")
	}
}

func TestDumpCookiesReadsDeclaredLength(t *testing.T) {
	socket := dumpCookiesServer(t, daemonResponse{
		Version:    daemonProtocolVersion,
		BodyLength: 5,
	}, "helloEXTRA")
	got, err := DumpCookies(socket, nil)
	if err != nil || got != "hello" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestDaemonProtocolFrame(t *testing.T) {
	var buf bytes.Buffer
	bw := bufio.NewWriter(&buf)
	want := daemonRequest{
		Version: daemonProtocolVersion,
		Op:      daemonOpRequest,
		URL:     "https://example.com/",
		Headers: map[string][]string{"accept": {"text/html"}},
	}
	if err := writeDaemonFrame(bw, want); err != nil {
		t.Fatal(err)
	}
	if err := bw.Flush(); err != nil {
		t.Fatal(err)
	}
	var got daemonRequest
	if err := readDaemonFrame(bufio.NewReader(&buf), &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != want.Version || got.Op != want.Op || got.URL != want.URL || got.Headers["accept"][0] != "text/html" {
		t.Fatalf("got %+v", got)
	}
}

func TestChunkedBodyRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := writeChunkedBody(&buf, bytes.NewReader([]byte("hello world"))); err != nil {
		t.Fatal(err)
	}
	body := &daemonBody{reader: bufio.NewReader(&buf), chunked: true}
	data, err := io.ReadAll(body)
	if err != nil || string(data) != "hello world" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

func TestChunkedBodyDetectsErrorFrame(t *testing.T) {
	var buf bytes.Buffer
	if err := writeDaemonChunk(&buf, []byte("partial")); err != nil {
		t.Fatal(err)
	}
	if err := writeDaemonChunkError(&buf, io.ErrUnexpectedEOF); err != nil {
		t.Fatal(err)
	}
	body := &daemonBody{reader: bufio.NewReader(&buf), chunked: true}
	data, err := io.ReadAll(body)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err=%v, want ErrUnexpectedEOF", err)
	}
	if string(data) != "partial" {
		t.Fatalf("data=%q", data)
	}
}

func TestChunkedBodyRejectsOversizeChunk(t *testing.T) {
	var buf bytes.Buffer
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], maxDaemonChunk+1)
	buf.Write(header[:])
	body := &daemonBody{reader: bufio.NewReader(&buf), chunked: true}
	if _, err := body.Read(make([]byte, 16)); err == nil {
		t.Fatal("expected oversized chunk error")
	}
}

func TestChunkedBodyTruncatedHeader(t *testing.T) {
	body := &daemonBody{reader: bufio.NewReader(bytes.NewReader([]byte{0, 0})), chunked: true}
	if _, err := body.Read(make([]byte, 16)); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err=%v, want ErrUnexpectedEOF", err)
	}
}

func TestValidateDaemonRequest(t *testing.T) {
	valid := &daemonRequest{Method: "GET", URL: "https://example.test/", Headers: map[string][]string{"accept": {"*/*"}}}
	if err := validateDaemonRequest(valid); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	for _, req := range []*daemonRequest{
		{Method: "GET\r\nX: 1"},
		{Method: "GET", RequestTarget: "/a HTTP/1.1"},
		{Method: "GET", URL: "file:///etc/passwd"},
		{Method: "GET", URL: "http://bad host/"},
		{Method: "GET", URL: "https://example.test/", Host: "bad\r\nHost"},
		{Method: "GET", Headers: map[string][]string{"bad name": {"x"}}},
		{Method: "GET", Headers: map[string][]string{"X-Test": {"a\r\nb"}}},
	} {
		if err := validateDaemonRequest(req); err == nil {
			t.Fatalf("invalid request accepted: %+v", req)
		}
	}
}
