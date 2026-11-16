package ftp

import (
	"errors"
	"fmt"
	"gocurl/internal/config"
	"gocurl/internal/exitcode"
	"gocurl/internal/output"
	"io"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/jlaffaye/ftp"
)

type ftpSession interface {
	store(path string, offset int64, r io.Reader) error
	retrieve(path string, offset int64, w io.Writer) (int64, error)
	list(path string, names bool, w io.Writer) (int64, error)
	size(path string) (int64, error)
	mdtm(path string) (time.Time, error)
	Close() error
}

type passiveSession struct {
	conn *ftp.ServerConn
}

func (s *passiveSession) store(path string, offset int64, r io.Reader) error {
	return s.conn.StorFrom(path, r, uint64(offset))
}

func (s *passiveSession) retrieve(path string, offset int64, w io.Writer) (int64, error) {
	var (
		resp *ftp.Response
		err  error
	)
	if offset > 0 {
		resp, err = s.conn.RetrFrom(path, uint64(offset))
	} else {
		resp, err = s.conn.Retr(path)
	}
	if err != nil {
		return 0, err
	}
	n, copyErr := io.Copy(w, resp)
	copyErr = ftpWriteError(copyErr)
	closeErr := resp.Close()
	if copyErr != nil {
		return n, copyErr
	}
	if closeErr != nil {
		return n, closeErr
	}
	return n, nil
}

func (s *passiveSession) list(path string, names bool, w io.Writer) (int64, error) {
	if names {
		entries, err := s.conn.NameList(path)
		if err != nil {
			return 0, err
		}
		var n int64
		for _, name := range entries {
			count, err := fmt.Fprintln(w, name)
			if err != nil {
				return n, ftpWriteError(err)
			}
			n += int64(count)
		}
		return n, nil
	}
	entries, err := s.conn.List(path)
	if err != nil {
		return 0, err
	}
	var n int64
	for _, entry := range entries {
		mode := "-rw-r--r--"
		switch entry.Type {
		case ftp.EntryTypeFolder:
			mode = "drwxr-xr-x"
		case ftp.EntryTypeLink:
			mode = "lrwxrwxrwx"
		}
		line := fmt.Sprintf("%s %10d %s %s", mode, entry.Size, entry.Time.Format("Jan _2 15:04"), entry.Name)
		if entry.Target != "" {
			line += " -> " + entry.Target
		}
		count, err := fmt.Fprintln(w, line)
		if err != nil {
			return n, ftpWriteError(err)
		}
		n += int64(count)
	}
	return n, nil
}

func (s *passiveSession) size(path string) (int64, error) { return s.conn.FileSize(path) }

func (s *passiveSession) mdtm(path string) (time.Time, error) {
	if modified, err := s.conn.GetTime(path); err == nil {
		return modified, nil
	}
	entry, err := s.conn.GetEntry(path)
	if err != nil {
		return time.Time{}, err
	}
	return entry.Time, nil
}

func (s *passiveSession) Close() error { return s.conn.Quit() }

func runFTPSession(o *config.Options, u *url.URL, idx int, stdout, stderr io.Writer, session ftpSession, start time.Time, addr, remotePath string) error {
	info := &output.ResponseInfo{Url: u.String(), RemoteAddr: addr}
	if o.UploadFile != "" {
		target := ftpUploadTarget(remotePath, o.UploadFile)
		if err := validateFTPArg(target); err != nil {
			return err
		}
		offset, err := ftpUploadOffset(o, func() (int64, error) { return session.size(target) })
		if err != nil {
			return err
		}
		reader, err := ftpResumeReader(o.UploadFile, offset)
		if err != nil {
			return err
		}
		defer reader.Close()
		if o.Verbose {
			fmt.Fprintf(stderr, "> STOR %s\n", target)
		}
		if err := session.store(target, offset, reader); err != nil {
			return err
		}
		info.TimeTotal = time.Since(start)
		return output.WriteOutFormat(o, info, stdout)
	}

	offset, local, err := ftpDownloadResume(o, u, idx)
	if err != nil {
		return err
	}
	out, closeOut, err := output.OutputWriter(local, u, idx, stdout)
	if err != nil {
		return err
	}
	defer closeOut()

	if o.Head {
		size, err := session.size(remotePath)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Content-Length: %d\r\n", size)
		if modified, err := session.mdtm(remotePath); err == nil && !modified.IsZero() {
			fmt.Fprintf(out, "Last-Modified: %s\r\n", modified.UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT"))
		}
		fmt.Fprint(out, "\r\n")
		info.StatusCode = 226
		info.TimeTotal = time.Since(start)
		if err := closeOut(); err != nil {
			return err
		}
		return output.WriteOutFormat(o, info, stdout)
	}

	isDir := o.ListOnly || remotePath == "/" || strings.HasSuffix(remotePath, "/")
	if isDir {
		if o.Verbose {
			if o.ListOnly {
				fmt.Fprintf(stderr, "> NLST %s\n", remotePath)
			} else {
				fmt.Fprintf(stderr, "> LIST %s\n", remotePath)
			}
		}
		n, err := session.list(remotePath, o.ListOnly, out)
		if err != nil {
			return err
		}
		info.SizeDownload = n
	} else {
		if o.Verbose {
			fmt.Fprintf(stderr, "> RETR %s\n", remotePath)
		}
		n, err := session.retrieve(remotePath, offset, out)
		if err != nil {
			return err
		}
		info.SizeDownload = n
	}
	info.TimeTotal = time.Since(start)
	if err := closeOut(); err != nil {
		return err
	}
	return output.WriteOutFormat(o, info, stdout)
}

func ftpWriteError(err error) error {
	if err == nil {
		return nil
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ENOSPC) ||
		errors.Is(err, syscall.EROFS) ||
		errors.Is(err, io.ErrClosedPipe) {
		return &exitcode.WriteError{Err: err}
	}
	return err
}
