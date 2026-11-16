//go:build unix

package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func startDaemonProcess(socket string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logPath := daemonLogPath()
	if err := ensurePrivateDir(filepath.Dir(logPath)); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "--daemon",
		"--socket", socket,
		"--idle-timeout", daemonIdleTimeout().String(),
		"--log", logPath,
	)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	_ = cmd.Process.Release()
	_ = logFile.Close()
	return nil
}
