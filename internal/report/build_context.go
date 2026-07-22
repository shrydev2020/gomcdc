package report

import (
	"sort"

	"github.com/shrydev2020/gomcdc/internal/backend"
	"github.com/shrydev2020/gomcdc/internal/config"
	cover "github.com/shrydev2020/gomcdc/internal/coverage"
	"github.com/shrydev2020/gomcdc/internal/mcdc"
)

// buildContext is the normalized input side of report assembly. Build consumes
// these indexes to construct the package/file/function tree; evidence grouping
// and deterministic ordering are kept out of that phase.
type buildContext struct {
	report                Report
	maskingAnalysisBudget mcdc.AnalysisBudget

	decisions []cover.DecisionMetadata
	clauses   []cover.ClauseMetadata

	evaluationsByDecision   map[cover.DecisionID][]cover.DecisionEvaluation
	notEvaluatedByDecision  map[cover.DecisionID]int
	observationCounts       map[cover.ClauseID]map[cover.ClauseEventKind]int
	selectedAlternatives    map[cover.ClauseID][]uint16
	noMatchObservations     map[cover.SwitchID]int
	packageEvidence         map[string]bool
	astPackageEvidence      map[string]bool
	compilerPackageEvidence map[string]bool
	builders                map[string]*packageBuilder
}

