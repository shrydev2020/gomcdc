//go:build !unix

// Package processgroup keeps subprocess descendants inside one cancellation
// boundary where the host operating system supports process groups.
package processgroup

import "os/exec"

// ConfigureCancellation leaves the platform's CommandContext behavior in
// place when Unix process groups are unavailable.
func ConfigureCancellation(_ *exec.Cmd) {}
