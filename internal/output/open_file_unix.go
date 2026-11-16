//go:build (aix || darwin || dragonfly || freebsd || netbsd || openbsd || solaris) && !linux

package output

import (
	"os"
	"syscall"
)

func openOutputFilePlatform(name string, flags int) (*os.File, error) {
	if isDevicePath(name) {
		return os.OpenFile(name, flags, 0o600)
	}
	return os.OpenFile(name, flags|syscall.O_NOFOLLOW, 0o600)
}
