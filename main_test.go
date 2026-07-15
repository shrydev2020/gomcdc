package main

import (
	"context"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestRunWithSignalsCancelsAndReturnsSignalExitCode(t *testing.T) {
	t.Parallel()

	signals := make(chan os.Signal, 1)
	started := make(chan struct{})
	var stopped atomic.Bool
	result := make(chan int, 1)
	go func() {
		result <- runWithSignals(signals, func() { stopped.Store(true) }, func(ctx context.Context) int {
			close(started)
			<-ctx.Done()
			if !stopped.Load() {
				t.Error("signal notifications were not stopped before cancellation")
			}
			return 99
		})
	}()

	<-started
	signals <- os.Interrupt
	select {
	case code := <-result:
		if code != 128+int(syscall.SIGINT) {
			t.Fatalf("exit code = %d, want %d", code, 128+int(syscall.SIGINT))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("signal cancellation did not stop the run")
	}
}

func TestRunWithSignalsPreservesOrdinaryExit(t *testing.T) {
	t.Parallel()

	signals := make(chan os.Signal, 1)
	if code := runWithSignals(signals, func() {}, func(context.Context) int { return 7 }); code != 7 {
		t.Fatalf("exit code = %d, want 7", code)
	}
}

func TestExitCodeForSIGTERM(t *testing.T) {
	t.Parallel()

	if code := exitCodeForSignal(syscall.SIGTERM); code != 128+int(syscall.SIGTERM) {
		t.Fatalf("exit code = %d, want %d", code, 128+int(syscall.SIGTERM))
	}
}
