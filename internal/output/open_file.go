package output

import "os"

func openOutputFile(name string, appendMode, truncate bool) (*os.File, error) {
	flags := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flags |= os.O_APPEND
	}
	if truncate {
		flags |= os.O_TRUNC
	}
	return openOutputFilePlatform(name, flags)
}
