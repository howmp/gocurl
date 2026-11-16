package app

import (
	"context"
	"fmt"
	"gocurl/internal/config"
	"gocurl/internal/output"
	"io"
	"net/url"
	"time"
)

func retryHTTP(ctx context.Context, o *config.Options, u *url.URL, method string, attempt int, start time.Time, info *output.ResponseInfo, delay time.Duration, stderr io.Writer) (bool, error) {
	if !allowRetry(o, method, retryHost(u), attempt, start, delay, info, stderr) {
		return false, nil
	}
	if o.Verbose {
		fmt.Fprintf(stderr, "* Retrying in %s (%d/%d)\n", delay, attempt+1, o.Retries)
	}
	if err := waitRetry(ctx, delay); err != nil {
		return false, retryWaitError(o, err)
	}
	return true, nil
}

func finishTransfer(o *config.Options, info *output.ResponseInfo, start time.Time, stdout io.Writer, closeOut func() error) error {
	info.TimeTotal = time.Since(start)
	if err := closeOut(); err != nil {
		return err
	}
	return output.WriteOutFormat(o, info, stdout)
}
