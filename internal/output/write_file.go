package output

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

func tempNameFor(path string) (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return path + ".tmp-" + hex.EncodeToString(suffix[:]), nil
}

func WriteFile(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("output path is a symlink")
		}
		if !info.Mode().IsRegular() {
			f, err := openOutputFile(path, false, true)
			if err != nil {
				return err
			}
			if _, err := f.Write(data); err != nil {
				_ = f.Close()
				return err
			}
			return f.Close()
		}
	}
	name, err := tempNameFor(path)
	if err != nil {
		return err
	}
	tmp, err := openOutputFile(name, false, true)
	if err != nil {
		return err
	}
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(name)
	}
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
