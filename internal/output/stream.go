package output

import (
	"bufio"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"syscall"

	"github.com/andybalholm/brotli"
	http "github.com/bogdanfinn/fhttp"
	"github.com/klauspost/compress/zstd"

	"gocurl/internal/config"
)

func StreamResponseBody(ctx context.Context, resp *http.Response, out io.Writer, o *config.Options) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var body io.ReadCloser
	if o.Raw {
		body = &multiReadCloser{Reader: resp.Body, closers: []func() error{resp.Body.Close}}
	} else {
		var err error
		body, err = decodedBody(resp)
		if err != nil {
			return 0, err
		}
	}
	defer body.Close()
	if o.MaxFileSize > 0 && resp.ContentLength > o.MaxFileSize {
		return 0, &MaxFilesizeError{}
	}
	var reader io.Reader = body
	if o.MaxFileSize > 0 {
		reader = &maxSizeReader{r: reader, remaining: o.MaxFileSize}
	}
	var writer io.Writer = out
	if o.LimitRate > 0 {
		writer = &rateWriter{w: out, limiter: newRateLimiter(ctx, o.LimitRate)}
	}
	n, err := io.Copy(writer, reader)
	if err == nil && hasExceededMaxSize(reader) {
		err = &MaxFilesizeError{}
	}
	return n, err
}

type multiReadCloser struct {
	io.Reader
	closers []func() error
	once    sync.Once
	err     error
}

func (m *multiReadCloser) Close() error {
	m.once.Do(func() {
		for i := len(m.closers) - 1; i >= 0; i-- {
			if err := m.closers[i](); err != nil && m.err == nil {
				m.err = err
			}
		}
	})
	return m.err
}

type ContentEncodingError struct {
	Encoding string
	Err      error
}

func (e *ContentEncodingError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("content encoding %q: %v", e.Encoding, e.Err)
	}
	return fmt.Sprintf("unsupported content encoding %q", e.Encoding)
}
func (e *ContentEncodingError) Unwrap() error { return e.Err }
func (e *ContentEncodingError) ExitCode() int { return 61 }

type encodingReadCloser struct {
	io.ReadCloser
	encoding string
}

func (e *encodingReadCloser) Read(p []byte) (int, error) {
	n, err := e.ReadCloser.Read(p)
	if err != nil && err != io.EOF && !passThroughReadError(err) {
		return n, &ContentEncodingError{Encoding: e.encoding, Err: err}
	}
	return n, err
}

func passThroughReadError(err error) bool {
	if errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

func decodedBody(resp *http.Response) (io.ReadCloser, error) {
	reader := io.Reader(resp.Body)
	mrc := &multiReadCloser{closers: []func() error{resp.Body.Close}}
	encodings := strings.Split(resp.Header.Get("Content-Encoding"), ",")
	layers := 0
	for i := len(encodings) - 1; i >= 0; i-- {
		enc := strings.ToLower(strings.TrimSpace(encodings[i]))
		switch enc {
		case "", "identity":
			continue
		}
		layers++
		if layers > 3 {
			mrc.Close()
			return nil, &ContentEncodingError{Encoding: enc}
		}
		switch enc {
		case "gzip", "x-gzip":
			zr, err := gzip.NewReader(reader)
			if err != nil {
				mrc.Close()
				return nil, err
			}
			reader = &encodingReadCloser{ReadCloser: zr, encoding: enc}
			mrc.closers = append(mrc.closers, zr.Close)
		case "deflate":
			br := bufio.NewReader(reader)
			header, err := br.Peek(2)
			if err != nil && !errors.Is(err, io.EOF) {
				mrc.Close()
				return nil, err
			}
			if err == nil && header[0]&0x0f == 8 && ((int(header[0])<<8)|int(header[1]))%31 == 0 {
				zr, err := zlib.NewReader(br)
				if err != nil {
					mrc.Close()
					return nil, err
				}
				reader = &encodingReadCloser{ReadCloser: zr, encoding: enc}
				mrc.closers = append(mrc.closers, zr.Close)
			} else {
				fr := flate.NewReader(br)
				reader = &encodingReadCloser{ReadCloser: fr, encoding: enc}
				mrc.closers = append(mrc.closers, fr.Close)
			}
		case "br":
			reader = &encodingReadCloser{ReadCloser: io.NopCloser(brotli.NewReader(reader)), encoding: enc}
		case "zstd":
			zr, err := zstd.NewReader(reader)
			if err != nil {
				mrc.Close()
				return nil, err
			}
			reader = &encodingReadCloser{ReadCloser: io.NopCloser(zr), encoding: enc}
			mrc.closers = append(mrc.closers, func() error {
				zr.Close()
				return nil
			})
		default:
			mrc.Close()
			return nil, &ContentEncodingError{Encoding: enc}
		}
	}
	mrc.Reader = reader
	return mrc, nil
}
