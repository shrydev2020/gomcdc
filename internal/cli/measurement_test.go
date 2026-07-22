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
	"github.com/shrydev2020/gomcdc/internal/gotest"
	"github.com/shrydev2020/gomcdc/internal/instrument"
	"github.com/shrydev2020/gomcdc/internal/loader"
	"github.com/shrydev2020/gomcdc/internal/modulecontext"
	"github.com/shrydev2020/gomcdc/internal/report"
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
	moduleSettings, err := modulecontext.SnapshotModule(filepath.Join(module, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\nwhile :; do :; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	outcome, measurementWork, err := measure(measurementRequest{
		context: ctx,
		loaded: loader.Result{
			ModulePath: "example.test/m", ModuleRoot: module, RelativeWorkDir: ".",
			PackageImportSet: []string{"example.test/m"}, CoverPackageImportSet: []string{"example.test/m"},
			ModuleSettings: moduleSettings,
		},
		decisions: []cover.DecisionMetadata{{ID: 1, Package: "example.test/m"}},
		needsC0:   true,
		needsAST:  true,
	}, io.Discard)
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	defer func() {
		if cleanupErr := measurementWork.finalize(io.Discard); cleanupErr != nil {
			t.Errorf("cleanup: %v", cleanupErr)
		}
	}()
	if !outcome.interrupted {
		t.Fatal("measurement was not classified as interrupted")
	}
	if measurementWork == nil || measurementWork.measurement != "combined" {
		t.Fatalf("measurement workspace = %#v, want combined", measurementWork)
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

func TestMeasurementResultsPreserveIndependentExecutionFacts(t *testing.T) {
	t.Parallel()
	interrupted := measurementOutcome{interrupted: true}
	if got := interrupted.results(false, nil); got != (report.RunResults{
		Test: report.ResultNotRun, Measurement: report.ResultFailed, Integrity: report.ResultNotRun,
		Strict: report.ResultNotRequested, Threshold: report.ResultNotRequested,
	}) {
		t.Fatalf("pre-test interruption results = %#v", got)
	}

	completed := measurementOutcome{
		testResult:       &gotest.Result{Status: cover.RunPassed},
		integrityChecked: true,
	}
	if got := completed.results(false, errors.New("cleanup failed")); got != (report.RunResults{
		Test: report.ResultPassed, Measurement: report.ResultFailed, Integrity: report.ResultPassed,
		Strict: report.ResultNotRequested, Threshold: report.ResultNotRequested,
	}) {
		t.Fatalf("cleanup failure results = %#v", got)
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

func TestRuntimeProducerOutcomeKeepsAxesIndependent(t *testing.T) {
	t.Parallel()

	passed := &gotest.Result{Status: cover.RunPassed}
	failed := &gotest.Result{Status: cover.RunFailed}
	tests := []struct {
		name          string
		run           *gotest.Result
		collectionErr error
		mappingErr    error
		diagnosticErr error
		diagnostics   []runtimecov.Diagnostic
		want          report.ProducerOutcome
	}{
		{
			name: "complete", run: passed,
			want: report.ProducerOutcome{
				Integrity: report.ProducerIntegrityValid, Completeness: report.ProducerCompletenessComplete,
				Mapping: report.ProducerMappingComplete, Usability: report.ProducerUsabilityAccepted,
			},
		},
		{
			name: "partial execution", run: failed,
			want: report.ProducerOutcome{
				Integrity: report.ProducerIntegrityValid, Completeness: report.ProducerCompletenessPartial,
				Mapping: report.ProducerMappingComplete, Usability: report.ProducerUsabilityAcceptedPartial,
			},
		},
		{
			name: "valid prefix after failed run", run: failed,
			diagnostics: []runtimecov.Diagnostic{{Severity: runtimecov.DiagnosticRecoverable}},
			want: report.ProducerOutcome{
				Integrity: report.ProducerIntegrityValidPrefix, Completeness: report.ProducerCompletenessPartial,
				Mapping: report.ProducerMappingComplete, Usability: report.ProducerUsabilityAcceptedPartial,
			},
		},
		{
			name: "valid prefix contradicts complete run", run: passed,
			diagnostics: []runtimecov.Diagnostic{{Severity: runtimecov.DiagnosticRecoverable}},
			want: report.ProducerOutcome{
				Integrity: report.ProducerIntegrityValidPrefix, Completeness: report.ProducerCompletenessPartial,
				Mapping: report.ProducerMappingComplete, Usability: report.ProducerUsabilityRejected,
			},
		},
		{
			name: "mapping rejected independently", run: passed, mappingErr: errors.New("unknown ID"),
			want: report.ProducerOutcome{
				Integrity: report.ProducerIntegrityValid, Completeness: report.ProducerCompletenessComplete,
				Mapping: report.ProducerMappingInvalid, Usability: report.ProducerUsabilityRejected,
			},
		},
		{
			name: "corrupt transport", run: failed,
			diagnostics: []runtimecov.Diagnostic{{Severity: runtimecov.DiagnosticIntegrity}},
			want: report.ProducerOutcome{
				Integrity: report.ProducerIntegrityInvalid, Completeness: report.ProducerCompletenessPartial,
				Mapping: report.ProducerMappingComplete, Usability: report.ProducerUsabilityRejected,
			},
		},
		{
			name: "collection unavailable", run: passed, collectionErr: errors.New("read"),
			want: report.ProducerOutcome{
				Integrity: report.ProducerIntegrityUnavailable, Completeness: report.ProducerCompletenessUnavailable,
				Mapping: report.ProducerMappingUnavailable, Usability: report.ProducerUsabilityRejected,
			},
		},
		{
			name: "run unavailable",
			want: report.ProducerOutcome{
				Integrity: report.ProducerIntegrityUnavailable, Completeness: report.ProducerCompletenessUnavailable,
				Mapping: report.ProducerMappingUnavailable, Usability: report.ProducerUsabilityRejected,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := runtimeProducerOutcome(
				report.ProducerASTRuntime,
				test.run,
				test.collectionErr,
				test.mappingErr,
				test.diagnosticErr,
				test.diagnostics,
			)
			test.want.Producer = report.ProducerASTRuntime
			if got != test.want {
				t.Fatalf("outcome = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestRuntimeAcceptancePreservesProducerOwnership(t *testing.T) {
	t.Parallel()

	decisions := []cover.DecisionMetadata{{ID: 1, Package: "example.test/p"}}
	clauses := []cover.ClauseMetadata{{
		ID: 10, SwitchID: 100, Package: "example.test/p",
		Kind: cover.ClauseExpressionSwitch,
	}}
	tests := []struct {
		name         string
		recorded     runtimecov.RecordedEvidence
		wantAST      bool
		wantCompiler bool
	}{
		{
			name: "invalid decision belongs only to AST runtime",
			recorded: runtimecov.RecordedEvidence{Evaluations: []cover.DecisionEvaluation{{
				DecisionID: 99,
			}}},
			wantAST: true,
		},
		{
			name: "invalid direct selection belongs only to compiler selection",
			recorded: runtimecov.RecordedEvidence{ClauseEvents: []runtimecov.RecordedClauseEvent{{
				RunID: "run", PackagePath: "example.test/p", ProcessID: 1,
				ClauseID: 99, Event: cover.ClauseDirectSelection,
			}}},
			wantCompiler: true,
		},
		{
			name: "invalid process file is shared transport evidence",
			recorded: runtimecov.RecordedEvidence{Files: []runtimecov.ProcessFile{{
				Path: "events.jsonl", RunID: "other", PackagePath: "example.test/p", ProcessID: 1,
			}}},
			wantAST: true, wantCompiler: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, issues := acceptRuntimeEvidenceByProducer(
				context.Background(), decisions, clauses, test.recorded, "run", nil,
			)
			if got := issues.astErr() != nil; got != test.wantAST {
				t.Fatalf("AST mapping error = %t, want %t (%v)", got, test.wantAST, issues.astErr())
			}
			if got := issues.compilerErr() != nil; got != test.wantCompiler {
				t.Fatalf("compiler mapping error = %t, want %t (%v)", got, test.wantCompiler, issues.compilerErr())
			}
		})
	}
}

func TestGoCoverProducerOutcomeSeparatesMappingFromIntegrity(t *testing.T) {
	t.Parallel()

	passed := &gotest.Result{Status: cover.RunPassed}
	if got := goCoverProducerOutcome(passed, nil, errors.New("unknown region")); got != (report.ProducerOutcome{
		Producer: report.ProducerGoCover, Integrity: report.ProducerIntegrityValid,
		Completeness: report.ProducerCompletenessComplete, Mapping: report.ProducerMappingInvalid,
		Usability: report.ProducerUsabilityRejected,
	}) {
		t.Fatalf("mapping failure outcome = %#v", got)
	}
	if got := goCoverProducerOutcome(passed, os.ErrNotExist, nil); got.Integrity != report.ProducerIntegrityUnavailable ||
		got.Mapping != report.ProducerMappingUnavailable || got.Usability != report.ProducerUsabilityRejected {
		t.Fatalf("missing profile outcome = %#v", got)
	}
}
