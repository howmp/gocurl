package output

import (
	"context"
	"io"
	"time"
)

func NewRateReader(ctx context.Context, r io.Reader, rate int64) io.ReadCloser {
	return &rateReader{r: r, limiter: newRateLimiter(ctx, rate)}
}

type rateLimiter struct {
	rate  int64
	start time.Time
	sent  int64
	ctx   context.Context
}

func newRateLimiter(ctx context.Context, rate int64) rateLimiter {
	if ctx == nil {
		ctx = context.Background()
	}
	return rateLimiter{rate: rate, start: time.Now(), ctx: ctx}
}

func (l *rateLimiter) after(n int) error {
	l.sent += int64(n)
	return waitForRate(l.ctx, l.sent, l.rate, l.start)
}

type rateWriter struct {
	w       io.Writer
	limiter rateLimiter
}

func (w *rateWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	if err == nil {
		err = w.limiter.after(n)
	}
	return n, err
}

type rateReader struct {
	r       io.Reader
	limiter rateLimiter
}

func (r *rateReader) Close() error {
	if closer, ok := r.r.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func (r *rateReader) Read(p []byte) (int, error) {
	if r.limiter.rate > 0 && len(p) > 32*1024 {
		p = p[:32*1024]
	}
	n, err := r.r.Read(p)
	if err == nil {
		err = r.limiter.after(n)
	}
	return n, err
}

func waitForRate(ctx context.Context, sent, rate int64, start time.Time) error {
	if rate <= 0 || sent <= 0 {
		return nil
	}
	delaySeconds := float64(sent)/float64(rate) - time.Since(start).Seconds()
	if delaySeconds <= 0 {
		return nil
	}
	if delaySeconds > 24*60*60 {
		delaySeconds = 24 * 60 * 60
	}
	timer := time.NewTimer(time.Duration(delaySeconds * float64(time.Second)))
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type MaxFilesizeError struct{}

func (e *MaxFilesizeError) Error() string {
	return "Maximum filesize exceeded"
}
func (e *MaxFilesizeError) ExitCode() int { return 63 }

type maxSizeReader struct {
	r         io.Reader
	remaining int64
	exceeded  bool
}

func (r *maxSizeReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		var one [1]byte
		n, err := r.r.Read(one[:])
		if n > 0 {
			r.exceeded = true
			return 0, &MaxFilesizeError{}
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.r.Read(p)
	r.remaining -= int64(n)
	return n, err
}

func hasExceededMaxSize(r io.Reader) bool {
	mr, ok := r.(*maxSizeReader)
	return ok && mr.exceeded
}
