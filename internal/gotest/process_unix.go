//go:build unix

package gotest

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configureCancellation gives the go command and every package test binary a
// private process group. A wrapper timeout must not leave instrumented test
// processes running and writing into a workspace after collection starts.
func configureCancellation(command *exec.Cmd) {
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
