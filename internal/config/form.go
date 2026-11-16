package config

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/textproto"
	"path/filepath"

	"strings"

	"golang.org/x/net/http/httpguts"
)

type formKind int

const (
	FormText formKind = iota
	FormFile
	FormContent
)

type FormField struct {
	Name        string
	Value       string
	Kind        formKind
	ContentType string
	Filename    string
	Headers     string
	Encoder     string
}

func validateFormFieldText(value string) error {
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] == 0x7f {
			return fmt.Errorf("form field contains control characters")
		}
	}
	return nil
}

func ParseFormOption(v string, literal bool) (FormField, error) {
	name, rest, ok := strings.Cut(v, "=")
	name = strings.TrimSpace(name)
	if !ok || name == "" {
		return FormField{}, fmt.Errorf("invalid form field")
	}
	if err := validateFormFieldText(name); err != nil {
		return FormField{}, err
	}
	if literal {
		return FormField{Name: name, Value: rest, Kind: FormText}, nil
	}

	kind := FormText
	switch {
	case strings.HasPrefix(rest, "@"):
		kind = FormFile
		rest = rest[1:]
	case strings.HasPrefix(rest, "<"):
		kind = FormContent
		rest = rest[1:]
	}
	parts := splitFormParts(rest)
	value := unquoteConfigValue(strings.TrimSpace(parts[0]))
	field := FormField{Name: name, Value: value, Kind: kind}
	for _, attr := range parts[1:] {
		attr = strings.TrimSpace(attr)
		if attr == "" {
			continue
		}
		key, val, ok := strings.Cut(attr, "=")
		if !ok {
			return FormField{}, fmt.Errorf("invalid form attribute")
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "type":
			field.ContentType = unquoteConfigValue(strings.TrimSpace(val))
		case "filename":
			field.Filename = unquoteConfigValue(strings.TrimSpace(val))
		case "headers":
			field.Headers = unquoteConfigValue(strings.TrimSpace(val))
		case "encoder":
			field.Encoder = strings.ToLower(unquoteConfigValue(strings.TrimSpace(val)))
		default:
			return FormField{}, fmt.Errorf("form attribute is not supported")
		}
	}
	if err := validateFormFieldText(field.Filename); err != nil {
		return FormField{}, err
	}
	if kind == FormFile {
		if field.Value == "" {
			return FormField{}, fmt.Errorf("form field has no file name")
		}
	}
	if kind == FormContent && field.ContentType == "" {
		field.ContentType = "text/plain"
	}
	return field, nil
}

func splitFormParts(v string) []string {
	var parts []string
	start := 0
	var quote byte
	for i := 0; i < len(v); i++ {
		switch {
		case quote != 0:
			if v[i] == '\\' {
				i++
			} else if v[i] == quote {
				quote = 0
			}
		case v[i] == '"' || v[i] == '\'':
			quote = v[i]
		case v[i] == ';':
			parts = append(parts, v[start:i])
			start = i + 1
		}
	}
	return append(parts, v[start:])
}

func BuildMultipartBody(fields []FormField) ([]byte, string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, field := range fields {
		switch field.Kind {
		case FormText:
			data, err := encodeFormData([]byte(field.Value), field.Encoder)
			if err != nil {
				return nil, "", err
			}
			if field.ContentType == "" && field.Headers == "" && field.Encoder == "" {
				if err := validateFormFieldText(field.Name); err != nil {
					return nil, "", err
				}
				w, err := mw.CreateFormField(field.Name)
				if err != nil {
					return nil, "", err
				}
				if _, err := w.Write(data); err != nil {
					return nil, "", err
				}
				continue
			}
			part, err := createFormPart(mw, field.Name, "", field.ContentType, field.Headers)
			if err != nil {
				return nil, "", err
			}
			if _, err := part.Write(data); err != nil {
				return nil, "", err
			}
		case FormFile, FormContent:
			paths := []string{field.Value}
			if field.Kind == FormFile {
				paths = splitFormPaths(field.Value)
			}
			for _, path := range paths {
				data, err := readFormValue(path)
				if err != nil {
					return nil, "", err
				}
				data, err = encodeFormData(data, field.Encoder)
				if err != nil {
					return nil, "", err
				}
				filename, contentType := formFileMeta(field, path)
				part, err := createFormPart(mw, field.Name, filename, contentType, field.Headers)
				if err != nil {
					return nil, "", err
				}
				if _, err := part.Write(data); err != nil {
					return nil, "", err
				}
			}
		}
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), mw.FormDataContentType(), nil
}

