//go:build !unix

package daemon

import "fmt"

func startDaemonProcess(string) error {
	return fmt.Errorf("background daemon is only supported on Unix")
}
