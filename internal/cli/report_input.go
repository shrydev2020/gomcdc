package cli

import (
	"github.com/shrydev2020/gomcdc/internal/backend"
	"github.com/shrydev2020/gomcdc/internal/c0"
	"github.com/shrydev2020/gomcdc/internal/config"
	cover "github.com/shrydev2020/gomcdc/internal/coverage"
	"github.com/shrydev2020/gomcdc/internal/gotest"
	"github.com/shrydev2020/gomcdc/internal/loader"
	"github.com/shrydev2020/gomcdc/internal/report"
	"github.com/shrydev2020/gomcdc/internal/runtimecov"
)

// reportAssembly is the validated boundary between measurement orchestration
// and report construction. It contains no workspace or writer ownership: the
// caller owns those resources and supplies only their results here.
type reportAssembly struct {
	loaded                 loader.Result
	sources                []sourceInstrumentation
	coverage               config.CoverageSet
	decisions              []cover.DecisionMetadata
	clauses                []cover.ClauseMetadata
	noMatches              []cover.NoMatchMetadata
	collection             runtimecov.Collection
	c0                     *c0.Report
	standardResult         *gotest.Result
	astResult              *gotest.Result
	standardCoverRequested bool
	astRequested           bool
	astEvidenceUnknown     bool
	c0EvidenceUnknown      bool
	instrumentationUnknown int
	integrityFailure       bool
	analysisIncomplete     bool
}

// assembleReportInput turns measurement results into the canonical report
// input. Package status merging and measurement-mode selection belong here so
// runCoverage remains responsible for sequencing and exit-code policy only.
func assembleReportInput(assembly reportAssembly) report.Input {
	overallStatus, overallFailure := combineTestResults(assembly.standardResult, assembly.astResult)
	packageStatuses := packageStatuses(assembly.loaded, assembly.standardResult, assembly.astResult, overallStatus)
	astPackageStatuses := astPackageStatuses(assembly.loaded, assembly.astResult)

	return report.Input{
		ModulePath:                  assembly.loaded.ModulePath,
		SourceFiles:                 sourceFileInputs(assembly.sources),
		Coverage:                    assembly.coverage,
		Decisions:                   assembly.decisions,
		Evaluations:                 assembly.collection.Evaluations,
		NotEvaluatedDecisions:       assembly.collection.NotEvaluatedDecisions,
		Clauses:                     assembly.clauses,
		NoMatches:                   assembly.noMatches,
		ClauseObservations:          assembly.collection.Clauses,
		C0:                          assembly.c0,
		RunStatus:                   overallStatus,
		FailureKind:                 overallFailure,
		MeasurementMode:             measurementMode(assembly.standardCoverRequested, assembly.astRequested),
		Measurements:                measurementRuns(assembly.standardResult, assembly.astResult),
		Backend:                     backend.OrchestratedBackend{},
		BackendProducers:            backend.V1Producers(),
		ASTEvidenceIntegrityUnknown: assembly.astEvidenceUnknown,
		C0EvidenceIntegrityUnknown:  assembly.c0EvidenceUnknown,
		InstrumentationUnknown:      assembly.instrumentationUnknown,
		Complete:                    overallStatus == cover.RunPassed && !assembly.integrityFailure && !assembly.analysisIncomplete,
		PackageStatuses:             packageStatuses,
		ASTPackageStatuses:          astPackageStatuses,
	}
}

func measurementMode(standardCoverRequested, astRequested bool) report.MeasurementMode {
	switch {
	case standardCoverRequested && astRequested:
		return report.MeasurementDualRunStandardCover
	case standardCoverRequested:
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
