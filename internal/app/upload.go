package app

import (
	"fmt"
	"gocurl/internal/config"
	"io"
	"os"
)

type uploadStream struct {
	path   string
	size   int64
	offset int64
}

func newUploadStream(path string, offset int64) (*uploadStream, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if offset < 0 || offset > info.Size() {
		return nil, fmt.Errorf("resume offset %d is outside upload file size %d", offset, info.Size())
	}
	return &uploadStream{path: path, size: info.Size(), offset: offset}, nil
}

func (u *uploadStream) open() (io.ReadCloser, error) {
	f, err := os.Open(u.path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if info.Size() != u.size {
		_ = f.Close()
		return nil, fmt.Errorf("upload file size changed from %d to %d", u.size, info.Size())
	}
	if u.offset > 0 {
		if _, err := f.Seek(u.offset, io.SeekStart); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	return f, nil
}

func isStreamingUpload(o *config.Options) bool {
	return o.UploadFile != "" && o.UploadFile != "-" && !o.Get && len(o.Forms) == 0
}
