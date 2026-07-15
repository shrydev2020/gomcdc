// Command gomcdc runs Go tests and reports statement, function, decision,
// condition, clause, Unique-Cause MC/DC, and Masking MC/DC coverage.
package main

import (
	"context"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	"github.com/shrydev2020/gomcdc/internal/cli"
)

func main() {
	os.Exit(runMain(os.Args[1:], os.Stdout, os.Stderr))
}

func runMain(args []string, stdout, stderr *os.File) int {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	return runWithSignals(signals, func() { signal.Stop(signals) }, func(ctx context.Context) int {
		return cli.Run(ctx, args, stdout, stderr)
	})
}

// runWithSignals converts the first termination signal into cooperative
// cancellation. stop restores the platform's default signal behavior before
// cancel is called, so a second signal remains an immediate forced exit.
func runWithSignals(signals <-chan os.Signal, stop func(), run func(context.Context) int) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	finished := make(chan struct{})
	handlerDone := make(chan struct{})
	var signalCode atomic.Int32
	go func() {
		defer close(handlerDone)
		select {
		case received := <-signals:
			signalCode.Store(int32(exitCodeForSignal(received)))
			stop()
			cancel()
		case <-finished:
		}
	}()

	code := run(ctx)
	close(finished)
	stop()
	<-handlerDone
	if interrupted := signalCode.Load(); interrupted != 0 {
		return int(interrupted)
	}
	return code
}

func exitCodeForSignal(received os.Signal) int {
	switch received {
	case os.Interrupt:
		return 128 + int(syscall.SIGINT)
	case syscall.SIGTERM:
		return 128 + int(syscall.SIGTERM)
	default:
		return 1
	}
}
