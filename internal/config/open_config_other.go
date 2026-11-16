//go:build !(aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package config

import (
	"fmt"
	"os"
)

func openConfigFile(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = f.Close()
		return nil, fmt.Errorf("config file changed while opening")
	}
	return f, nil
}
