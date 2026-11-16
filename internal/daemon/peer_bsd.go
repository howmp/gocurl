//go:build darwin || freebsd

package daemon

import (
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func peerAllowedPlatform(conn net.Conn) error {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("daemon peer is not a Unix connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return err
	}
	var cred *unix.Xucred
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		cred, sockErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return err
	}
	if sockErr != nil {
		return sockErr
	}
	if cred == nil || int(cred.Uid) != os.Getuid() {
		return fmt.Errorf("daemon peer uid mismatch")
	}
	return nil
}
