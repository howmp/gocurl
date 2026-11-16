package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func ensurePrivateDir(dir string) error {
	if dir == "" {
		return nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	dir = abs
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("daemon path %s is not a directory", dir)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("daemon directory %s must not be group/world accessible", dir)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("daemon directory %s is owned by uid %d, not %d", dir, stat.Uid, os.Getuid())
	}
	return nil
}

func validateDaemonSocket(socket string) error {
	info, err := os.Lstat(socket)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("daemon path %s is not a Unix socket", socket)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("daemon socket %s must not be group/world accessible", socket)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("daemon socket %s is owned by uid %d, not %d", socket, stat.Uid, os.Getuid())
	}
	if err := ensurePrivateDir(filepath.Dir(socket)); err != nil {
		return err
	}
	return nil
}
