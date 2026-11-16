package config

import (
	"os"
	"strings"
	"testing"
)

func FuzzParseFormOption(f *testing.F) {
	f.Add("a=b")
	f.Add(`f=@/tmp/x;filename="x"`)
	f.Fuzz(func(t *testing.T, value string) {
		_, _ = ParseFormOption(value, false)
		_, _ = ParseFormOption(value, true)
	})
}

func TestParseFormOptionRejectsControlCharacters(t *testing.T) {
	if _, err := ParseFormOption("a\r\nX-Injected: 1=value", false); err == nil {
		t.Fatal("expected field name rejection")
	}
	if _, err := ParseFormOption(`f=@/tmp/x;filename="a\r\nb"`, false); err == nil {
		t.Fatal("expected filename rejection")
	}
	if _, err := ParseFormOption("a\x00b=value", false); err == nil {
		t.Fatal("expected NUL rejection")
	}
	if _, err := ParseFormOption("a\x7fb=value", false); err == nil {
		t.Fatal("expected DEL rejection")
	}
}

func TestParseFormOptionAllowsUnicode(t *testing.T) {
	field, err := ParseFormOption("字段=值", false)
	if err != nil {
		t.Fatal(err)
	}
	if field.Name != "字段" || field.Value != "值" {
		t.Fatalf("field = %#v", field)
	}
	field, err = ParseFormOption(`file=@/tmp/x;filename="文件.txt"`, false)
	if err != nil {
		t.Fatal(err)
	}
	if field.Filename != "文件.txt" {
		t.Fatalf("filename = %q", field.Filename)
	}
}

func TestBuildMultipartBodyRejectsInjectedName(t *testing.T) {
	_, _, err := BuildMultipartBody([]FormField{{
		Name:  "a\r\nX-Injected: 1",
		Value: "value",
		Kind:  FormText,
	}})
	if err == nil {
		t.Fatal("expected multipart field rejection")
	}
}

func TestBuildFormPartHeaderRejectsInjectedFilename(t *testing.T) {
	if _, err := buildFormPartHeader("file", "x\r\nY-Injected: 2", "", ""); err == nil {
		t.Fatal("expected multipart filename rejection")
	}
}

func TestNewMultipartStreamRejectsInjectedFilename(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "form-*")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	_ = f.Close()
	_, err = NewMultipartStream([]FormField{{
		Name:     "file",
		Value:    path,
		Kind:     FormFile,
		Filename: "x\r\nY-Injected: 2",
	}})
	if err == nil {
		t.Fatal("expected stream filename rejection")
	}
	if strings.Contains(err.Error(), "\r\n") {
		t.Fatalf("error leaked control characters: %q", err)
	}
}
