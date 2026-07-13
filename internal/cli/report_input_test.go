package cli

import (
	"testing"

	"github.com/shrydev2020/gomcdc/internal/config"
	cover "github.com/shrydev2020/gomcdc/internal/coverage"
	"github.com/shrydev2020/gomcdc/internal/gotest"
	"github.com/shrydev2020/gomcdc/internal/loader"
	"github.com/shrydev2020/gomcdc/internal/report"
	"github.com/shrydev2020/gomcdc/internal/runtimecov"
)

func TestAssembleReportInputPreservesMeasurementBoundaries(t *testing.T) {
	input := assembleReportInput(reportAssembly{
		loaded:                 loader.Result{ModulePath: "example.test/m", PackageImportSet: []string{"example.test/m/p"}},
		coverage:               config.AllCoverage(),
		standardResult:         &gotest.Result{Status: cover.RunPassed, FailureKind: cover.RunFailureNone, Packages: map[string]gotest.PackageStatus{"example.test/m/p": gotest.PackagePassed}},
		astResult:              &gotest.Result{Status: cover.RunFailed, FailureKind: cover.RunFailureTest, Packages: map[string]gotest.PackageStatus{"example.test/m/p": gotest.PackageFailed}},
		standardCoverRequested: true,
		astRequested:           true,
		collection:             runtimecov.Collection{},
	})

	if input.MeasurementMode != "dual-run-standard-cover" {
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
	if len(input.Measurements) != 2 || input.Measurements[0].Name != "standard-cover" || input.Measurements[1].Name != "ast" {
		t.Fatalf("measurements = %#v", input.Measurements)
	}
}
