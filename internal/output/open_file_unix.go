//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package output

import (
	"os"
	"strings"
	"syscall"
)

func openOutputFilePlatform(name string, flags int) (*os.File, error) {
	if isDevicePath(name) {
		return os.OpenFile(name, flags, 0o600)
	}
	return os.OpenFile(name, flags|syscall.O_NOFOLLOW, 0o600)
}

func isDevicePath(name string) bool {
	switch name {
	case "/dev/stdin", "/dev/stdout", "/dev/stderr":
		return true
	}
	return strings.HasPrefix(name, "/dev/fd/") || strings.HasPrefix(name, "/proc/self/fd/")
}
