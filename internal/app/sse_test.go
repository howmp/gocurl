package app

import (
	"bytes"
	"fmt"
	"gocurl/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type timedWriter struct {
	start time.Time
	mu    sync.Mutex
	first time.Duration
	wrote bool
	buf   bytes.Buffer
}

func (w *timedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	if !w.wrote {
		w.first = time.Since(w.start)
		w.wrote = true
	}
	w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *timedWriter) firstWrite() time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.first
}

func newSSEServer(t *testing.T, events int, interval time.Duration, hold bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for i := 0; i < events; i++ {
			fmt.Fprintf(w, "data: event-%d\n\n", i)
			if flusher != nil {
				flusher.Flush()
			}
			if interval > 0 {
				select {
				case <-time.After(interval):
				case <-r.Context().Done():
					return
				}
			}
		}
		if hold {
			<-r.Context().Done()
		}
	}))
}

func TestSSENoBufferStreams(t *testing.T) {
	srv := newSSEServer(t, 2, 200*time.Millisecond, true)
	defer srv.Close()

	out := &timedWriter{start: time.Now()}
	var stderr bytes.Buffer
	code := run([]string{"--no-persist", "-sS", "-N", "--max-time", "0.8", srv.URL}, out, &stderr)
	if code != 28 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(out.buf.String(), "data: event-0") {
		t.Fatalf("missing first event: %q", out.buf.String())
	}
	if got := out.firstWrite(); got >= 500*time.Millisecond {
		t.Fatalf("-N did not stream: first write after %v", got)
	}
}

func TestSSEDefaultBuffers(t *testing.T) {
	srv := newSSEServer(t, 2, 200*time.Millisecond, false)
	defer srv.Close()

	out := &timedWriter{start: time.Now()}
	var stderr bytes.Buffer
	code := run([]string{"--no-persist", "-sS", "--max-time", "2", srv.URL}, out, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	body := out.buf.String()
	if !strings.Contains(body, "data: event-0") || !strings.Contains(body, "data: event-1") {
		t.Fatalf("missing events: %q", body)
	}
	if got := out.firstWrite(); got < 250*time.Millisecond {
		t.Fatalf("default output was not buffered: first write after %v", got)
	}
}

func TestSSETimeoutPartialOutput(t *testing.T) {
	srv := newSSEServer(t, 100, 50*time.Millisecond, true)
	defer srv.Close()

	for _, persist := range []bool{false, true} {
		name := "direct"
		if persist {
			name = "daemon"
			setupDaemonTest(t)
		}
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr := runGocurlMode(t, persist, "-sS", "-N", "--max-time", "0.3",
				"-w", "\ncode=%{http_code} size=%{size_download}\n", srv.URL)
			if code != 28 {
				t.Fatalf("code = %d, stderr = %q", code, stderr)
			}
			if !strings.Contains(stdout, "data: event-0") {
				t.Fatalf("missing partial output: %q", stdout)
			}
			if !strings.Contains(stdout, "code=200 size=") {
				t.Fatalf("missing write-out: %q", stdout)
			}
			if strings.LastIndex(stdout, "data: event-") > strings.Index(stdout, "code=200") {
				t.Fatalf("write-out appeared before body: %q", stdout)
			}
			if !strings.Contains(stderr, "Operation timed out after 300 milliseconds with") {
				t.Fatalf("unexpected stderr: %q", stderr)
			}
		})
	}
}

func TestNoBufferOption(t *testing.T) {
	o, err := config.ParseArgs([]string{"-N", "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.NoBuffer {
		t.Fatal("-N did not set noBuffer")
	}
}

func TestTruncatedResponseMatchesCurl(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("response writer cannot hijack")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprint(conn, "HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nhello")
		conn.Close()
	}))
	defer srv.Close()

	code, stdout, stderr := runGocurl(t, "-sS", "-N", "-w", "\ncode=%{http_code} size=%{size_download}\n", srv.URL)
	if code != 18 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if stdout != "hello\ncode=200 size=5\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if !strings.Contains(stderr, "end of response with 95 bytes missing") {
		t.Fatalf("stderr = %q", stderr)
	}
}
