package cli

import (
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/analysis"
	"github.com/shrydev2020/gomcdc/v2/internal/config"
	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
	"github.com/shrydev2020/gomcdc/v2/internal/evidence"
	"github.com/shrydev2020/gomcdc/v2/internal/gotest"
	"github.com/shrydev2020/gomcdc/v2/internal/loader"
	"github.com/shrydev2020/gomcdc/v2/internal/mcdc"
)

func TestAssembleReportInputUsesOneCombinedMeasurement(t *testing.T) {
	input := assembleAnalysisInput(analysisAssembly{
		loaded:                 loader.Result{ModulePath: "example.test/m", PackageImportSet: []string{"example.test/m/p"}},
		coverage:               config.AllCoverage(),
		testResult:             &gotest.Result{Status: cover.RunFailed, FailureKind: cover.RunFailureTest, Packages: map[string]gotest.PackageStatus{"example.test/m/p": gotest.PackageFailed}},
		standardCoverRequested: true,
		astRequested:           true,
		producerOutcomes: []analysis.ProducerOutcome{{
			Producer: analysis.ProducerASTRuntime, Integrity: analysis.ProducerIntegrityValid,
			Completeness: analysis.ProducerCompletenessPartial, Mapping: analysis.ProducerMappingComplete,
			Usability: analysis.ProducerUsabilityAcceptedPartial,
		}},
		results: analysis.RunResults{
			Test: analysis.ResultFailed, Measurement: analysis.ResultPassed, Integrity: analysis.ResultPassed,
			Strict: analysis.ResultNotRequested, Threshold: analysis.ResultNotRequested,
		},
		maskingAnalysisBudget: mcdc.AnalysisBudget{MaxSearchStates: 123},
		evidence:              evidence.Runtime{},
	})

	if input.MeasurementMode != analysis.MeasurementSingleRun {
		t.Fatalf("measurement mode = %q", input.MeasurementMode)
	}
	if input.RunStatus != cover.RunFailed || input.FailureKind != cover.RunFailureTest || input.Complete {
		t.Fatalf("run state = %#v", input)
	}
	if input.Results != (analysis.RunResults{
		Test: analysis.ResultFailed, Measurement: analysis.ResultPassed, Integrity: analysis.ResultPassed,
		Strict: analysis.ResultNotRequested, Threshold: analysis.ResultNotRequested,
	}) {
		t.Fatalf("independent results = %#v", input.Results)
	}
	if got := input.PackageStatuses["example.test/m/p"]; got != string(gotest.PackageFailed) {
		t.Fatalf("package status = %q", got)
	}
	if len(input.Measurements) != 1 || input.Measurements[0].Name != "combined" {
		t.Fatalf("measurements = %#v", input.Measurements)
	}
	if input.MaskingAnalysisBudget.MaxSearchStates != 123 {
		t.Fatalf("Masking analysis budget = %#v", input.MaskingAnalysisBudget)
	}
	if len(input.Errors) != 1 || input.Errors[0].Message != "combined go test failed" {
		t.Fatalf("combined measurement errors = %#v", input.Errors)
	}
}

func TestAssembleReportInputClassifiesCallerInterruption(t *testing.T) {
	t.Parallel()

	input := assembleAnalysisInput(analysisAssembly{
		loaded:      loader.Result{ModulePath: "example.test/m"},
		interrupted: true,
		results: analysis.RunResults{
			Test: analysis.ResultNotRun, Measurement: analysis.ResultFailed, Integrity: analysis.ResultNotRun,
			Strict: analysis.ResultNotRequested, Threshold: analysis.ResultNotRequested,
		},
	})
	if input.RunStatus != cover.RunFailed || input.FailureKind != cover.RunFailureInterrupted || input.Complete {
		t.Fatalf("interrupted run state = %#v", input)
	}
	if input.Results.Test != analysis.ResultNotRun || input.Results.Measurement != analysis.ResultFailed || input.Results.Integrity != analysis.ResultNotRun {
		t.Fatalf("interrupted results = %#v", input.Results)
	}
	if len(input.PackageStatuses) != 0 || len(input.Measurements) != 0 {
		t.Fatalf("not-run test exposed execution results: packages=%#v measurements=%#v", input.PackageStatuses, input.Measurements)
	}
}

func TestAssembleReportInputRecordsCleanupAsMeasurementFailure(t *testing.T) {
	t.Parallel()
	input := assembleAnalysisInput(analysisAssembly{
		loaded:     loader.Result{ModulePath: "example.test/m"},
		testResult: &gotest.Result{Status: cover.RunPassed},
		results: analysis.RunResults{
			Test: analysis.ResultPassed, Measurement: analysis.ResultFailed, Integrity: analysis.ResultPassed,
			Strict: analysis.ResultNotRequested, Threshold: analysis.ResultNotRequested,
		},
		producerOutcomes: []analysis.ProducerOutcome{{
			Producer: analysis.ProducerGoCover, Integrity: analysis.ProducerIntegrityValid,
			Completeness: analysis.ProducerCompletenessComplete, Mapping: analysis.ProducerMappingComplete,
			Usability: analysis.ProducerUsabilityAccepted,
		}},
		errors: []analysis.ReportError{{
			Phase: "cleanup", Code: "workspace-cleanup-failed", Message: "temporary workspace cleanup failed",
		}},
	})
	if input.Results.Test != analysis.ResultPassed || input.Results.Measurement != analysis.ResultFailed || input.Results.Integrity != analysis.ResultPassed {
		t.Fatalf("cleanup results = %#v", input.Results)
	}
	if input.Complete || len(input.Errors) != 1 || input.Errors[0].Code != "workspace-cleanup-failed" {
		t.Fatalf("cleanup completion/errors = complete %v errors %#v", input.Complete, input.Errors)
	}
}
