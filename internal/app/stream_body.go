package app

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"syscall"

	"gocurl/internal/config"
	"gocurl/internal/exitcode"
	"gocurl/internal/output"

	http "github.com/bogdanfinn/fhttp"
)

func streamResponseBody(ctx context.Context, o *config.Options, resp *http.Response, out io.Writer, info *output.ResponseInfo) error {
	n, err := output.StreamResponseBody(ctx, resp, out, o)
	info.SizeDownload = n
	return mapStreamError(ctx, o, resp, n, err)
}

func mapStreamError(ctx context.Context, o *config.Options, resp *http.Response, n int64, err error) error {
	if err == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil || isTimeoutError(err) {
		return newTimeoutError(o, n)
	}
	if errors.Is(err, syscall.ECONNRESET) {
		return &exitcode.Coded{Code: exitcode.RecvFailure, Msg: "Recv failure: Connection reset by peer"}
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		if resp != nil && resp.ContentLength >= 0 && n < resp.ContentLength {
			return &exitcode.Coded{Code: exitcode.PartialFile, Msg: missingBytesMessage(resp.ContentLength - n)}
		}
		return &exitcode.Coded{Code: exitcode.PartialFile, Msg: "transfer closed with outstanding read data remaining"}
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) || errors.Is(err, syscall.EPIPE) || errors.Is(err, io.ErrClosedPipe) {
		return &exitcode.WriteError{Err: err}
	}
	return err
}

func missingBytesMessage(missing int64) string {
	return "end of response with " + strconv.FormatInt(missing, 10) + " bytes missing"
}
