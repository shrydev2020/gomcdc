//go:build !unix

package gotest

import "os/exec"

func configureCancellation(_ *exec.Cmd) {}
