//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package config

import (
	"fmt"
	"os"
	"syscall"
)

func openLocalFile(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file: %s", path)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = f.Close()
		return nil, fmt.Errorf("file changed while opening: %s", path)
	}
	return f, nil
}
