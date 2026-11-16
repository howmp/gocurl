//go:build linux

package output

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func openOutputFilePlatform(name string, flags int) (*os.File, error) {
	if isDevicePath(name) {
		return os.OpenFile(name, flags, 0o600)
	}
	how := &unix.OpenHow{
		Flags:   uint64(flags | syscall.O_CLOEXEC),
		Mode:    0o600,
		Resolve: unix.RESOLVE_NO_SYMLINKS,
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, name, how)
	if err == nil {
		return os.NewFile(uintptr(fd), name), nil
	}
	if !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.EPERM) {
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	return os.OpenFile(name, flags|syscall.O_NOFOLLOW, 0o600)
}
