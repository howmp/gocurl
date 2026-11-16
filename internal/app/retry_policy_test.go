package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseRetryAfterClamps(t *testing.T) {
	cases := []struct {
		value string
		want  time.Duration
		ok    bool
	}{
		{"1", time.Second, true},
		{"9223372037", time.Minute, true},
		{"-1", 0, false},
		{"", 0, false},
		{time.Now().Add(2 * time.Minute).UTC().Format(http.TimeFormat), time.Minute, true},
	}
	for _, tc := range cases {
		got, ok := parseRetryAfter(tc.value)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("parseRetryAfter(%q) = %v, %v; want %v, %v", tc.value, got, ok, tc.want, tc.ok)
		}
	}
}

func TestWaitRetryRejectsNegativeDelay(t *testing.T) {
	if err := waitRetry(nil, -time.Second); err == nil {
		t.Fatal("expected negative delay error")
	}
}

func TestRetryIdempotentBlocksPost(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, "busy")
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sS", "--retry", "2", "--retry-delay", "0.01",
		"--retry-idempotent", "-d", "payload", srv.URL)
	if code != 0 || out != "busy" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("attempts=%d, want 1", got)
	}
}

func TestRetryIdempotentAllowsGetAndCountsRetries(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sS", "--retry", "1", "--retry-delay", "0.01",
		"--retry-idempotent", "-w", "%{num_retries}", srv.URL)
	if code != 0 || out != "ok1" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("attempts=%d, want 2", got)
	}
}

func TestRetryMaxPerHost(t *testing.T) {
	var one, two int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var count *int32
		switch r.URL.Path {
		case "/one":
			count = &one
		case "/two":
			count = &two
		default:
			http.NotFound(w, r)
			return
		}
		if atomic.AddInt32(count, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, "busy")
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	code, out, stderr := runGocurl(t, "-sS", "--retry", "1", "--retry-delay", "0.01",
		"--retry-idempotent", "--retry-max-per-host", "1",
		srv.URL+"/one", srv.URL+"/two")
	if code != 0 || out != "okbusy" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if got := atomic.LoadInt32(&one); got != 2 {
		t.Fatalf("first host attempts=%d, want 2", got)
	}
	if got := atomic.LoadInt32(&two); got != 1 {
		t.Fatalf("second host attempts=%d, want 1", got)
	}
}
