// Package report builds deterministic integrated coverage reports.
package report

import (
	"fmt"
	"math"
	"sort"

	"github.com/shrydev2020/gomcdc/internal/backend"
	"github.com/shrydev2020/gomcdc/internal/c0"
	"github.com/shrydev2020/gomcdc/internal/config"
	cover "github.com/shrydev2020/gomcdc/internal/coverage"
	"github.com/shrydev2020/gomcdc/internal/mcdc"
)

// SchemaVersion identifies the current stable JSON and text report schema.
const SchemaVersion = "1.1"

const statusDisabled = "disabled"

type MeasurementMode string

const (
	MeasurementSingleRun            MeasurementMode = "single-run"
	MeasurementStandardCover        MeasurementMode = "standard-cover"
	MeasurementDualRunStandardCover MeasurementMode = "dual-run-standard-cover"
)

// Input contains all static metadata, runtime evidence, and run state needed
// to build an integrated report.
type Input struct {
	ToolVersion           string
	ModulePath            string
	SourceFiles           []SourceFileInput
	Coverage              config.CoverageSet
	Decisions             []cover.DecisionMetadata
	Evaluations           []cover.DecisionEvaluation
	NotEvaluatedDecisions []cover.DecisionNotEvaluatedObservation
	Clauses               []cover.ClauseMetadata
	NoMatches             []cover.NoMatchMetadata
	ClauseObservations    []cover.ClauseObservation
	C0                    *c0.Report
	RunStatus             cover.RunStatus
	FailureKind           cover.RunFailureKind
	Results               RunResults
	MeasurementMode       MeasurementMode
	Measurements          []MeasurementRun
	Backend               backend.InstrumentationBackend
	BackendProducers      []backend.ProducerCapabilities
	// ProducerIntegrityUnknown forces entities from a damaged evidence stream
	// to unknown even when some records survived validation. Partial evidence
	// must never be reported as ordinary not-covered data.
	ASTEvidenceIntegrityUnknown bool
	C0EvidenceIntegrityUnknown  bool
	// InstrumentationUnknown counts requested source units whose static
	// analysis did not complete, so their entity denominator is unknowable.
	InstrumentationUnknown int
	Errors                 []ReportError
	Complete               bool
	PackageStatuses        map[string]string
	ASTPackageStatuses     map[string]string
}

// Report is the deterministic module report.
type Report struct {
	SchemaVersion   string                         `json:"schemaVersion"`
	ToolVersion     string                         `json:"toolVersion"`
	Module          string                         `json:"module"`
	Run             Run                            `json:"run"`
	MeasurementMode MeasurementMode                `json:"measurementMode"`
	Measurements    []MeasurementRun               `json:"measurements"`
	Capabilities    backend.CapabilitySet          `json:"capabilities"`
	Backends        []backend.ProducerCapabilities `json:"backendCapabilities"`
	Instrumentation backend.InstrumentationReport  `json:"instrumentationCoverage"`
	Summary         Summary                        `json:"summary"`
	Packages        []PackageReport                `json:"packages"`
	Errors          []ReportError                  `json:"errors"`
}

