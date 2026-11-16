package transport

import (
	"bytes"
	"fmt"
	"io"
)

const MaxResponseHeaderBytes = 64 << 10

func LimitResponseHeaderReader(r io.Reader) io.Reader {
	return &headerLimitReader{r: r, remaining: MaxResponseHeaderBytes}
}

type headerLimitReader struct {
	r         io.Reader
	remaining int
	done      bool
	tail      []byte
}

func (l *headerLimitReader) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	if l.done || n == 0 {
		return n, err
	}
	data := p[:n]
	search := data
	if len(l.tail) > 0 {
		search = append(append([]byte(nil), l.tail...), data...)
	}
	if idx := bytes.Index(search, []byte("\r\n\r\n")); idx >= 0 {
		l.remaining -= idx + 4 - len(l.tail)
		l.done = true
	} else {
		l.remaining -= n
		if len(search) > 3 {
			l.tail = append(l.tail[:0], search[len(search)-3:]...)
		} else {
			l.tail = append(l.tail[:0], search...)
		}
	}
	if l.remaining < 0 {
		return n, fmt.Errorf("response header exceeds %d bytes", MaxResponseHeaderBytes)
	}
	return n, err
}