func newBuildContext(input Input) *buildContext {
	instrumentationBackend := input.Backend
	if instrumentationBackend == nil {
		instrumentationBackend = backend.OrchestratedBackend{}
	}
	capabilities := instrumentationBackend.Capabilities().Clone()
	producerCapabilities := cloneProducerCapabilities(input.BackendProducers)
	if len(producerCapabilities) == 0 {
		if input.Backend == nil {
			producerCapabilities = cloneProducerCapabilities(backend.OrchestratedProducers())
		} else {
			producerCapabilities = []backend.ProducerCapabilities{{Backend: "configured", Capabilities: capabilities.Clone()}}
		}
	}
	maskingAnalysisBudget := mcdc.EffectiveMaskingAnalysisBudget(input.MaskingAnalysisBudget)
	report := Report{
		SchemaVersion:    SchemaVersion,
		ToolVersion:      normalizedToolVersion(input.ToolVersion),
		Module:           input.ModulePath,
		Run:              Run{Status: input.RunStatus, FailureKind: input.FailureKind, Complete: input.Complete, Results: normalizeRunResults(input.Results)},
		MeasurementMode:  input.MeasurementMode,
		Measurements:     cloneMeasurementRuns(input.Measurements),
		ProducerOutcomes: cloneProducerOutcomes(input.ProducerOutcomes),
		Capabilities:     capabilities,
		Backends:         producerCapabilities,
		Instrumentation:  buildInstrumentationReport(input, capabilities),
		Summary:          newSummary(input.Coverage),
		Packages:         make([]PackageReport, 0),
		Errors:           cloneReportErrors(input.Errors),
	}
	if input.Coverage.Enabled(config.MetricMCDCMasking) {
		report.MaskingAnalysisLimits = &MaskingAnalysisLimits{
			MaxEvaluationPairs: maskingAnalysisBudget.MaxEvaluationPairs,
			MaxSearchStates:    maskingAnalysisBudget.MaxSearchStates,
			MaxSolverBytes:     maskingAnalysisBudget.MaxSolverBytes,
		}
	}
	astEvidenceUnusable := producerRejectsEvidence(input.ProducerOutcomes, ProducerASTRuntime)
	compilerEvidenceUnusable := producerRejectsEvidence(input.ProducerOutcomes, ProducerCompilerSelection)
	c0EvidenceUnusable := producerRejectsEvidence(input.ProducerOutcomes, ProducerGoCover)

	decisions := append([]cover.DecisionMetadata(nil), input.Decisions...)
	sort.Slice(decisions, func(i, j int) bool { return lessDecision(decisions[i], decisions[j]) })
	clauses := append([]cover.ClauseMetadata(nil), input.Clauses...)
	sort.Slice(clauses, func(i, j int) bool { return lessClause(clauses[i], clauses[j]) })

	decisionByID := make(map[cover.DecisionID]cover.DecisionMetadata, len(decisions))
	for _, decision := range decisions {
		decisionByID[decision.ID] = decision
	}
	clauseByID := make(map[cover.ClauseID]cover.ClauseMetadata, len(clauses))
	for _, clause := range clauses {
		clauseByID[clause.ID] = clause
	}

	evaluationsByDecision := make(map[cover.DecisionID][]cover.DecisionEvaluation)
	notEvaluatedByDecision := make(map[cover.DecisionID]int)
	packageEvidence := make(map[string]bool)
	astPackageEvidence := make(map[string]bool)
	compilerPackageEvidence := make(map[string]bool)
	for _, evaluation := range input.Evaluations {
		evaluationsByDecision[evaluation.DecisionID] = append(evaluationsByDecision[evaluation.DecisionID], evaluation)
		packagePath := evaluation.PackagePath
		if packagePath == "" {
			packagePath = decisionByID[evaluation.DecisionID].Package
		}
		if packagePath != "" && !astEvidenceUnusable {
			packageEvidence[packagePath] = true
			astPackageEvidence[packagePath] = true
		}
	}
	for id := range evaluationsByDecision {
		sort.Slice(evaluationsByDecision[id], func(i, j int) bool {
			return lessEvaluation(evaluationsByDecision[id][i], evaluationsByDecision[id][j])
		})
	}
	for _, observation := range input.NotEvaluatedDecisions {
		notEvaluatedByDecision[observation.DecisionID]++
		if metadata, found := decisionByID[observation.DecisionID]; found && !astEvidenceUnusable {
			packageEvidence[metadata.Package] = true
			astPackageEvidence[metadata.Package] = true
		}
	}

	observationCounts := make(map[cover.ClauseID]map[cover.ClauseEventKind]int)
	selectedAlternativeSets := make(map[cover.ClauseID]map[uint16]struct{})
	noMatchObservations := make(map[cover.SwitchID]int)
	noMatchByID := make(map[cover.SwitchID]cover.NoMatchMetadata, len(input.NoMatches))
	for _, noMatch := range input.NoMatches {
		noMatchByID[noMatch.SwitchID] = noMatch
	}
	for _, observation := range input.ClauseObservations {
		if observation.Event == cover.ClauseNoMatchSelection {
			noMatchObservations[observation.SwitchID]++
			if metadata, found := noMatchByID[observation.SwitchID]; found && !compilerEvidenceUnusable {
				packageEvidence[metadata.Package] = true
				compilerPackageEvidence[metadata.Package] = true
			}
			continue
		}
		if observationCounts[observation.ClauseID] == nil {
			observationCounts[observation.ClauseID] = make(map[cover.ClauseEventKind]int)
		}
		observationCounts[observation.ClauseID][observation.Event]++
		if observation.Event == cover.ClauseDirectSelection && observation.AlternativeKnown {
			if selectedAlternativeSets[observation.ClauseID] == nil {
				selectedAlternativeSets[observation.ClauseID] = make(map[uint16]struct{})
			}
			selectedAlternativeSets[observation.ClauseID][observation.AlternativeIndex] = struct{}{}
		}
		if clause, found := clauseByID[observation.ClauseID]; found {
			switch observation.Event {
			case cover.ClauseDirectSelection:
				if !compilerEvidenceUnusable {
					packageEvidence[clause.Package] = true
					compilerPackageEvidence[clause.Package] = true
				}
			case cover.ClauseBodyExecution:
				if !astEvidenceUnusable {
					packageEvidence[clause.Package] = true
					astPackageEvidence[clause.Package] = true
				}
			}
		}
	}
	selectedAlternatives := make(map[cover.ClauseID][]uint16, len(selectedAlternativeSets))
	for clauseID, indexes := range selectedAlternativeSets {
		for index := range indexes {
			selectedAlternatives[clauseID] = append(selectedAlternatives[clauseID], index)
		}
		sort.Slice(selectedAlternatives[clauseID], func(i, j int) bool {
			return selectedAlternatives[clauseID][i] < selectedAlternatives[clauseID][j]
		})
	}

	builders := make(map[string]*packageBuilder)
	for packagePath := range input.PackageStatuses {
		ensurePackageBuilder(builders, packagePath, input)
	}
	for _, source := range input.SourceFiles {
		builder := ensurePackageBuilder(builders, source.PackagePath, input)
		if _, exists := builder.files[source.Path]; !exists {
			builder.files[source.Path] = &fileBuilder{path: source.Path, functions: make(map[string]*functionBuilder)}
		}
	}
	if input.C0 != nil {
		for _, packageReport := range input.C0.Packages {
			if packageReport.Evidence && !c0EvidenceUnusable {
				packageEvidence[packageReport.Path] = true
			}
			builder := ensurePackageBuilder(builders, packageReport.Path, input)
			for _, fileReport := range packageReport.Files {
				for _, functionReport := range fileReport.Functions {
					location := c0SourceLocation(fileReport.Path, functionReport.Position)
					function := ensureFunctionBuilder(builder, fileReport.Path, functionReport.Name, &location, input.Coverage)
					addC0Function(function, fileReport.Path, functionReport, c0EvidenceUnusable, input.Coverage)
				}
			}
		}
	}

	return &buildContext{
		report: report, maskingAnalysisBudget: maskingAnalysisBudget, decisions: decisions, clauses: clauses,
		evaluationsByDecision: evaluationsByDecision, notEvaluatedByDecision: notEvaluatedByDecision,
		observationCounts: observationCounts, selectedAlternatives: selectedAlternatives, noMatchObservations: noMatchObservations,
		packageEvidence: packageEvidence, astPackageEvidence: astPackageEvidence,
		compilerPackageEvidence: compilerPackageEvidence, builders: builders,
	}
}

func normalizedToolVersion(version string) string {
	if version == "" {
		return "unknown"
	}
	return version
}

func normalizeRunResults(results RunResults) RunResults {
	if results.Test == "" {
		results.Test = ResultNotRun
	}
	if results.Measurement == "" {
		results.Measurement = ResultNotRun
	}
	if results.Integrity == "" {
		results.Integrity = ResultNotRun
	}
	if results.Strict == "" {
		results.Strict = ResultNotRequested
	}
	if results.Threshold == "" {
		results.Threshold = ResultNotRequested
	}
	return results
}

func cloneReportErrors(values []ReportError) []ReportError {
	cloned := append(make([]ReportError, 0, len(values)), values...)
	sort.Slice(cloned, func(i, j int) bool {
		left, right := cloned[i], cloned[j]
		if left.Phase != right.Phase {
			return left.Phase < right.Phase
		}
		if left.Code != right.Code {
			return left.Code < right.Code
		}
		if left.Package != right.Package {
			return left.Package < right.Package
		}
		if left.Path != right.Path {
			return left.Path < right.Path
		}
		return left.Message < right.Message
	})
	return cloned
}
