package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shrydev2020/gomcdc/internal/analyzer"
	"github.com/shrydev2020/gomcdc/internal/c0"
	"github.com/shrydev2020/gomcdc/internal/c0map"
	cover "github.com/shrydev2020/gomcdc/internal/coverage"
	"github.com/shrydev2020/gomcdc/internal/instrument"
	"github.com/shrydev2020/gomcdc/internal/loader"
	"github.com/shrydev2020/gomcdc/internal/runtimecov"
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

func TestMeasureUsesOneCombinedWorkspaceWhenInterrupted(t *testing.T) {
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
	if len(workspaces.items) != 1 {
		t.Fatalf("workspace count = %d, want one combined measurement workspace", len(workspaces.items))
	}
	if workspaces.items[0].measurement != "combined" {
		t.Fatalf("measurement = %q, want combined", workspaces.items[0].measurement)
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

func TestMeasurementCoverageHelpers(t *testing.T) {
	t.Parallel()

	if got := runtimeDataDirEnv(false); got != "" {
		t.Fatalf("disabled runtime data environment = %q", got)
	}
	if got := runtimeDataDirEnv(true); got != runtimecov.DataDirEnv {
		t.Fatalf("enabled runtime data environment = %q", got)
	}
	paths := generatedPaths([]c0map.GeneratedFile{{Path: "z/z.go"}, {Path: "a/a.go"}})
	if len(paths) != 2 || paths[0] != "a/a.go" || paths[1] != "z/z.go" {
		t.Fatalf("generated paths = %#v", paths)
	}

	profilePath := filepath.Join(t.TempDir(), "cover.out")
	if err := os.WriteFile(profilePath, []byte("mode: set\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile, err := readCoverProfile(profilePath)
	if err != nil || profile.Mode != c0.ModeSet {
		t.Fatalf("read cover profile = %#v, %v", profile, err)
	}
	if _, err := readCoverProfile(filepath.Join(t.TempDir(), "missing.out")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing cover profile error = %v", err)
	}
	invalidPath := filepath.Join(t.TempDir(), "invalid.out")
	if err := os.WriteFile(invalidPath, []byte("invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCoverProfile(invalidPath); err == nil {
		t.Fatal("invalid cover profile was accepted")
	}
}

func TestSourceCoveragePlansUseInventoryAuthority(t *testing.T) {
	t.Parallel()

	sourceText := []byte("package p\n\nfunc Value() int { return 1 }\n")
	inventory, err := c0.BuildInventory("p/p.go", sourceText)
	if err != nil {
		t.Fatal(err)
	}
	source := sourceInstrumentation{
		loaded: loader.File{PackagePath: "example.test/m/p"},
		analysis: analyzer.File{
			RelativePath: "p/p.go",
			Source:       sourceText,
		},
		inventory: &inventory,
	}
	plans, err := sourceCoveragePlans(context.Background(), []sourceInstrumentation{source}, nil)
	if err != nil || len(plans) != 1 || plans[0].OriginalPath != "p/p.go" {
		t.Fatalf("identity plans = %#v, %v", plans, err)
	}
	instrumented := []instrument.PackageResult{{CoveragePlans: []instrument.FileCoveragePlan{{
		OriginalPath: "p/p.go", Correspondence: plans[0].Correspondence,
	}}}}
	plans, err = sourceCoveragePlans(context.Background(), []sourceInstrumentation{source}, instrumented)
	if err != nil || len(plans) != 1 {
		t.Fatalf("instrumented plans = %#v, %v", plans, err)
	}
	if _, err := sourceCoveragePlans(context.Background(), []sourceInstrumentation{source}, append(instrumented, instrumented...)); err == nil {
		t.Fatal("duplicate correspondence was accepted")
	}
	unknown := []instrument.PackageResult{{CoveragePlans: []instrument.FileCoveragePlan{{
		OriginalPath: "other/other.go", Correspondence: plans[0].Correspondence,
	}}}}
	if _, err := sourceCoveragePlans(context.Background(), []sourceInstrumentation{source}, unknown); err == nil {
		t.Fatal("correspondence without original inventory was accepted")
	}
	withoutInventory := source
	withoutInventory.inventory = nil
	if plans, err := sourceCoveragePlans(context.Background(), []sourceInstrumentation{withoutInventory}, nil); err != nil || len(plans) != 0 {
		t.Fatalf("source without C0 inventory produced plans: %#v, %v", plans, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sourceCoveragePlans(canceled, []sourceInstrumentation{source}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled coverage planning error = %v", err)
	}
}
