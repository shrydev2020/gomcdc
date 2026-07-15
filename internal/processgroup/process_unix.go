//go:build unix

// Package processgroup keeps subprocess descendants inside one cancellation
// boundary on Unix systems.
package processgroup

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// ConfigureCancellation gives command and its descendants a private process
// group and makes context cancellation terminate that entire group.
func ConfigureCancellation(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
