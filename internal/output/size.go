package output

import (
	http "github.com/bogdanfinn/fhttp"
)

func ResponseHeaderSize(resp *http.Response) int64 {
	size := int64(len(resp.Proto) + 1 + len(resp.Status) + 2)
	for name, values := range resp.Header {
		for _, value := range values {
			size += int64(len(name) + 2 + len(value) + 2)
		}
	}
	return size + 2
}
