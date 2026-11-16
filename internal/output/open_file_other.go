//go:build !(aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package output

import (
	"fmt"
	"os"
)

func openOutputFilePlatform(name string, flags int) (*os.File, error) {
	if info, err := os.Lstat(name); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("output path is a symlink")
	}
	return os.OpenFile(name, flags, 0o600)
}
