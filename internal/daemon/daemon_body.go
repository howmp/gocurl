package daemon

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
)

func writeDaemonResponseBody(conn net.Conn, body io.Reader, length int64, chunked bool) error {
	if chunked {
		return writeChunkedBody(conn, body)
	}
	if length <= 0 {
		return nil
	}
	_, err := io.CopyN(conn, body, length)
	return err
}

func writeChunkedBody(w io.Writer, body io.Reader) error {
	buf := make([]byte, 32<<10)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if err := writeDaemonChunk(w, buf[:n]); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			return writeDaemonChunkEnd(w)
		}
		if readErr != nil {
			_ = writeDaemonChunkError(w, readErr)
			return readErr
		}
	}
}

func writeDaemonChunk(w io.Writer, data []byte) error {
	if len(data) > int(maxDaemonChunk) {
		return fmt.Errorf("daemon chunk too large")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}

func writeDaemonChunkEnd(w io.Writer) error {
	var header [4]byte
	_, err := w.Write(header[:])
	return err
}

func writeDaemonChunkError(w io.Writer, cause error) error {
	msg := cause.Error()
	if len(msg) > 4096 {
		msg = msg[:4096]
	}
	var header [9]byte
	binary.BigEndian.PutUint32(header[:4], daemonChunkError)
	header[4] = daemonChunkErrorKind(cause)
	binary.BigEndian.PutUint32(header[5:], uint32(len(msg)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err := io.WriteString(w, msg)
	return err
}

func daemonChunkErrorKind(err error) byte {
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return chunkErrTruncated
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return chunkErrReset
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return chunkErrTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return chunkErrTimeout
	}
	return chunkErrOther
}
