package websocket

import (
	"errors"
	"net/url"
	"testing"

	"gocurl/internal/config"
	"gocurl/internal/exitcode"
)

func TestWebsocketHeaders(t *testing.T) {
	o := config.NewOptions()
	o.Headers = []config.HeaderArg{{Name: "X-Test", Value: "yes"}}
	u, _ := url.Parse("wss://example.test/socket")
	h := websocketHeaders(o, u)
	if h.Get("X-Test") != "yes" {
		t.Fatalf("custom header missing: %v", h)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestWriteWSMessageReportsWriteErrors(t *testing.T) {
	if _, err := writeWSMessage(failingWriter{}, []byte("data"), true); err == nil {
		t.Fatal("expected write error")
	} else if exitcode.From(err) != 23 {
		t.Fatalf("exit code = %d, want 23", exitcode.From(err))
	}
}

func TestWriteWSMessageCountsNewline(t *testing.T) {
	var buf capWriter
	n, err := writeWSMessage(&buf, []byte("data"), true)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 || string(buf) != "data\n" {
		t.Fatalf("n=%d buf=%q", n, buf)
	}
}

type capWriter []byte

func (w *capWriter) Write(p []byte) (int, error) {
	*w = append(*w, p...)
	return len(p), nil
}