// ReportError is one deterministic, machine-readable failure attached to a
// partial or unsuccessful report. Path is module-relative when present.
type ReportError struct {
	Phase   string `json:"phase"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Package string `json:"package,omitempty"`
	Path    string `json:"path,omitempty"`
}

// MeasurementRun prevents dual-run standard C0 and AST evidence from being
// presented as though they came from one test execution.
type MeasurementRun struct {
	Name     string            `json:"name"`
	Run      TestRun           `json:"run"`
	Packages map[string]string `json:"packages"`
}

// ResultStatus is one independent D28 outcome axis. Not-requested is distinct
// from not-run so callers never have to infer whether a policy was enabled.
type ResultStatus string

const (
	ResultPassed       ResultStatus = "passed"
	ResultFailed       ResultStatus = "failed"
	ResultTimeout      ResultStatus = "timeout"
	ResultNotRun       ResultStatus = "not-run"
	ResultNotRequested ResultStatus = "not-requested"
)

// RunResults preserves the five independent outcomes required by D28 even
// when the process exit code collapses them according to precedence.
type RunResults struct {
	Test        ResultStatus `json:"test"`
	Measurement ResultStatus `json:"measurement"`
	Integrity   ResultStatus `json:"integrity"`
	Strict      ResultStatus `json:"strict"`
	Threshold   ResultStatus `json:"threshold"`
}

// TestRun describes one go test execution independently from coverage and
// from the overall policy results.
type TestRun struct {
	Status      cover.RunStatus      `json:"status"`
	FailureKind cover.RunFailureKind `json:"failureKind"`
	Complete    bool                 `json:"complete"`
}

// Run describes the aggregate test execution and independent D28 outcomes.
type Run struct {
	Status      cover.RunStatus      `json:"status"`
	FailureKind cover.RunFailureKind `json:"failureKind"`
	Complete    bool                 `json:"complete"`
	Results     RunResults           `json:"results"`
}

// Summary always exposes every supported metric.
type Summary struct {
	Statement                 MetricSummary `json:"statement"`
	Function                  MetricSummary `json:"function"`
	Decision                  MetricSummary `json:"decision"`
	SwitchClauseBody          MetricSummary `json:"switchClauseBody"`
	TypeSwitchClauseBody      MetricSummary `json:"typeSwitchClauseBody"`
	SelectClauseBody          MetricSummary `json:"selectClauseBody"`
	SwitchClauseSelection     MetricSummary `json:"switchClauseSelection"`
	TypeSwitchClauseSelection MetricSummary `json:"typeSwitchClauseSelection"`
	Condition                 MetricSummary `json:"condition"`
	MCDCUnique                MetricSummary `json:"mcdcUnique"`
	MCDCMasking               MetricSummary `json:"mcdcMasking"`
}

// MetricSummary contains denominator-based coverage and orthogonal analysis
// states. Unsupported, unknown, and infeasible obligations are excluded from
// Total and reported separately.
type MetricSummary struct {
	Enabled            bool     `json:"enabled"`
	Covered            int      `json:"covered"`
	Total              int      `json:"total"`
	Percentage         *float64 `json:"percentage"`
	Unsupported        int      `json:"unsupported"`
	Unknown            int      `json:"unknown"`
	Infeasible         int      `json:"infeasible"`
	AnalysisIncomplete int      `json:"analysisIncomplete"`
}

// PackageReport contains one package's status, evidence marker, and hierarchy.
type PackageReport struct {
	Path     string       `json:"path"`
	Status   string       `json:"status"`
	Evidence bool         `json:"evidence"`
	Summary  Summary      `json:"summary"`
	Files    []FileReport `json:"files"`
}

// FileReport contains merged C0 and AST entities for one original file.
type FileReport struct {
	Path      string           `json:"path"`
	Summary   Summary          `json:"summary"`
	Functions []FunctionReport `json:"functions"`
	Source    *SourceFileView  `json:"-"`
}

// FunctionReport contains statements, decisions, and selectable clauses.
type FunctionReport struct {
	Name       string                `json:"name"`
	Location   *cover.SourceLocation `json:"location"`
	Summary    Summary               `json:"summary"`
	Statements []StatementReport     `json:"statements"`
	Decisions  []DecisionReport      `json:"decisions"`
	Clauses    []ClauseReport        `json:"clauses"`
	NoMatches  []NoMatchReport       `json:"noMatchSelections,omitempty"`
}

// StatementReport is one original-source Go cover block.
type StatementReport struct {
	Location   cover.SourceLocation `json:"location"`
	Statements int                  `json:"statements"`
	Count      uint64               `json:"count"`
	Covered    bool                 `json:"covered"`
	Metric     MetricSummary        `json:"metric"`
}

// DecisionReport contains static metadata, runtime evidence, and both MC/DC
// strategy results.
type DecisionReport struct {
	DecisionID       string               `json:"decisionId"`
	Kind             cover.DecisionKind   `json:"kind"`
	Location         cover.SourceLocation `json:"location"`
	Expression       string               `json:"expression"`
	NotEvaluated     int                  `json:"notEvaluated"`
	Summary          Summary              `json:"summary"`
	DecisionCoverage DecisionCoverage     `json:"decisionCoverage"`
	Conditions       []ConditionReport    `json:"conditions"`
	MCDCUnique       MCDCAnalysisReport   `json:"mcdcUnique"`
	MCDCMasking      MCDCAnalysisReport   `json:"mcdcMasking"`
	Evaluations      []EvaluationReport   `json:"evaluations"`
}

// DecisionCoverage distinguishes the true and false outcomes.
type DecisionCoverage struct {
	True   bool          `json:"true"`
	False  bool          `json:"false"`
	Metric MetricSummary `json:"metric"`
}

// ConditionReport contains observed states and strategy-specific MC/DC data.
type ConditionReport struct {
	ConditionID  string               `json:"conditionId"`
	Index        uint16               `json:"index"`
	Expression   string               `json:"expression"`
	Location     cover.SourceLocation `json:"location"`
	True         bool                 `json:"true"`
	False        bool                 `json:"false"`
	NotEvaluated int                  `json:"notEvaluated"`
	Metric       MetricSummary        `json:"metric"`
	MCDCUnique   MCDCConditionReport  `json:"mcdcUnique"`
	MCDCMasking  MCDCConditionReport  `json:"mcdcMasking"`
}

// MCDCAnalysisReport is one strategy result for a decision.
type MCDCAnalysisReport struct {
	Enabled             bool                  `json:"enabled"`
	Status              string                `json:"status"`
	Outcome             string                `json:"outcome"`
	Support             string                `json:"support"`
	Analysis            string                `json:"analysis"`
	Metric              MetricSummary         `json:"metric"`
	EvaluationsAnalyzed int                   `json:"evaluationsAnalyzed"`
	AbortedEvaluations  int                   `json:"abortedEvaluations"`
	InvalidEvaluations  int                   `json:"invalidEvaluations"`
	Reason              string                `json:"reason"`
	Conditions          []MCDCConditionReport `json:"conditions"`
}

// MCDCConditionReport is one condition's strategy status and witness.
type MCDCConditionReport struct {
	Index    uint16         `json:"index"`
	Status   string         `json:"status"`
	Outcome  string         `json:"outcome"`
	Support  string         `json:"support"`
	Analysis string         `json:"analysis"`
	Reason   string         `json:"reason"`
	Witness  *WitnessReport `json:"witness"`
}

// WitnessReport contains the pair and any masking completion evidence.
type WitnessReport struct {
	First                EvaluationReport `json:"first"`
	Second               EvaluationReport `json:"second"`
	FirstCompletion      []string         `json:"firstCompletion"`
	SecondCompletion     []string         `json:"secondCompletion"`
	UnobservedConditions []uint16         `json:"unobservedConditions"`
	MaskedConditions     []uint16         `json:"maskedConditions"`
}

// EvaluationReport serializes typed IDs as fixed strings while retaining the
// provenance needed to distinguish package test processes.
type EvaluationReport struct {
	DecisionID   string   `json:"decisionId"`
	EvaluationID string   `json:"evaluationId"`
	Status       string   `json:"status"`
	RunID        string   `json:"runId"`
	PackagePath  string   `json:"packagePath"`
	ProcessID    int      `json:"processId"`
	TestID       string   `json:"testId"`
	Conditions   []string `json:"conditions"`
	Result       bool     `json:"result"`
}

// ClauseReport keeps source-body and compiler dispatch evidence separate.
type ClauseReport struct {
	ClauseID             string               `json:"clauseId"`
	GroupID              string               `json:"groupId"`
	SwitchID             string               `json:"switchId"`
	Kind                 cover.ClauseKind     `json:"kind"`
	Role                 cover.ClauseRole     `json:"role"`
	Index                uint16               `json:"index"`
	Location             cover.SourceLocation `json:"location"`
	Expressions          []string             `json:"expressions"`
	Types                []string             `json:"types"`
	DecisionIDs          []string             `json:"decisionIds"`
	BodyExecutions       int                  `json:"bodyExecutions"`
	DirectSelections     int                  `json:"directSelections"`
	SelectedAlternatives []uint16             `json:"selectedAlternatives"`
	BodyCoverage         MetricSummary        `json:"bodyCoverage"`
	SelectionCoverage    MetricSummary        `json:"selectionCoverage"`
}

// NoMatchReport is a switch-selection obligation without a source ClauseID.
type NoMatchReport struct {
	SwitchID          string               `json:"switchId"`
	Kind              cover.ClauseKind     `json:"kind"`
	Location          cover.SourceLocation `json:"location"`
	SelectionCoverage MetricSummary        `json:"selectionCoverage"`
}

type packageBuilder struct {
	path   string
	status string
	files  map[string]*fileBuilder
}

type fileBuilder struct {
	path      string
	functions map[string]*functionBuilder
}

type functionBuilder struct {
	report FunctionReport
}

type entityState uint8

const (
	entityNormal entityState = iota
	entityUnknown
	entityUnsupported
)

// Build creates the deterministic integrated hierarchy.
func Build(input Input) Report {
	context := newBuildContext(input)
	buildHierarchy(context, input)
	finalizeReport(context, input)
	return context.report
}

// WithRunResultsAndErrors returns an already-built coverage hierarchy with
// only the CLI-owned policy results and report errors replaced. It preserves
// coverage summaries and witnesses and copies errors so callers retain no
// mutation authority over the report.
func WithRunResultsAndErrors(value Report, results RunResults, errors []ReportError) Report {
	value.Run.Results = normalizeRunResults(results)
	value.Errors = cloneReportErrors(errors)
	return value
}

// buildHierarchy turns indexed evidence and static metadata into the
// package/file/function/decision/clause tree. It does not finalize package
// ordering or module summaries.
func buildHierarchy(context *buildContext, input Input) {
	decisions := context.decisions
	clauses := context.clauses
	evaluationsByDecision := context.evaluationsByDecision
	notEvaluatedByDecision := context.notEvaluatedByDecision
	observationCounts := context.observationCounts
	noMatchObservations := context.noMatchObservations
	astPackageEvidence := context.astPackageEvidence
	builders := context.builders

	for _, decision := range decisions {
		builder := ensurePackageBuilder(builders, decision.Package, input)
		function := ensureFunctionBuilder(builder, decision.Location.File, displayFunctionName(decision.Function), optionalLocation(decision.FunctionLocation), input.Coverage)
		state := stateForEvaluations(
			evaluationsByDecision[decision.ID],
			input.ASTEvidenceIntegrityUnknown || packageUnknown(astPackageStatus(input, decision.Package, builder.status), astPackageEvidence[decision.Package]),
		)
		if state == entityNormal && !supportedDecision(decision.Kind) {
			state = entityUnsupported
		}
		decisionReport := buildDecisionReport(decision, evaluationsByDecision[decision.ID], notEvaluatedByDecision[decision.ID], state, input.Coverage)
		function.report.Decisions = append(function.report.Decisions, decisionReport)
		addSummary(&function.report.Summary, decisionReport.Summary)
	}

	for _, clause := range clauses {
		builder := ensurePackageBuilder(builders, clause.Package, input)
		function := ensureFunctionBuilder(builder, clause.Location.File, displayFunctionName(clause.Function), optionalLocation(clause.FunctionLocation), input.Coverage)
		unknown := input.ASTEvidenceIntegrityUnknown || packageUnknown(astPackageStatus(input, clause.Package, builder.status), astPackageEvidence[clause.Package])
		clauseReport := buildClauseReport(
			clause,
			observationCounts[clause.ID],
			context.selectedAlternatives[clause.ID],
			unknown,
			context.report.Capabilities,
			input.Coverage,
		)
		function.report.Clauses = append(function.report.Clauses, clauseReport)
		addClauseMetrics(&function.report.Summary, clause.Kind, clauseReport.BodyCoverage, clauseReport.SelectionCoverage)
	}
	for _, noMatch := range input.NoMatches {
		builder := ensurePackageBuilder(builders, noMatch.Package, input)
		function := ensureFunctionBuilder(builder, noMatch.Location.File, displayFunctionName(noMatch.Function), optionalLocation(noMatch.FunctionLocation), input.Coverage)
		unknown := input.ASTEvidenceIntegrityUnknown || packageUnknown(astPackageStatus(input, noMatch.Package, builder.status), astPackageEvidence[noMatch.Package])
		noMatchReport := buildNoMatchReport(noMatch, noMatchObservations[noMatch.SwitchID] > 0, unknown, context.report.Capabilities, input.Coverage)
		function.report.NoMatches = append(function.report.NoMatches, noMatchReport)
		addNoMatchMetric(&function.report.Summary, noMatch.Kind, noMatchReport.SelectionCoverage)
	}
}

// finalizeReport orders the hierarchy and aggregates child summaries into
// package and module totals. Source views are a separate HTML projection.
func finalizeReport(context *buildContext, input Input) {
	report := &context.report
	packageEvidence := context.packageEvidence
	builders := context.builders
	packagePaths := make([]string, 0, len(builders))
	for packagePath := range builders {
		packagePaths = append(packagePaths, packagePath)
	}
	sort.Strings(packagePaths)
	for _, packagePath := range packagePaths {
		builder := builders[packagePath]
		packageReport := PackageReport{
			Path:     builder.path,
			Status:   builder.status,
			Evidence: packageEvidence[builder.path],
			Summary:  newSummary(input.Coverage),
			Files:    make([]FileReport, 0, len(builder.files)),
		}
		filePaths := make([]string, 0, len(builder.files))
		for filePath := range builder.files {
			filePaths = append(filePaths, filePath)
		}
		sort.Strings(filePaths)
		for _, filePath := range filePaths {
			file := finalizeFile(builder.files[filePath], input.Coverage)
			packageReport.Files = append(packageReport.Files, file)
			addSummary(&packageReport.Summary, file.Summary)
		}
		report.Packages = append(report.Packages, packageReport)
		addSummary(&report.Summary, packageReport.Summary)
	}
}

func buildInstrumentationReport(input Input, capabilities backend.CapabilitySet) backend.InstrumentationReport {
	metrics := make(map[string]backend.InstrumentationCoverage)
	add := func(name string, status backend.CapabilityStatus, count int, instrumented bool) {
		coverage := metrics[name]
		coverage.Add(status, count, instrumented)
		metrics[name] = coverage
	}

	if input.Coverage.Enabled(config.MetricStatement) && input.C0 != nil {
		for _, packageReport := range input.C0.Packages {
			for _, fileReport := range packageReport.Files {
				for _, functionReport := range fileReport.Functions {
					for _, block := range functionReport.Blocks {
						add(
							string(config.MetricStatement),
							capabilities.Status(backend.CapabilityStatementCoverage),
							block.Statements,
							block.Evidence,
						)
					}
				}
			}
		}
	}
	if input.Coverage.Enabled(config.MetricFunction) && input.C0 != nil {
		for _, packageReport := range input.C0.Packages {
			for _, fileReport := range packageReport.Files {
				for _, functionReport := range fileReport.Functions {
					add(
						string(config.MetricFunction),
						capabilities.Status(backend.CapabilityFunctionCoverage),
						functionReport.Summary.Functions.Total,
						functionReport.CompleteEvidence,
					)
				}
			}
		}
	}

	for _, decision := range input.Decisions {
		decisionStatus := decisionInstrumentationStatus(capabilities, decision.Kind)
		if input.Coverage.Enabled(config.MetricDecision) {
			add(string(config.MetricDecision), decisionStatus, 1, decisionStatus == backend.CapabilitySupported)
		}
		if input.Coverage.Enabled(config.MetricCondition) {
			status := combineCapabilityStatus(decisionStatus, capabilities.Status(backend.CapabilityConditionCoverage))
			add(string(config.MetricCondition), status, len(decision.Conditions), status == backend.CapabilitySupported)
		}
		if input.Coverage.Enabled(config.MetricMCDCUnique) {
			status := combineCapabilityStatus(decisionStatus, capabilities.Status(backend.CapabilityMCDCUnique))
			add(string(config.MetricMCDCUnique), status, len(decision.Conditions), status == backend.CapabilitySupported)
		}
		if input.Coverage.Enabled(config.MetricMCDCMasking) {
			status := combineCapabilityStatus(
				decisionStatus,
				capabilities.Status(backend.CapabilityMCDCMasking),
				booleanExpressionCapability(decision.ExpressionTree),
			)
			add(string(config.MetricMCDCMasking), status, len(decision.Conditions), status == backend.CapabilitySupported)
		}
	}

	for _, clause := range input.Clauses {
		bodyMetric, bodyEnabled := clauseBodyMetric(clause.Kind, input.Coverage)
		if bodyEnabled {
			status := clauseBodyInstrumentationStatus(capabilities, clause)
			add(string(bodyMetric), status, 1, status == backend.CapabilitySupported)
		}
		selectionMetric, selectionEnabled := clauseSelectionMetric(clause.Kind, input.Coverage)
		if selectionEnabled {
			status := clauseSelectionInstrumentationStatus(capabilities, clause)
			add(string(selectionMetric), status, 1, status == backend.CapabilitySupported)
		}
	}
	for _, noMatch := range input.NoMatches {
		selectionMetric, selectionEnabled := clauseSelectionMetric(noMatch.Kind, input.Coverage)
		if selectionEnabled {
			status := clauseSelectionInstrumentationStatus(capabilities, cover.ClauseMetadata{Kind: noMatch.Kind})
			add(string(selectionMetric), status, 1, status == backend.CapabilitySupported)
		}
	}
	add("sourceAnalysis", backend.CapabilityUnknown, input.InstrumentationUnknown, false)
	return backend.NewInstrumentationReport(metrics)
}

func clauseBodyMetric(kind cover.ClauseKind, coverage config.CoverageSet) (config.Metric, bool) {
	var metric config.Metric
	switch kind {
	case cover.ClauseExpressionSwitch, cover.ClauseConditionlessSwitch:
		metric = config.MetricSwitchClauseBody
	case cover.ClauseTypeSwitch:
		metric = config.MetricTypeSwitchClauseBody
	case cover.ClauseSelect:
		metric = config.MetricSelectClauseBody
	default:
		return "", false
	}
	return metric, coverage.Enabled(metric)
}

func clauseSelectionMetric(kind cover.ClauseKind, coverage config.CoverageSet) (config.Metric, bool) {
	var metric config.Metric
	switch kind {
	case cover.ClauseExpressionSwitch:
		metric = config.MetricSwitchClauseSelection
	case cover.ClauseTypeSwitch:
		metric = config.MetricTypeSwitchClauseSelection
	default:
		return "", false
	}
	return metric, coverage.Enabled(metric)
}

func clauseSelectionInstrumentationStatus(capabilities backend.CapabilitySet, clause cover.ClauseMetadata) backend.CapabilityStatus {
	switch clause.Kind {
	case cover.ClauseExpressionSwitch:
		return capabilities.Status(backend.CapabilitySwitchClauseSelection)
	case cover.ClauseTypeSwitch:
		return capabilities.Status(backend.CapabilityTypeSwitchClauseSelection)
	default:
		return backend.CapabilityUnknown
	}
}

func decisionInstrumentationStatus(capabilities backend.CapabilitySet, kind cover.DecisionKind) backend.CapabilityStatus {
	capability, known := backend.DecisionCapability(kind)
	if !known {
		return backend.CapabilityUnknown
	}
	return capabilities.Status(capability)
}

func clauseBodyInstrumentationStatus(capabilities backend.CapabilitySet, clause cover.ClauseMetadata) backend.CapabilityStatus {
	if clause.Role != cover.ClauseCase && clause.Role != cover.ClauseDefault {
		return backend.CapabilityUnknown
	}
	capability, known := backend.ClauseBodyCapability(clause.Kind)
	if !known {
		return backend.CapabilityUnknown
	}
	return capabilities.Status(capability)
}

func combineCapabilityStatus(statuses ...backend.CapabilityStatus) backend.CapabilityStatus {
	result := backend.CapabilitySupported
	for _, status := range statuses {
		switch status {
		case backend.CapabilityUnsupportedByBackend:
			return status
		case backend.CapabilityUnknown:
			result = status
		case backend.CapabilitySupported:
		default:
			result = backend.CapabilityUnknown
		}
	}
	return result
}

func booleanExpressionCapability(expression *cover.BooleanExpression) backend.CapabilityStatus {
	if expression == nil {
		return backend.CapabilityUnknown
	}
	visiting := make(map[*cover.BooleanExpression]bool)
	var visit func(*cover.BooleanExpression) backend.CapabilityStatus
	visit = func(node *cover.BooleanExpression) backend.CapabilityStatus {
		if node == nil || visiting[node] {
			return backend.CapabilityUnknown
		}
		visiting[node] = true
		defer delete(visiting, node)
		switch node.Kind {
		case cover.BooleanExpressionCondition, cover.BooleanExpressionConstant:
			return backend.CapabilitySupported
		case cover.BooleanExpressionNot:
			return visit(node.Left)
		case cover.BooleanExpressionAnd, cover.BooleanExpressionOr:
			return combineCapabilityStatus(visit(node.Left), visit(node.Right))
		default:
			return backend.CapabilityUnsupportedByBackend
		}
	}
	return visit(expression)
}

func addC0Function(builder *functionBuilder, filePath string, source c0.FunctionReport, integrityUnknown bool, coverage config.CoverageSet) {
	coveredWithEvidence := false
	for _, block := range source.Blocks {
		coveredWithEvidence = coveredWithEvidence || block.Evidence && block.Count > 0
		metric := newMetric(coverage.Enabled(config.MetricStatement))
		if metric.Enabled {
			if integrityUnknown {
				metric.Unknown = block.Summary.Statements.Total
			} else if block.Evidence {
				metric.Covered = block.Summary.Statements.Covered
				metric.Total = block.Summary.Statements.Total
			} else {
				metric.Unknown = block.Summary.Statements.Total
			}
			metric.recalculate()
		}
		builder.report.Statements = append(builder.report.Statements, StatementReport{
			Location:   c0SourceLocation(filePath, block.Position),
			Statements: block.Statements,
			Count:      block.Count,
			Covered:    block.Count > 0,
			Metric:     metric,
		})
		addMetric(&builder.report.Summary.Statement, metric)
	}
	if coverage.Enabled(config.MetricFunction) {
		metric := newMetric(true)
		if integrityUnknown {
			metric.Unknown = source.Summary.Functions.Total
		} else if coveredWithEvidence {
			metric.Covered = source.Summary.Functions.Total
			metric.Total = source.Summary.Functions.Total
		} else if source.CompleteEvidence {
			metric.Total = source.Summary.Functions.Total
		} else {
			metric.Unknown = source.Summary.Functions.Total
		}
		metric.recalculate()
		addMetric(&builder.report.Summary.Function, metric)
	}
}

func buildDecisionReport(
	metadata cover.DecisionMetadata,
	evaluations []cover.DecisionEvaluation,
	notEvaluated int,
	state entityState,
	coverage config.CoverageSet,
) DecisionReport {
	completed := completedEvaluations(evaluations)
	trueCovered, falseCovered := false, false
	for _, evaluation := range completed {
		if evaluation.Result {
			trueCovered = true
		} else {
			falseCovered = true
		}
	}
	decisionMetric := metricForOutcomes(coverage.Enabled(config.MetricDecision), state, trueCovered, falseCovered)

	uniqueResult := (mcdc.UniqueCauseStrategy{}).Analyze(metadata, evaluations)
	maskingResult := (mcdc.MaskingStrategy{Budget: mcdc.DefaultMaskingAnalysisBudget()}).Analyze(metadata, evaluations)
	unique := buildMCDCAnalysis(uniqueResult, metadata.Conditions, state, coverage.Enabled(config.MetricMCDCUnique))
	masking := buildMCDCAnalysis(maskingResult, metadata.Conditions, state, coverage.Enabled(config.MetricMCDCMasking))

	conditions := append([]cover.ConditionMetadata(nil), metadata.Conditions...)
	sort.Slice(conditions, func(i, j int) bool { return conditions[i].Index < conditions[j].Index })
	conditionReports := make([]ConditionReport, 0, len(conditions))
	for _, condition := range conditions {
		trueObserved, falseObserved, conditionNotEvaluated := conditionEvidence(completed, condition.Index)
		conditionMetric := metricForOutcomes(coverage.Enabled(config.MetricCondition), state, trueObserved, falseObserved)
		conditionReports = append(conditionReports, ConditionReport{
			ConditionID:  formatID(uint64(condition.ID)),
			Index:        condition.Index,
			Expression:   condition.Expression,
			Location:     condition.Location,
			True:         trueObserved,
			False:        falseObserved,
			NotEvaluated: conditionNotEvaluated + notEvaluated,
			Metric:       conditionMetric,
			MCDCUnique:   mcdcCondition(unique, condition.Index),
			MCDCMasking:  mcdcCondition(masking, condition.Index),
		})
	}

	summary := newSummary(coverage)
	addMetric(&summary.Decision, decisionMetric)
	for _, condition := range conditionReports {
		addMetric(&summary.Condition, condition.Metric)
	}
	addMetric(&summary.MCDCUnique, unique.Metric)
	addMetric(&summary.MCDCMasking, masking.Metric)

	evaluationReports := make([]EvaluationReport, 0, len(evaluations))
	for _, evaluation := range evaluations {
		evaluationReports = append(evaluationReports, evaluationReport(evaluation))
	}
	return DecisionReport{
		DecisionID:   formatID(uint64(metadata.ID)),
		Kind:         metadata.Kind,
		Location:     metadata.Location,
		Expression:   metadata.Expression,
		NotEvaluated: notEvaluated,
		Summary:      summary,
		DecisionCoverage: DecisionCoverage{
			True:   trueCovered,
			False:  falseCovered,
			Metric: decisionMetric,
		},
		Conditions:  conditionReports,
		MCDCUnique:  unique,
		MCDCMasking: masking,
		Evaluations: evaluationReports,
	}
}

func buildMCDCAnalysis(
	result cover.MCDCResult,
	conditions []cover.ConditionMetadata,
	state entityState,
	enabled bool,
) MCDCAnalysisReport {
	report := MCDCAnalysisReport{
		Enabled:             enabled,
		Status:              statusDisabled,
		Metric:              newMetric(enabled),
		EvaluationsAnalyzed: result.EvaluationsAnalyzed,
		AbortedEvaluations:  result.AbortedEvaluations,
		InvalidEvaluations:  result.InvalidEvaluations,
		Reason:              result.Reason,
		Conditions:          make([]MCDCConditionReport, 0, len(conditions)),
	}
	if !enabled {
		report.Reason = ""
	}
	resultByIndex := make(map[uint16]cover.MCDCConditionResult, len(result.Conditions))
	for _, condition := range result.Conditions {
		resultByIndex[condition.ConditionIndex] = condition
	}
	metadata := append([]cover.ConditionMetadata(nil), conditions...)
	sort.Slice(metadata, func(i, j int) bool { return metadata[i].Index < metadata[j].Index })
	for _, condition := range metadata {
		conditionResult := resultByIndex[condition.Index]
		outcome := conditionResult.Outcome
		support := conditionResult.Support
		analysis := conditionResult.Analysis
		status := mcdcStatus(outcome, support, analysis)
		reason := conditionResult.Reason
		var witness *WitnessReport
		if conditionResult.Witness != nil {
			witness = witnessReport(*conditionResult.Witness)
		}
		if !enabled {
			status = statusDisabled
			outcome = cover.CoverageOutcomeUnknown
			support = cover.SupportUnknown
			analysis = cover.AnalysisIncomplete
			reason = ""
			witness = nil
		} else {
			switch state {
			case entityUnknown:
				status = string(cover.SupportUnknown)
				outcome = cover.CoverageOutcomeUnknown
				support = cover.SupportUnknown
				analysis = cover.AnalysisIncomplete
				reason = "package did not produce runtime or C0 evidence"
				witness = nil
			case entityUnsupported:
				status = string(cover.SupportUnsupported)
				outcome = cover.CoverageOutcomeUnknown
				support = cover.SupportUnsupported
				analysis = cover.AnalysisComplete
				reason = "decision kind is not supported"
				witness = nil
			}
			addMCDCStatus(&report.Metric, outcome, support, analysis, 1)
		}
		report.Conditions = append(report.Conditions, MCDCConditionReport{
			Index: condition.Index, Status: status, Outcome: string(outcome),
			Support: string(support), Analysis: string(analysis),
			Reason: reason, Witness: witness,
		})
	}
	if enabled {
		outcome := result.Outcome
		support := result.Support
		analysis := result.Analysis
		switch state {
		case entityUnknown:
			report.Status = string(cover.SupportUnknown)
			outcome = cover.CoverageOutcomeUnknown
			support = cover.SupportUnknown
			analysis = cover.AnalysisIncomplete
			report.Reason = "package did not produce runtime or C0 evidence"
		case entityUnsupported:
			report.Status = string(cover.SupportUnsupported)
			outcome = cover.CoverageOutcomeUnknown
			support = cover.SupportUnsupported
			analysis = cover.AnalysisComplete
			report.Reason = "decision kind is not supported"
		default:
			report.Status = mcdcStatus(outcome, support, analysis)
		}
		report.Outcome = string(outcome)
		report.Support = string(support)
		report.Analysis = string(analysis)
	}
	return report
}

func mcdcStatus(
	outcome cover.CoverageOutcome,
	support cover.SupportStatus,
	analysis cover.AnalysisStatus,
) string {
	switch {
	case support == cover.SupportUnsupported:
		return string(cover.SupportUnsupported)
	case support == cover.SupportUnknown:
		return string(cover.SupportUnknown)
	case analysis == cover.AnalysisIncomplete:
		return string(cover.CoverageAnalysisIncomplete)
	case analysis == cover.AnalysisInfeasible:
		return string(cover.CoverageInfeasible)
	case outcome == cover.CoverageOutcomeCovered:
		return string(cover.CoverageCovered)
	case outcome == cover.CoverageOutcomeNotCovered:
		return string(cover.CoverageNotCovered)
	default:
		return string(cover.SupportUnknown)
	}
}

func buildClauseReport(
	metadata cover.ClauseMetadata,
	observations map[cover.ClauseEventKind]int,
	selectedAlternatives []uint16,
	unknown bool,
	capabilities backend.CapabilitySet,
	coverage config.CoverageSet,
) ClauseReport {
	bodyExecutions := observations[cover.ClauseBodyExecution]
	directSelections := observations[cover.ClauseDirectSelection]
	_, bodyEnabled := clauseBodyMetric(metadata.Kind, coverage)
	bodyCoverage := newMetric(bodyEnabled)
	if bodyCoverage.Enabled {
		switch {
		case unknown:
			bodyCoverage.Unknown = 1
		case !supportedClause(metadata):
			bodyCoverage.Unsupported = 1
		default:
			bodyCoverage.Total = 1
			if bodyExecutions > 0 {
				bodyCoverage.Covered = 1
			}
		}
		bodyCoverage.recalculate()
	}
	_, selectionEnabled := clauseSelectionMetric(metadata.Kind, coverage)
	selectionCoverage := newMetric(selectionEnabled)
	if selectionCoverage.Enabled {
		status := clauseSelectionInstrumentationStatus(capabilities, metadata)
		switch {
		case unknown:
			selectionCoverage.Unknown = 1
		case status == backend.CapabilityUnsupportedByBackend:
			selectionCoverage.Unsupported = 1
		case status != backend.CapabilitySupported:
			selectionCoverage.Unknown = 1
		default:
			selectionCoverage.Total = 1
			if directSelections > 0 {
				selectionCoverage.Covered = 1
			}
		}
		selectionCoverage.recalculate()
	}
	decisionIDs := make([]string, 0, len(metadata.DecisionIDs))
	for _, id := range metadata.DecisionIDs {
		decisionIDs = append(decisionIDs, formatID(uint64(id)))
	}
	sort.Strings(decisionIDs)
	return ClauseReport{
		ClauseID: formatID(uint64(metadata.ID)), GroupID: formatID(uint64(metadata.GroupID)),
		SwitchID: formatID(uint64(metadata.SwitchID)),
		Kind:     metadata.Kind, Role: metadata.Role, Index: metadata.Index, Location: metadata.Location,
		Expressions: append([]string{}, metadata.Expressions...), Types: append([]string{}, metadata.Types...),
		DecisionIDs: decisionIDs, BodyExecutions: bodyExecutions,
		DirectSelections: directSelections, SelectedAlternatives: append([]uint16{}, selectedAlternatives...),
		BodyCoverage: bodyCoverage, SelectionCoverage: selectionCoverage,
	}
}

func buildNoMatchReport(metadata cover.NoMatchMetadata, observed, unknown bool, capabilities backend.CapabilitySet, coverage config.CoverageSet) NoMatchReport {
	enabled := metadata.Kind == cover.ClauseExpressionSwitch && coverage.Enabled(config.MetricSwitchClauseSelection) ||
		metadata.Kind == cover.ClauseTypeSwitch && coverage.Enabled(config.MetricTypeSwitchClauseSelection)
	metric := newMetric(enabled)
	if enabled {
		status := clauseSelectionInstrumentationStatus(capabilities, cover.ClauseMetadata{Kind: metadata.Kind})
		switch {
		case unknown:
			metric.Unknown = 1
		case status == backend.CapabilityUnsupportedByBackend:
			metric.Unsupported = 1
		case status != backend.CapabilitySupported:
			metric.Unknown = 1
		default:
			metric.Total = 1
			if observed {
				metric.Covered = 1
			}
		}
		metric.recalculate()
	}
	return NoMatchReport{
		SwitchID: formatID(uint64(metadata.SwitchID)),
		Kind:     metadata.Kind, Location: metadata.Location,
		SelectionCoverage: metric,
	}
}

func finalizeFile(builder *fileBuilder, coverage config.CoverageSet) FileReport {
	file := FileReport{Path: builder.path, Summary: newSummary(coverage), Functions: make([]FunctionReport, 0, len(builder.functions))}
	functions := make([]FunctionReport, 0, len(builder.functions))
	for _, function := range builder.functions {
		sort.Slice(function.report.Statements, func(i, j int) bool {
			return lessLocation(function.report.Statements[i].Location, function.report.Statements[j].Location)
		})
		sort.Slice(function.report.Decisions, func(i, j int) bool {
			return lessLocation(function.report.Decisions[i].Location, function.report.Decisions[j].Location) || (function.report.Decisions[i].Location == function.report.Decisions[j].Location && function.report.Decisions[i].DecisionID < function.report.Decisions[j].DecisionID)
		})
		sort.Slice(function.report.Clauses, func(i, j int) bool {
			return lessLocation(function.report.Clauses[i].Location, function.report.Clauses[j].Location) || (function.report.Clauses[i].Location == function.report.Clauses[j].Location && function.report.Clauses[i].ClauseID < function.report.Clauses[j].ClauseID)
		})
		sort.Slice(function.report.NoMatches, func(i, j int) bool {
			return lessLocation(function.report.NoMatches[i].Location, function.report.NoMatches[j].Location) ||
				(function.report.NoMatches[i].Location == function.report.NoMatches[j].Location && function.report.NoMatches[i].SwitchID < function.report.NoMatches[j].SwitchID)
		})
		functions = append(functions, function.report)
	}
	sort.Slice(functions, func(i, j int) bool { return lessFunction(functions[i], functions[j]) })
	for _, function := range functions {
		file.Functions = append(file.Functions, function)
		addSummary(&file.Summary, function.Summary)
	}
	return file
}

func ensurePackageBuilder(builders map[string]*packageBuilder, path string, input Input) *packageBuilder {
	if builder := builders[path]; builder != nil {
		return builder
	}
	status := input.PackageStatuses[path]
	if status == "" {
		status = string(input.RunStatus)
	}
	builder := &packageBuilder{path: path, status: status, files: make(map[string]*fileBuilder)}
	builders[path] = builder
	return builder
}

func ensureFunctionBuilder(builder *packageBuilder, filePath, name string, location *cover.SourceLocation, coverage config.CoverageSet) *functionBuilder {
	file := builder.files[filePath]
	if file == nil {
		file = &fileBuilder{path: filePath, functions: make(map[string]*functionBuilder)}
		builder.files[filePath] = file
	}
	key := name
	if name == "init" && location != nil {
		key = fmt.Sprintf("init@%d:%d-%d:%d", location.Start.Line, location.Start.Column, location.End.Line, location.End.Column)
	}
	function := file.functions[key]
	if function == nil {
		function = &functionBuilder{report: FunctionReport{Name: name, Location: cloneLocation(location), Summary: newSummary(coverage), Statements: make([]StatementReport, 0), Decisions: make([]DecisionReport, 0), Clauses: make([]ClauseReport, 0)}}
		file.functions[key] = function
	} else if function.report.Location == nil {
		function.report.Location = cloneLocation(location)
	}
	return function
}

func cloneLocation(location *cover.SourceLocation) *cover.SourceLocation {
	if location == nil {
		return nil
	}
	cloned := *location
	return &cloned
}

func optionalLocation(location cover.SourceLocation) *cover.SourceLocation {
	if location.File == "" || location.Start == (cover.Position{}) || location.End == (cover.Position{}) {
		return nil
	}
	return &location
}

func newSummary(coverage config.CoverageSet) Summary {
	return Summary{
		Statement:                 newMetric(coverage.Enabled(config.MetricStatement)),
		Function:                  newMetric(coverage.Enabled(config.MetricFunction)),
		Decision:                  newMetric(coverage.Enabled(config.MetricDecision)),
		SwitchClauseBody:          newMetric(coverage.Enabled(config.MetricSwitchClauseBody)),
		TypeSwitchClauseBody:      newMetric(coverage.Enabled(config.MetricTypeSwitchClauseBody)),
		SelectClauseBody:          newMetric(coverage.Enabled(config.MetricSelectClauseBody)),
		SwitchClauseSelection:     newMetric(coverage.Enabled(config.MetricSwitchClauseSelection)),
		TypeSwitchClauseSelection: newMetric(coverage.Enabled(config.MetricTypeSwitchClauseSelection)),
		Condition:                 newMetric(coverage.Enabled(config.MetricCondition)),
		MCDCUnique:                newMetric(coverage.Enabled(config.MetricMCDCUnique)),
		MCDCMasking:               newMetric(coverage.Enabled(config.MetricMCDCMasking)),
	}
}

func newMetric(enabled bool) MetricSummary { return MetricSummary{Enabled: enabled} }

func metricForOutcomes(enabled bool, state entityState, trueCovered, falseCovered bool) MetricSummary {
	metric := newMetric(enabled)
	if !enabled {
		return metric
	}
	switch state {
	case entityUnknown:
		metric.Unknown = 2
	case entityUnsupported:
		metric.Unsupported = 2
	default:
		metric.Total = 2
		if trueCovered {
			metric.Covered++
		}
		if falseCovered {
			metric.Covered++
		}
	}
	metric.recalculate()
	return metric
}

func addMCDCStatus(
	metric *MetricSummary,
	outcome cover.CoverageOutcome,
	support cover.SupportStatus,
	analysis cover.AnalysisStatus,
	units int,
) {
	if !metric.Enabled {
		return
	}
	switch {
	case support == cover.SupportUnsupported:
		metric.Unsupported += units
	case support == cover.SupportUnknown:
		metric.Unknown += units
	case analysis == cover.AnalysisIncomplete:
		metric.AnalysisIncomplete += units
	case analysis == cover.AnalysisInfeasible:
		metric.Infeasible += units
	case outcome == cover.CoverageOutcomeCovered:
		metric.Covered += units
		metric.Total += units
	case outcome == cover.CoverageOutcomeNotCovered:
		metric.Total += units
	default:
		metric.Unknown += units
	}
	metric.recalculate()
}

func addSummary(destination *Summary, source Summary) {
	addMetric(&destination.Statement, source.Statement)
	addMetric(&destination.Function, source.Function)
	addMetric(&destination.Decision, source.Decision)
	addMetric(&destination.SwitchClauseBody, source.SwitchClauseBody)
	addMetric(&destination.TypeSwitchClauseBody, source.TypeSwitchClauseBody)
	addMetric(&destination.SelectClauseBody, source.SelectClauseBody)
	addMetric(&destination.SwitchClauseSelection, source.SwitchClauseSelection)
	addMetric(&destination.TypeSwitchClauseSelection, source.TypeSwitchClauseSelection)
	addMetric(&destination.Condition, source.Condition)
	addMetric(&destination.MCDCUnique, source.MCDCUnique)
	addMetric(&destination.MCDCMasking, source.MCDCMasking)
}

func addMetric(destination *MetricSummary, source MetricSummary) {
	if !destination.Enabled {
		return
	}
	destination.Covered += source.Covered
	destination.Total += source.Total
	destination.Unsupported += source.Unsupported
	destination.Unknown += source.Unknown
	destination.Infeasible += source.Infeasible
	destination.AnalysisIncomplete += source.AnalysisIncomplete
	destination.recalculate()
}

func (metric *MetricSummary) recalculate() {
	if metric.Total == 0 {
		metric.Percentage = nil
		return
	}
	percentage := float64(metric.Covered) * 100 / float64(metric.Total)
	value := math.Round(percentage*100) / 100
	metric.Percentage = &value
}

func stateForEvaluations(evaluations []cover.DecisionEvaluation, packageUnknown bool) entityState {
	if packageUnknown {
		return entityUnknown
	}
	completed := 0
	for _, evaluation := range evaluations {
		switch evaluation.Status {
		case cover.EvaluationCompleted:
			completed++
		}
	}
	if completed > 0 {
		return entityNormal
	}
	return entityNormal
}

func packageUnknown(status string, evidence bool) bool {
	if evidence {
		return false
	}
	switch status {
	case "failed", "build-failed", "started", "timeout":
		return true
	default:
		return false
	}
}

func astPackageStatus(input Input, packagePath, defaultStatus string) string {
	if status := input.ASTPackageStatuses[packagePath]; status != "" {
		return status
	}
	return defaultStatus
}

func cloneProducerCapabilities(values []backend.ProducerCapabilities) []backend.ProducerCapabilities {
	cloned := make([]backend.ProducerCapabilities, 0, len(values))
	for _, value := range values {
		cloned = append(cloned, backend.ProducerCapabilities{
			Backend:      value.Backend,
			Capabilities: value.Capabilities.Clone(),
		})
	}
	sort.Slice(cloned, func(i, j int) bool { return cloned[i].Backend < cloned[j].Backend })
	return cloned
}

func cloneMeasurementRuns(values []MeasurementRun) []MeasurementRun {
	cloned := make([]MeasurementRun, len(values))
	for index, value := range values {
		cloned[index] = value
		cloned[index].Packages = make(map[string]string, len(value.Packages))
		for packagePath, status := range value.Packages {
			cloned[index].Packages[packagePath] = status
		}
	}
	return cloned
}

func completedEvaluations(evaluations []cover.DecisionEvaluation) []cover.DecisionEvaluation {
	result := make([]cover.DecisionEvaluation, 0, len(evaluations))
	for _, evaluation := range evaluations {
		if evaluation.Status == cover.EvaluationCompleted {
			result = append(result, evaluation)
		}
	}
	return result
}

func conditionEvidence(evaluations []cover.DecisionEvaluation, index uint16) (bool, bool, int) {
	trueObserved, falseObserved, notEvaluated := false, false, 0
	for _, evaluation := range evaluations {
		if int(index) >= len(evaluation.Conditions) {
			continue
		}
		switch evaluation.Conditions[index] {
		case cover.ConditionTrue:
			trueObserved = true
		case cover.ConditionFalse:
			falseObserved = true
		case cover.ConditionNotEvaluated:
			notEvaluated++
		}
	}
	return trueObserved, falseObserved, notEvaluated
}

func evaluationReport(evaluation cover.DecisionEvaluation) EvaluationReport {
	conditions := make([]string, 0, len(evaluation.Conditions))
	for _, condition := range evaluation.Conditions {
		conditions = append(conditions, conditionStateString(condition))
	}
	status := "unknown"
	switch evaluation.Status {
	case cover.EvaluationCompleted:
		status = "completed"
	case cover.EvaluationAborted:
		status = "aborted"
	}
	testID := evaluation.TestID
	if testID == "" {
		testID = cover.UnknownTestID
	}
	return EvaluationReport{
		DecisionID:   formatID(uint64(evaluation.DecisionID)),
		EvaluationID: formatID(uint64(evaluation.EvaluationID)),
		Status:       status,
		RunID:        evaluation.RunID,
		PackagePath:  evaluation.PackagePath,
		ProcessID:    evaluation.ProcessID,
		TestID:       testID,
		Conditions:   conditions,
		Result:       evaluation.Result,
	}
}

func witnessReport(witness cover.MCDCWitness) *WitnessReport {
	return &WitnessReport{
		First:                evaluationReport(witness.First),
		Second:               evaluationReport(witness.Second),
		FirstCompletion:      stateStrings(witness.FirstCompletion),
		SecondCompletion:     stateStrings(witness.SecondCompletion),
		UnobservedConditions: append([]uint16{}, witness.UnobservedConditions...),
		MaskedConditions:     append([]uint16{}, witness.MaskedConditions...),
	}
}

func stateStrings(states []cover.ConditionState) []string {
	result := make([]string, 0, len(states))
	for _, state := range states {
		result = append(result, conditionStateString(state))
	}
	return result
}

func conditionStateString(state cover.ConditionState) string {
	switch state {
	case cover.ConditionTrue:
		return "true"
	case cover.ConditionFalse:
		return "false"
	case cover.ConditionNotEvaluated:
		return "not-evaluated"
	default:
		return fmt.Sprintf("unknown(%d)", state)
	}
}

func mcdcCondition(analysis MCDCAnalysisReport, index uint16) MCDCConditionReport {
	for _, condition := range analysis.Conditions {
		if condition.Index == index {
			return condition
		}
	}
	return MCDCConditionReport{Index: index, Status: statusDisabled}
}

func supportedClause(metadata cover.ClauseMetadata) bool {
	kindSupported := metadata.Kind == cover.ClauseExpressionSwitch ||
		metadata.Kind == cover.ClauseTypeSwitch ||
		metadata.Kind == cover.ClauseSelect ||
		metadata.Kind == cover.ClauseConditionlessSwitch
	roleSupported := metadata.Role == cover.ClauseCase || metadata.Role == cover.ClauseDefault
	return kindSupported && roleSupported
}

func addClauseMetrics(summary *Summary, kind cover.ClauseKind, body, selection MetricSummary) {
	switch kind {
	case cover.ClauseExpressionSwitch, cover.ClauseConditionlessSwitch:
		addMetric(&summary.SwitchClauseBody, body)
		addMetric(&summary.SwitchClauseSelection, selection)
	case cover.ClauseTypeSwitch:
		addMetric(&summary.TypeSwitchClauseBody, body)
		addMetric(&summary.TypeSwitchClauseSelection, selection)
	case cover.ClauseSelect:
		addMetric(&summary.SelectClauseBody, body)
	}
}

func addNoMatchMetric(summary *Summary, kind cover.ClauseKind, selection MetricSummary) {
	switch kind {
	case cover.ClauseExpressionSwitch:
		addMetric(&summary.SwitchClauseSelection, selection)
	case cover.ClauseTypeSwitch:
		addMetric(&summary.TypeSwitchClauseSelection, selection)
	}
}

func supportedDecision(kind cover.DecisionKind) bool {
	return kind == cover.DecisionIf || kind == cover.DecisionFor || kind == cover.DecisionSwitchCase
}

func lessDecision(left, right cover.DecisionMetadata) bool {
	if left.Package != right.Package {
		return left.Package < right.Package
	}
	if left.Location.File != right.Location.File {
		return left.Location.File < right.Location.File
	}
	if left.Function != right.Function {
		return left.Function < right.Function
	}
	if left.Location.Start != right.Location.Start {
		return lessPosition(left.Location.Start, right.Location.Start)
	}
	if left.Location.End != right.Location.End {
		return lessPosition(left.Location.End, right.Location.End)
	}
	return left.ID < right.ID
}

func lessClause(left, right cover.ClauseMetadata) bool {
	if left.Package != right.Package {
		return left.Package < right.Package
	}
	if left.Location.File != right.Location.File {
		return left.Location.File < right.Location.File
	}
	if left.Function != right.Function {
		return left.Function < right.Function
	}
	if left.Location.Start != right.Location.Start {
		return lessPosition(left.Location.Start, right.Location.Start)
	}
	if left.Location.End != right.Location.End {
		return lessPosition(left.Location.End, right.Location.End)
	}
	return left.ID < right.ID
}

func lessEvaluation(left, right cover.DecisionEvaluation) bool {
	if left.RunID != right.RunID {
		return left.RunID < right.RunID
	}
	if left.PackagePath != right.PackagePath {
		return left.PackagePath < right.PackagePath
	}
	if left.ProcessID != right.ProcessID {
		return left.ProcessID < right.ProcessID
	}
	if left.EvaluationID != right.EvaluationID {
		return left.EvaluationID < right.EvaluationID
	}
	if left.TestID != right.TestID {
		return left.TestID < right.TestID
	}
	return left.Status < right.Status
}

func lessFunction(left, right FunctionReport) bool {
	if left.Location != nil && right.Location != nil {
		if lessLocation(*left.Location, *right.Location) {
			return true
		}
		if lessLocation(*right.Location, *left.Location) {
			return false
		}
	} else if left.Location != nil {
		return true
	} else if right.Location != nil {
		return false
	}
	return left.Name < right.Name
}

func lessLocation(left, right cover.SourceLocation) bool {
	if left.File != right.File {
		return left.File < right.File
	}
	if left.Start != right.Start {
		return lessPosition(left.Start, right.Start)
	}
	return lessPosition(left.End, right.End)
}

func lessPosition(left, right cover.Position) bool {
	if left.Line != right.Line {
		return left.Line < right.Line
	}
	return left.Column < right.Column
}

func c0SourceLocation(file string, source c0.SourceRange) cover.SourceLocation {
	return cover.SourceLocation{
		File:  file,
		Start: cover.Position{Line: source.Start.Line, Column: source.Start.Column},
		End:   cover.Position{Line: source.End.Line, Column: source.End.Column},
	}
}

func formatID(id uint64) string { return fmt.Sprintf("0x%016x", id) }

func displayFunctionName(name string) string {
	if name == "" {
		return "<package>"
	}
	return name
}
