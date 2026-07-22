package cli

import (
	"context"

	"github.com/shrydev2020/gomcdc/internal/backend"
	"github.com/shrydev2020/gomcdc/internal/c0"
	"github.com/shrydev2020/gomcdc/internal/config"
	cover "github.com/shrydev2020/gomcdc/internal/coverage"
	"github.com/shrydev2020/gomcdc/internal/gotest"
	"github.com/shrydev2020/gomcdc/internal/loader"
	"github.com/shrydev2020/gomcdc/internal/mcdc"
	"github.com/shrydev2020/gomcdc/internal/report"
)

// reportAssembly is the validated boundary between measurement orchestration
// and report construction. It contains no workspace or writer ownership: the
// caller owns those resources and supplies only their results here.
type reportAssembly struct {
	context                context.Context
	toolVersion            string
	loaded                 loader.Result
	sources                []sourceInstrumentation
	coverage               config.CoverageSet
	maskingAnalysisBudget  mcdc.AnalysisBudget
	decisions              []cover.DecisionMetadata
	clauses                []cover.ClauseMetadata
	noMatches              []cover.NoMatchMetadata
	evidence               acceptedRuntimeEvidence
	c0                     *c0.Report
	testResult             *gotest.Result
	measurementName        string
	standardCoverRequested bool
	astRequested           bool
	producerOutcomes       []report.ProducerOutcome
	instrumentationUnknown int
	results                report.RunResults
	interrupted            bool
	errors                 []report.ReportError
	measurementDiagnostics []measurementDiagnostic
}

// assembleReportInput turns measurement results into the canonical report
// input. Package status merging and measurement-mode selection belong here so
// runCoverage remains responsible for sequencing and exit-code policy only.
func assembleReportInput(assembly reportAssembly) report.Input {
	overallStatus := cover.RunPassed
	overallFailure := cover.RunFailureNone
	if assembly.testResult != nil {
		overallStatus = assembly.testResult.Status
		overallFailure = assembly.testResult.FailureKind
	}
	if assembly.interrupted {
		overallStatus = cover.RunFailed
		overallFailure = cover.RunFailureInterrupted
	}
	packageStatuses := packageStatuses(assembly.loaded, assembly.testResult, overallStatus)
	astPackageStatuses := astPackageStatuses(assembly.loaded, assembly.testResult, assembly.astRequested)
	measurementName := assembly.measurementName
	if measurementName == "" {
		measurementName = requestedMeasurementName(assembly.standardCoverRequested, assembly.astRequested)
	}

	errors := append([]report.ReportError(nil), assembly.errors...)
	for _, diagnostic := range assembly.measurementDiagnostics {
		errors = append(errors, report.ReportError{
			Phase: diagnostic.phase, Code: diagnostic.code, Message: diagnostic.message,
		})
	}
	errors = append(errors, measurementRunErrors(measurementName, assembly.testResult)...)
	results := assembly.results

	return report.Input{
		Context:                assembly.context,
		ToolVersion:            assembly.toolVersion,
		ModulePath:             assembly.loaded.ModulePath,
		SourceFiles:            sourceFileInputs(assembly.sources),
		Coverage:               assembly.coverage,
		MaskingAnalysisBudget:  assembly.maskingAnalysisBudget,
		Decisions:              assembly.decisions,
		Evaluations:            assembly.evidence.Evaluations,
		NotEvaluatedDecisions:  assembly.evidence.NotEvaluatedDecisions,
		Clauses:                assembly.clauses,
		NoMatches:              assembly.noMatches,
		ClauseObservations:     assembly.evidence.ClauseObservations,
		C0:                     assembly.c0,
		RunStatus:              overallStatus,
		FailureKind:            overallFailure,
		Results:                results,
		MeasurementMode:        measurementMode(assembly.standardCoverRequested, assembly.astRequested),
		Measurements:           measurementRuns(measurementName, assembly.testResult),
		ProducerOutcomes:       assembly.producerOutcomes,
		Backend:                backend.OrchestratedBackend{},
		BackendProducers:       backend.OrchestratedProducers(),
		InstrumentationUnknown: assembly.instrumentationUnknown,
		Errors:                 errors,
		Complete:               overallStatus == cover.RunPassed && results.Measurement == report.ResultPassed && results.Integrity == report.ResultPassed,
		PackageStatuses:        packageStatuses,
		ASTPackageStatuses:     astPackageStatuses,
	}
}

func testResultStatus(result *gotest.Result) report.ResultStatus {
	if result == nil {
		return report.ResultNotRun
	}
	switch result.Status {
	case cover.RunPassed:
		return report.ResultPassed
	case cover.RunFailed:
		return report.ResultFailed
	case cover.RunTimeout:
		return report.ResultTimeout
	default:
		return report.ResultNotRun
	}
}

func passFailResult(passed bool) report.ResultStatus {
	if passed {
		return report.ResultPassed
	}
	return report.ResultFailed
}

func measurementRunErrors(name string, result *gotest.Result) []report.ReportError {
	if result == nil || result.Status == cover.RunPassed {
		return nil
	}
	code := "go-test-" + string(result.FailureKind)
	message := name + " go test failed"
	if result.FailureKind == cover.RunFailureInterrupted {
		message = name + " go test interrupted"
	} else if result.Status == cover.RunTimeout {
		message = name + " go test timed out"
	}
	return []report.ReportError{{Phase: "test", Code: code, Message: message}}
}

func measurementMode(standardCoverRequested, astRequested bool) report.MeasurementMode {
	switch {
	case standardCoverRequested:
		if astRequested {
			return report.MeasurementSingleRun
		}
		return report.MeasurementStandardCover
	default:
		return report.MeasurementSingleRun
	}
}

func requestedMeasurementName(standardCoverRequested, astRequested bool) string {
	switch {
	case standardCoverRequested && astRequested:
		return "combined"
	case astRequested:
		return "ast"
	default:
		return "standard-cover"
	}
}

func packageStatuses(loaded loader.Result, result *gotest.Result, overall cover.RunStatus) map[string]string {
	statuses := make(map[string]string, len(loaded.PackageImportSet))
	if result == nil {
		return statuses
	}
	for _, packagePath := range loaded.PackageImportSet {
		statuses[packagePath] = string(overall)
	}
	if result != nil {
		for packagePath, status := range result.Packages {
			statuses[packagePath] = string(status)
		}
	}
	return statuses
}

func astPackageStatuses(loaded loader.Result, result *gotest.Result, requested bool) map[string]string {
	statuses := make(map[string]string, len(loaded.PackageImportSet))
	if !requested || result == nil {
		return statuses
	}
	for _, packagePath := range loaded.PackageImportSet {
		statuses[packagePath] = string(result.Status)
	}
	for packagePath, status := range result.Packages {
		statuses[packagePath] = string(status)
	}
	return statuses
}
