package output

import (
	"io"
	"os"
	"strings"

	"gocurl/internal/exitcode"
)

func isDevicePath(name string) bool {
	switch name {
	case "/dev/stdin", "/dev/stdout", "/dev/stderr":
		return true
	}
	return strings.HasPrefix(name, "/dev/fd/") || strings.HasPrefix(name, "/proc/self/fd/")
}

func openOutputFile(name string, appendMode, truncate bool) (*os.File, error) {
	flags := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flags |= os.O_APPEND
	}
	if truncate {
		flags |= os.O_TRUNC
	}
	return openOutputFilePlatform(name, flags)
}

func openResumeOutput(name string, offset int64) (*os.File, error) {
	f, err := openOutputFile(name, false, false)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	size := info.Size()
	seekTo := offset
	if size == 0 && offset > 0 {
		seekTo = 0
	} else if size != offset {
		_ = f.Close()
		return nil, &exitcode.Coded{Code: exitcode.RangeError, Msg: "output file changed during resume"}
	}
	if _, err := f.Seek(seekTo, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
