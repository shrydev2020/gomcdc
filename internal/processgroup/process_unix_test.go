//go:build unix

package processgroup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestConfigureCancellationTerminatesDescendants(t *testing.T) {
	t.Parallel()

	pidFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(ctx, "/bin/sh", "-c", `sleep 30 & child=$!; printf '%s\n' "$child" > "$PID_FILE"; wait`)
	command.Env = append(os.Environ(), "PID_FILE="+pidFile)
	ConfigureCancellation(command)
	done := make(chan error, 1)
	go func() { done <- command.Run() }()

	childPID := waitForPID(t, pidFile)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled command did not exit")
	}
	waitForProcessExit(t, childPID)
}

func waitForPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		contents, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(contents)))
			if parseErr != nil {
				t.Fatalf("parse child PID: %v", parseErr)
			}
			return pid
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read child PID: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("child PID was not recorded")
	return 0
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("descendant process %d survived cancellation", pid)
}
