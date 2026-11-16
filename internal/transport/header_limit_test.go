package transport

import (
	"bufio"
	"io"
	stdhttp "net/http"
	"strings"
	"testing"
)

type chunkReader struct {
	data []byte
	size int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := r.size
	if n > len(r.data) {
		n = len(r.data)
	}
	if n > len(p) {
		n = len(p)
	}
	copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}

func TestResponseHeaderLimitAllowsBody(t *testing.T) {
	raw := "HTTP/1.1 200 OK\r\nContent-Length: 4\r\n\r\nbody"
	reader := bufio.NewReader(LimitResponseHeaderReader(&chunkReader{data: []byte(raw), size: 7}))
	resp, err := stdhttp.ReadResponse(reader, &stdhttp.Request{Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "body" {
		t.Fatalf("body=%q err=%v", body, err)
	}
}

func TestResponseHeaderLimitRejectsOversizedHeaders(t *testing.T) {
	raw := "HTTP/1.1 200 OK\r\nX-Fill: " + strings.Repeat("a", MaxResponseHeaderBytes) + "\r\n\r\n"
	reader := bufio.NewReader(LimitResponseHeaderReader(strings.NewReader(raw)))
	if _, err := stdhttp.ReadResponse(reader, &stdhttp.Request{Method: "GET"}); err == nil {
		t.Fatal("expected oversized header error")
	}
}
