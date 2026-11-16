//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package daemon

import (
	"os"
	"syscall"
)

func openDaemonLog(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
