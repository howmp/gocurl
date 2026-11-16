package config

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

type formSegment struct {
	prefix     []byte
	file       string
	size       int64
	data       []byte
	hasContent bool
}

type MultipartStream struct {
	contentType string
	length      int64
	segments    []formSegment
	boundary    string
}

func CanStreamForm(o *Options) bool {
	if len(o.Forms) == 0 || o.Get || len(o.Data) > 0 || o.JsonSet || o.UploadFile != "" {
		return false
	}
	for _, field := range o.Forms {
		if field.Kind == FormText {
			continue
		}
		if field.Value == "-" || field.Encoder != "" {
			return false
		}
		if field.Kind == FormFile {
			for _, path := range splitFormPaths(field.Value) {
				if path == "-" {
					return false
				}
			}
		}
	}
	return true
}

func NewMultipartStream(fields []FormField) (*MultipartStream, error) {
	boundaryBytes := make([]byte, 16)
	if _, err := rand.Read(boundaryBytes); err != nil {
		return nil, err
	}
	boundary := hex.EncodeToString(boundaryBytes)
	stream := &MultipartStream{
		boundary:    boundary,
		contentType: "multipart/form-data; boundary=" + boundary,
	}
	addPart := func(header string, file string, size int64, data []byte) {
		prefix := []byte("--" + boundary + "\r\n" + header)
		stream.segments = append(stream.segments, formSegment{
			prefix:     prefix,
			file:       file,
			size:       size,
			data:       data,
			hasContent: true,
		})
		stream.length += int64(len(prefix)) + size + 2
	}
	for _, field := range fields {
		switch field.Kind {
		case FormText:
			data, err := encodeFormData([]byte(field.Value), field.Encoder)
			if err != nil {
				return nil, err
			}
			header, err := formPartHeader(field, "", field.ContentType)
			if err != nil {
				return nil, err
			}
			addPart(header, "", int64(len(data)), data)
		case FormFile, FormContent:
			paths := []string{field.Value}
			if field.Kind == FormFile {
				paths = splitFormPaths(field.Value)
			}
			for _, path := range paths {
				info, err := os.Stat(path)
				if err != nil {
					return nil, err
				}
				if info.IsDir() {
					return nil, fmt.Errorf("form file %q is a directory", path)
				}
				filename, contentType := formFileMeta(field, path)
				header, err := formPartHeader(field, filename, contentType)
				if err != nil {
					return nil, err
				}
				addPart(header, path, info.Size(), nil)
			}
		}
	}
	tail := []byte("--" + boundary + "--\r\n")
	stream.segments = append(stream.segments, formSegment{prefix: tail})
	stream.length += int64(len(tail))
	return stream, nil
}

func formPartHeader(field FormField, filename, contentType string) (string, error) {
	part, err := buildFormPartHeader(field.Name, filename, contentType, field.Headers)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("Content-Disposition: ")
	b.WriteString(part.disposition)
	b.WriteString("\r\n")
	if part.contentType != "" {
		b.WriteString("Content-Type: ")
		b.WriteString(part.contentType)
		b.WriteString("\r\n")
	}
	for _, pair := range part.extra {
		b.WriteString(pair[0])
		b.WriteString(": ")
		b.WriteString(pair[1])
		b.WriteString("\r\n")
	}
	b.WriteString("\r\n")
	return b.String(), nil
}

func formHeaderLinesChecked(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	if strings.HasPrefix(value, "@") {
		data, err := ReadLocalFile(strings.TrimPrefix(value, "@"))
		if err != nil {
			return nil, err
		}
		value = string(data)
	}
	var lines []string
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

func (s *MultipartStream) Open() (io.ReadCloser, error) {
	var readers []io.Reader
	var files []*os.File
	closeAll := func() {
		for _, file := range files {
			_ = file.Close()
		}
	}
	for _, segment := range s.segments {
		if len(segment.prefix) > 0 {
			readers = append(readers, bytes.NewReader(segment.prefix))
		}
		if segment.file != "" {
			f, err := os.Open(segment.file)
			if err != nil {
				closeAll()
				return nil, err
			}
			info, err := f.Stat()
			if err != nil || info.Size() != segment.size {
				_ = f.Close()
				closeAll()
				if err != nil {
					return nil, err
				}
				return nil, fmt.Errorf("form file %s changed during upload", segment.file)
			}
			files = append(files, f)
			readers = append(readers, io.LimitReader(f, segment.size))
		} else if len(segment.data) > 0 {
			readers = append(readers, bytes.NewReader(segment.data))
		}
		if segment.hasContent {
			readers = append(readers, strings.NewReader("\r\n"))
		}
	}
	return &formStreamReader{Reader: io.MultiReader(readers...), files: files}, nil
}

type formStreamReader struct {
	io.Reader
	files []*os.File
}

func (r *formStreamReader) Close() error {
	for _, file := range r.files {
		_ = file.Close()
	}
	return nil
}

func (s *MultipartStream) ContentType() string { return s.contentType }
func (s *MultipartStream) Length() int64       { return s.length }
