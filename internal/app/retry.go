package app

import (
	"context"
	"errors"
	"fmt"
	"gocurl/internal/config"
	"gocurl/internal/exitcode"
	"gocurl/internal/output"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
)

func retryHost(u *url.URL) string {
	if u == nil {
		return ""
	}
	host := u.Hostname()
	if host == "" {
		host = u.Host
	}
	return strings.ToLower(host)
}

func isIdempotentMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodPut,
		http.MethodDelete, http.MethodOptions, http.MethodTrace:
		return true
	}
	return false
}

func allowRetry(o *config.Options, method, host string, attempt int, start time.Time, delay time.Duration, info *output.ResponseInfo, stderr io.Writer) bool {
	if attempt >= o.Retries {
		return false
	}
	if o.RetryIdempotent && !isIdempotentMethod(method) {
		if o.Verbose {
			fmt.Fprintf(stderr, "* Not retrying: method %s is not idempotent\n", method)
		}
		return false
	}
	if !retryFits(o, start, delay) {
		return false
	}
	if !o.RetryBudget.Take(host) {
		if o.Verbose {
			fmt.Fprintf(stderr, "* Not retrying: retry budget exhausted for host %s\n", host)
		}
		return false
	}
	if info != nil {
		info.NumRetries++
	}
	return true
}

func shouldRetryStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

func shouldRetryError(err error, o *config.Options) bool {
	if err == nil {
		return false
	}
	if o.RetryAllErrors {
		return true
	}
	if isTimeoutError(err) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	if o.RetryConnrefused && errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	var de *exitcode.Coded
	if errors.As(err, &de) {
		switch de.Code {
		case 28, 52, 55, 56:
			return true
		case 7:
			return o.RetryConnrefused
		}
	}
	return false
}

func retryDelayFor(o *config.Options, attempt int, resp *fhttp.Response) time.Duration {
	if resp != nil {
		if delay, ok := parseRetryAfter(resp.Header.Get("Retry-After")); ok {
			if delay > time.Minute {
				delay = time.Minute
			}
			return delay
		}
	}
	if o.RetryDelay > 0 {
		return o.RetryDelay
	}
	delay := 10 * time.Minute
	if attempt < 10 {
		delay = time.Second << attempt
	}
	return delay
}

func retryFits(o *config.Options, start time.Time, delay time.Duration) bool {
	if o.RetryMaxTime > 0 && time.Since(start)+delay > o.RetryMaxTime {
		return false
	}
	if o.MaxTime > 0 && time.Since(start)+delay > o.MaxTime {
		return false
	}
	return true
}

func waitRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func parseRetryAfter(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second, true
	}
	if when, err := http.ParseTime(value); err == nil {
		delay := time.Until(when)
		if delay < 0 {
			delay = 0
		}
		return delay, true
	}
	return 0, false
}

func retryWaitError(o *config.Options, err error) error {
	if err == nil {
		return nil
	}
	if o.MaxTime > 0 {
		return newTimeoutError(o, 0)
	}
	return fmt.Errorf("retry wait: %w", err)
}
