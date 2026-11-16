//go:build !(aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package output

import "os"

func openOutputFilePlatform(name string, flags int) (*os.File, error) {
	return os.OpenFile(name, flags, 0o600)
}
