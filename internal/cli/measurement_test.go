package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	cover "github.com/shrydev2020/gomcdc/internal/coverage"
	"github.com/shrydev2020/gomcdc/internal/loader"
)

func TestRecoveryContextSurvivesRequestCancellationUntilDeadline(t *testing.T) {
	t.Parallel()

	request, cancelRequest := context.WithCancel(context.Background())
	recovery, cancelRecovery := newRecoveryContext(request, 30*time.Millisecond)
	defer cancelRecovery()
	cancelRequest()
	select {
	case <-recovery.Done():
		t.Fatal("recovery was canceled immediately with the request")
	case <-time.After(5 * time.Millisecond):
	}
	select {
	case <-recovery.Done():
		if !errors.Is(recovery.Err(), context.Canceled) {
			t.Fatalf("recovery error = %v, want cancellation after deadline", recovery.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("recovery deadline did not stop finalization")
	}
}

func TestMeasureDoesNotStartASTPreparationAfterStandardRunInterruption(t *testing.T) {
	module := t.TempDir()
	if err := os.WriteFile(filepath.Join(module, "go.mod"), []byte("module example.test/m\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\nwhile :; do :; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	outcome, workspaces, err := measure(measurementRequest{
		context: ctx,
		loaded: loader.Result{
			ModulePath: "example.test/m", ModuleRoot: module, RelativeWorkDir: ".",
			PackageImportSet: []string{"example.test/m"}, CoverPackageImportSet: []string{"example.test/m"},
		},
		decisions: []cover.DecisionMetadata{{ID: 1, Package: "example.test/m"}},
		needsC0:   true,
		needsAST:  true,
	}, io.Discard)
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	defer func() {
		if cleanupErr := workspaces.cleanup(io.Discard); cleanupErr != nil {
			t.Errorf("cleanup: %v", cleanupErr)
		}
	}()
	if !outcome.interrupted {
		t.Fatal("measurement was not classified as interrupted")
	}
	var astModule string
	for _, item := range workspaces.items {
		if item.measurement == "ast" {
			astModule = item.workspace.ModuleDir
		}
	}
	if astModule == "" {
		t.Fatal("AST workspace is missing")
	}
	if _, statErr := os.Stat(filepath.Join(astModule, "internal")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("AST preparation started after interruption: %v", statErr)
	}
}

func TestRecoveryContextStartsWithDeadlineAfterInterruption(t *testing.T) {
	t.Parallel()

	request, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	recovery, cancelRecovery := newRecoveryContext(request, time.Second)
	defer cancelRecovery()
	if _, ok := recovery.Deadline(); !ok {
		t.Fatal("interrupted recovery has no deadline")
	}
}
