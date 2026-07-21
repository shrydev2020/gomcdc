package cli

import (
	"github.com/shrydev2020/gomcdc/internal/backend"
	"github.com/shrydev2020/gomcdc/internal/c0"
	"github.com/shrydev2020/gomcdc/internal/config"
	cover "github.com/shrydev2020/gomcdc/internal/coverage"
	"github.com/shrydev2020/gomcdc/internal/gotest"
	"github.com/shrydev2020/gomcdc/internal/loader"
	"github.com/shrydev2020/gomcdc/internal/report"
)

// reportAssembly is the validated boundary between measurement orchestration
// and report construction. It contains no workspace or writer ownership: the
// caller owns those resources and supplies only their results here.
type reportAssembly struct {
	toolVersion            string
	loaded                 loader.Result
	sources                []sourceInstrumentation
	coverage               config.CoverageSet
	decisions              []cover.DecisionMetadata
	clauses                []cover.ClauseMetadata
	noMatches              []cover.NoMatchMetadata
	evidence               acceptedRuntimeEvidence
	c0                     *c0.Report
	standardResult         *gotest.Result
	astResult              *gotest.Result
	standardCoverRequested bool
	astRequested           bool
	astEvidenceUnknown     bool
	c0EvidenceUnknown      bool
	instrumentationUnknown int
	integrityFailure       bool
	interrupted            bool
	analysisIncomplete     bool
	errors                 []report.ReportError
	measurementDiagnostics []measurementDiagnostic
}

// assembleReportInput turns measurement results into the canonical report
// input. Package status merging and measurement-mode selection belong here so
// runCoverage remains responsible for sequencing and exit-code policy only.
func assembleReportInput(assembly reportAssembly) report.Input {
	overallStatus, overallFailure := combineTestResults(assembly.standardResult, assembly.astResult)
	if assembly.interrupted {
		overallStatus = cover.RunFailed
		overallFailure = cover.RunFailureInterrupted
	}
	packageStatuses := packageStatuses(assembly.loaded, assembly.standardResult, assembly.astResult, overallStatus)
	astPackageStatuses := astPackageStatuses(assembly.loaded, assembly.astResult)

	errors := append([]report.ReportError(nil), assembly.errors...)
	for _, diagnostic := range assembly.measurementDiagnostics {
		errors = append(errors, report.ReportError{
			Phase: diagnostic.phase, Code: diagnostic.code, Message: diagnostic.message,
		})
	}
	errors = append(errors, measurementRunErrors("standard-cover", assembly.standardResult)...)
	astMeasurementName := "ast"
	if assembly.standardCoverRequested && assembly.astRequested {
		astMeasurementName = "combined"
	}
	errors = append(errors, measurementRunErrors(astMeasurementName, assembly.astResult)...)

	return report.Input{
		ToolVersion:           assembly.toolVersion,
		ModulePath:            assembly.loaded.ModulePath,
		SourceFiles:           sourceFileInputs(assembly.sources),
		Coverage:              assembly.coverage,
		Decisions:             assembly.decisions,
		Evaluations:           assembly.evidence.Evaluations,
		NotEvaluatedDecisions: assembly.evidence.NotEvaluatedDecisions,
		Clauses:               assembly.clauses,
		NoMatches:             assembly.noMatches,
		ClauseObservations:    assembly.evidence.ClauseObservations,
		C0:                    assembly.c0,
		RunStatus:             overallStatus,
		FailureKind:           overallFailure,
		Results: report.RunResults{
			Test:        testResultStatus(overallStatus),
			Measurement: passFailResult(!assembly.analysisIncomplete && !assembly.interrupted),
			Integrity:   passFailResult(!assembly.integrityFailure),
			Strict:      report.ResultNotRequested,
			Threshold:   report.ResultNotRequested,
		},
		MeasurementMode: measurementMode(assembly.standardCoverRequested, assembly.astRequested),
		Measurements: measurementRuns(
			assembly.standardResult,
			assembly.astResult,
			assembly.standardCoverRequested && assembly.astRequested,
		),
		Backend:                     backend.OrchestratedBackend{},
		BackendProducers:            backend.V1Producers(),
		ASTEvidenceIntegrityUnknown: assembly.astEvidenceUnknown,
		C0EvidenceIntegrityUnknown:  assembly.c0EvidenceUnknown,
		InstrumentationUnknown:      assembly.instrumentationUnknown,
		Errors:                      errors,
		Complete:                    overallStatus == cover.RunPassed && !assembly.integrityFailure && !assembly.analysisIncomplete,
		PackageStatuses:             packageStatuses,
		ASTPackageStatuses:          astPackageStatuses,
	}
}

func testResultStatus(status cover.RunStatus) report.ResultStatus {
	switch status {
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

func packageStatuses(loaded loader.Result, standard, ast *gotest.Result, overall cover.RunStatus) map[string]string {
	statuses := make(map[string]string, len(loaded.PackageImportSet))
	for _, packagePath := range loaded.PackageImportSet {
		statuses[packagePath] = ""
	}
	for _, result := range []*gotest.Result{standard, ast} {
		if result == nil {
			continue
		}
		for packagePath, status := range result.Packages {
			statuses[packagePath] = mergePackageStatus(statuses[packagePath], string(status))
		}
	}
	for packagePath, status := range statuses {
		if status == "" {
			statuses[packagePath] = string(overall)
		}
	}
	return statuses
}

func astPackageStatuses(loaded loader.Result, ast *gotest.Result) map[string]string {
	statuses := make(map[string]string, len(loaded.PackageImportSet))
	if ast == nil {
		return statuses
	}
	for _, packagePath := range loaded.PackageImportSet {
		statuses[packagePath] = string(ast.Status)
	}
	for packagePath, status := range ast.Packages {
		statuses[packagePath] = string(status)
	}
	return statuses
}
