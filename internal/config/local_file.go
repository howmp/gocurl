package config

import (
	"fmt"
	"io"
	"os"
)

const MaxLocalInputBytes = 64 << 20

func ReadLocalFile(path string) ([]byte, error) {
	if path == "-" {
		return readLimited(os.Stdin)
	}
	return ReadFile(path)
}

func ReadFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readLimited(f)
}

func readLimited(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxLocalInputBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxLocalInputBytes {
		return nil, fmt.Errorf("local input exceeds %d bytes", MaxLocalInputBytes)
	}
	return data, nil
}
