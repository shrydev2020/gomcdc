package cli

import (
	"testing"

	"github.com/shrydev2020/gomcdc/internal/config"
	cover "github.com/shrydev2020/gomcdc/internal/coverage"
	"github.com/shrydev2020/gomcdc/internal/gotest"
	"github.com/shrydev2020/gomcdc/internal/loader"
	"github.com/shrydev2020/gomcdc/internal/mcdc"
	"github.com/shrydev2020/gomcdc/internal/report"
)

func TestAssembleReportInputUsesOneCombinedMeasurement(t *testing.T) {
	input := assembleReportInput(reportAssembly{
		loaded:                 loader.Result{ModulePath: "example.test/m", PackageImportSet: []string{"example.test/m/p"}},
		coverage:               config.AllCoverage(),
		testResult:             &gotest.Result{Status: cover.RunFailed, FailureKind: cover.RunFailureTest, Packages: map[string]gotest.PackageStatus{"example.test/m/p": gotest.PackageFailed}},
		standardCoverRequested: true,
		astRequested:           true,
		maskingAnalysisBudget:  mcdc.AnalysisBudget{MaxSearchStates: 123},
		evidence:               acceptedRuntimeEvidence{},
	})

	if input.MeasurementMode != report.MeasurementSingleRun {
		t.Fatalf("measurement mode = %q", input.MeasurementMode)
	}
	if input.RunStatus != cover.RunFailed || input.FailureKind != cover.RunFailureTest || input.Complete {
		t.Fatalf("run state = %#v", input)
	}
	if input.Results != (report.RunResults{
		Test: report.ResultFailed, Measurement: report.ResultPassed, Integrity: report.ResultPassed,
		Strict: report.ResultNotRequested, Threshold: report.ResultNotRequested,
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

	input := assembleReportInput(reportAssembly{
		loaded:      loader.Result{ModulePath: "example.test/m"},
		interrupted: true,
	})
	if input.RunStatus != cover.RunFailed || input.FailureKind != cover.RunFailureInterrupted || input.Complete {
		t.Fatalf("interrupted run state = %#v", input)
	}
	if input.Results.Test != report.ResultFailed || input.Results.Measurement != report.ResultFailed {
		t.Fatalf("interrupted results = %#v", input.Results)
	}
}
