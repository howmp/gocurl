package app

import (
	"context"
	"io"
	"net/url"
	"time"

	"gocurl/internal/config"
	"gocurl/internal/httpx"
	"gocurl/internal/output"

	http "github.com/bogdanfinn/fhttp"
)

func failStatus(o *config.Options, resp *http.Response, withBody bool) bool {
	if withBody {
		return o.FailWithBody && resp.StatusCode >= 400
	}
	return o.Fail && !o.FailWithBody && resp.StatusCode >= 400
}

func statusError(resp *http.Response) error {
	return &httpx.HTTPStatusError{Code: resp.StatusCode}
}

func emitResponse(o *config.Options, outOptions *config.Options, u *url.URL, idx int, stdout io.Writer, resp *http.Response, info *output.ResponseInfo, ctx context.Context, start time.Time) error {
	if failStatus(o, resp, false) {
		info.TimeTotal = time.Since(start)
		if o.Include || o.Head {
			out, closeOut, err := output.OutputWriter(outOptions, u, idx, stdout)
			if err != nil {
				return err
			}
			if err := output.WriteResponseHeaders(out, resp); err != nil {
				_ = closeOut()
				return err
			}
			if err := closeOut(); err != nil {
				return err
			}
		}
		if err := output.WriteOutFormat(o, info, stdout); err != nil {
			return err
		}
		return statusError(resp)
	}

	out, closeOut, err := output.OutputWriter(outOptions, u, idx, stdout)
	if err != nil {
		return err
	}
	defer closeOut()

	if o.Include || o.Head {
		if err := output.WriteResponseHeaders(out, resp); err != nil {
			return err
		}
	}
	if !o.Head {
		streamErr := streamResponseBody(ctx, outOptions, resp, out, info)
		if streamErr != nil {
			if ctx.Err() != nil || isTimeoutError(streamErr) {
				if err := finishTransfer(o, info, start, stdout, closeOut); err != nil {
					return err
				}
				return newTimeoutError(o, info.SizeDownload)
			}
			if err := finishTransfer(o, info, start, stdout, closeOut); err != nil {
				return err
			}
			return streamErr
		}
		if ctx.Err() != nil {
			if err := finishTransfer(o, info, start, stdout, closeOut); err != nil {
				return err
			}
			return newTimeoutError(o, info.SizeDownload)
		}
	}
	if err := finishTransfer(o, info, start, stdout, closeOut); err != nil {
		return err
	}
	if failStatus(o, resp, true) {
		return statusError(resp)
	}
	return nil
}