func splitFormPaths(value string) []string {
	var paths []string
	for _, path := range strings.Split(value, ",") {
		if path = strings.TrimSpace(path); path != "" {
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		return []string{value}
	}
	return paths
}

type formPartSpec struct {
	disposition string
	contentType string
	extra       [][2]string
}

func buildFormPartHeader(name, filename, contentType, headers string) (*formPartSpec, error) {
	if err := validateFormFieldText(name); err != nil {
		return nil, err
	}
	if err := validateFormFieldText(filename); err != nil {
		return nil, err
	}
	params := map[string]string{"name": name}
	if filename != "" {
		params["filename"] = filename
	}
	disposition := mime.FormatMediaType("form-data", params)
	if disposition == "" {
		return nil, fmt.Errorf("invalid form field name %q", name)
	}
	header := &formPartSpec{disposition: disposition}
	if contentType != "" {
		if !httpguts.ValidHeaderFieldValue(contentType) {
			return nil, fmt.Errorf("invalid form content type")
		}
		header.contentType = contentType
	}
	lines, err := formHeaderLinesChecked(headers)
	if err != nil {
		return nil, err
	}
	for _, line := range lines {
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("invalid form header")
		}
		field, value = strings.TrimSpace(field), strings.TrimSpace(value)
		if !httpguts.ValidHeaderFieldName(field) || !httpguts.ValidHeaderFieldValue(value) {
			return nil, fmt.Errorf("invalid form header")
		}
		header.extra = append(header.extra, [2]string{field, value})
	}
	return header, nil
}

func formFileMeta(field FormField, path string) (string, string) {
	if field.Kind != FormFile {
		return "", field.ContentType
	}
	filename := field.Filename
	if filename == "" {
		if path == "-" {
			filename = "stdin"
		} else {
			filename = filepath.Base(path)
		}
	}
	contentType := field.ContentType
	if contentType == "" {
		if path == "-" {
			contentType = "application/octet-stream"
		} else {
			contentType = mime.TypeByExtension(filepath.Ext(path))
			if contentType == "" {
				contentType = "application/octet-stream"
			}
		}
	}
	return filename, contentType
}

func createFormPart(mw *multipart.Writer, name, filename, contentType, headers string) (io.Writer, error) {
	part, err := buildFormPartHeader(name, filename, contentType, headers)
	if err != nil {
		return nil, err
	}
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", part.disposition)
	if part.contentType != "" {
		header.Set("Content-Type", part.contentType)
	}
	for _, pair := range part.extra {
		header.Set(pair[0], pair[1])
	}
	return mw.CreatePart(header)
}

func encodeFormData(data []byte, encoder string) ([]byte, error) {
	switch strings.ToLower(encoder) {
	case "", "binary", "8bit":
		return data, nil
	case "base64":
		out := make([]byte, base64.StdEncoding.EncodedLen(len(data)))
		base64.StdEncoding.Encode(out, data)
		return out, nil
	case "quoted-printable":
		var buf bytes.Buffer
		w := quotedprintable.NewWriter(&buf)
		if _, err := w.Write(data); err != nil {
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	default:
		return nil, fmt.Errorf("form encoder %q is not supported", encoder)
	}
}

func readFormValue(path string) ([]byte, error) {
	return ReadLocalFile(path)
}
