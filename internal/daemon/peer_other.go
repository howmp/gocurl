//go:build !linux && !darwin && !freebsd

package daemon

import (
	"fmt"
	"net"
)

func peerAllowedPlatform(net.Conn) error {
	return fmt.Errorf("daemon peer credentials are not supported on this platform")
}
