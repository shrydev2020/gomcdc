// Package coverage defines the shared source, runtime-evidence, and result
// vocabulary used by every measurement backend.
package coverage

import "fmt"

// DecisionID identifies a decision within one source revision.
type DecisionID uint64

// ConditionID identifies one atomic condition occurrence within one source
// revision.
type ConditionID uint64

// EvaluationID identifies one dynamic evaluation of a decision. Producers
// should make it unique within a coverage run. EvaluationIdentity also carries
// process provenance so separately produced package data cannot be merged by a
// bare counter accidentally.
type EvaluationID uint64

// ClauseID and ClauseGroupID are deterministic within one source revision.
type ClauseID uint64
type ClauseGroupID uint64

// SwitchID identifies one switch dispatch independently of its source clauses.
type SwitchID uint64

// DecisionKind identifies a boolean control-flow decision.
type DecisionKind string

const (
	DecisionIf         DecisionKind = "if"
	DecisionFor        DecisionKind = "for"
	DecisionSwitchCase DecisionKind = "switch-case"
)

// Position is a one-based source position.
type Position struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

// SourceLocation is a half-open range in an original (un-instrumented) file.
// Instrumentation locations must be normalized before constructing this type.
type SourceLocation struct {
	File  string   `json:"file"`
	Start Position `json:"start"`
	End   Position `json:"end"`
	// Offsets are byte offsets in the original, uninstrumented source. They
	// are internal identity data; line/column remains the public display form.
	StartOffset int `json:"-"`
	EndOffset   int `json:"-"`
}

// ClauseKind identifies the control construct whose clause body execution is
// measured. It is intentionally separate from DecisionKind.
type ClauseKind string

const (
	ClauseExpressionSwitch    ClauseKind = "expression-switch"
	ClauseTypeSwitch          ClauseKind = "type-switch"
	ClauseSelect              ClauseKind = "select"
	ClauseConditionlessSwitch ClauseKind = "conditionless-switch"
)

// ClauseRole distinguishes explicit source clauses. A no-match selection is
// represented by NoMatchMetadata and never by a synthetic ClauseMetadata.
type ClauseRole string

const (
	ClauseCase    ClauseRole = "case"
	ClauseDefault ClauseRole = "default"
)

// NoMatchMetadata is the selection obligation for a switch without default.
// It has a SwitchID and source location, but no ClauseID because no source
// clause was selected.
type NoMatchMetadata struct {
	SwitchID         SwitchID       `json:"switchId"`
	ModulePath       string         `json:"modulePath"`
	Package          string         `json:"package"`
	Function         string         `json:"function"`
	FunctionLocation SourceLocation `json:"functionLocation"`
	Kind             ClauseKind     `json:"kind"`
	Location         SourceLocation `json:"location"`
}

// ClauseMetadata describes one switch/type-switch/select source clause.
// Expressions preserves the individual case expressions for future finer
// reporting even though v1 aggregates at clause granularity. DecisionIDs are
// set for conditionless-switch case expressions, each of which is also its own
// boolean decision.
type ClauseMetadata struct {
	ID               ClauseID       `json:"id"`
	GroupID          ClauseGroupID  `json:"groupId"`
	SwitchID         SwitchID       `json:"switchId"`
	ModulePath       string         `json:"modulePath"`
	Package          string         `json:"package"`
	Function         string         `json:"function"`
	FunctionLocation SourceLocation `json:"functionLocation"`
	Kind             ClauseKind     `json:"kind"`
	Role             ClauseRole     `json:"role"`
	Index            uint16         `json:"index"`
	Location         SourceLocation `json:"location"`
	Expressions      []string       `json:"expressions,omitempty"`
	Types            []string       `json:"types,omitempty"`
	DecisionIDs      []DecisionID   `json:"decisionIds,omitempty"`
}

// ClauseEventKind keeps AST body execution distinct from exact compiler-aware
// dispatch selection.
type ClauseEventKind string

const (
	ClauseDirectSelection  ClauseEventKind = "direct-selection"
	ClauseBodyExecution    ClauseEventKind = "body-execution"
	ClauseNoMatchSelection ClauseEventKind = "no-match-selection"
)

// ClauseObservation is a verified, provenance-free coverage observation used
// for aggregation. AlternativeKnown distinguishes case alternative zero from
// default/no-match events, which have no alternative. Runtime transport data
// must retain provenance until it is verified and projected into this type.
type ClauseObservation struct {
	SwitchID         SwitchID        `json:"switchId,omitempty"`
	ClauseID         ClauseID        `json:"clauseId,omitempty"`
	Event            ClauseEventKind `json:"event"`
	AlternativeIndex uint16          `json:"alternativeIndex,omitempty"`
	AlternativeKnown bool            `json:"alternativeKnown,omitempty"`
}

// DecisionNotEvaluatedObservation records that a source decision was skipped
// because an earlier conditionless-switch case expression evaluated true.
// CauseEvaluationID and its provenance identify the completed evaluation that
// caused the skip; no decision result is invented for the skipped expression.
type DecisionNotEvaluatedObservation struct {
	DecisionID        DecisionID   `json:"decisionId"`
	CauseDecisionID   DecisionID   `json:"causeDecisionId"`
	CauseEvaluationID EvaluationID `json:"causeEvaluationId"`
	RunID             string       `json:"runId,omitempty"`
	PackagePath       string       `json:"packagePath,omitempty"`
	ProcessID         int          `json:"processId,omitempty"`
}

// ConditionMetadata describes one atomic condition in a decision. Index is
// the stable, zero-based position used by runtime evaluation vectors.
type ConditionMetadata struct {
	ID         ConditionID    `json:"id"`
	Index      uint16         `json:"index"`
	Expression string         `json:"expression"`
	Location   SourceLocation `json:"location"`
}

// BooleanExpressionKind identifies the supported logical structure used to
// prove masking. Predicates and other indivisible boolean expressions are
// represented by BooleanExpressionCondition leaves.
type BooleanExpressionKind string

const (
	BooleanExpressionCondition BooleanExpressionKind = "condition"
	BooleanExpressionConstant  BooleanExpressionKind = "constant"
	BooleanExpressionNot       BooleanExpressionKind = "not"
	BooleanExpressionAnd       BooleanExpressionKind = "and"
	BooleanExpressionOr        BooleanExpressionKind = "or"
)

// BooleanExpression is a read-once expression tree over condition indexes.
//
//   - condition: ConditionIndex is used; Left and Right must be nil.
//   - constant: Constant is used; Left and Right must be nil.
//   - not: Left is the operand; Right must be nil.
//   - and/or: Left and Right are the operands.
//
// Each atomic occurrence has its own condition index, including repeated
// source text. This preserves actual evaluation order and avoids inferring
// value coupling for expressions that may contain calls or side effects.
type BooleanExpression struct {
	Kind           BooleanExpressionKind `json:"kind"`
	ConditionIndex uint16                `json:"conditionIndex,omitempty"`
	Constant       bool                  `json:"constant,omitempty"`
	Left           *BooleanExpression    `json:"left,omitempty"`
	Right          *BooleanExpression    `json:"right,omitempty"`
}

// NewConditionExpression constructs an atomic-condition expression node.
func NewConditionExpression(index uint16) *BooleanExpression {
	return &BooleanExpression{Kind: BooleanExpressionCondition, ConditionIndex: index}
}

// NewConstantExpression constructs a boolean constant expression node.
func NewConstantExpression(value bool) *BooleanExpression {
	return &BooleanExpression{Kind: BooleanExpressionConstant, Constant: value}
}

// NewNotExpression constructs a logical negation expression node.
func NewNotExpression(operand *BooleanExpression) *BooleanExpression {
	return &BooleanExpression{Kind: BooleanExpressionNot, Left: operand}
}

// NewAndExpression constructs a short-circuit conjunction expression node.
func NewAndExpression(left, right *BooleanExpression) *BooleanExpression {
	return &BooleanExpression{Kind: BooleanExpressionAnd, Left: left, Right: right}
}

// NewOrExpression constructs a short-circuit disjunction expression node.
func NewOrExpression(left, right *BooleanExpression) *BooleanExpression {
	return &BooleanExpression{Kind: BooleanExpressionOr, Left: left, Right: right}
}

// DecisionMetadata describes one stable decision occurrence in the original
// (un-instrumented) source tree. Location is always normalized to the original source.
type DecisionMetadata struct {
	ID               DecisionID
	ModulePath       string
	Package          string
	Function         string
	FunctionLocation SourceLocation
	Kind             DecisionKind
	Location         SourceLocation
	Expression       string
	Conditions       []ConditionMetadata
	ExpressionTree   *BooleanExpression
}

// StableKey contains every field that contributes to a decision ID. It is
// retained so callers can detect the extremely unlikely event of a hash
// collision instead of silently merging unrelated decisions.
func (d DecisionMetadata) StableKey() string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%d\x00%s",
		d.ModulePath,
		d.Package,
		d.Location.File,
		d.Location.StartOffset,
		d.Location.EndOffset,
		d.Kind,
	)
}

// ConditionState records whether an atomic condition was evaluated and, when
// it was, its result. Not evaluated is distinct from false by construction.
type ConditionState uint8

const (
	ConditionNotEvaluated ConditionState = iota
	ConditionFalse
	ConditionTrue
)

// IsEvaluated reports whether this state contains a boolean result.
func (s ConditionState) IsEvaluated() bool {
	return s == ConditionFalse || s == ConditionTrue
}

// Bool returns the condition result and whether the state was evaluated.
func (s ConditionState) Bool() (value bool, evaluated bool) {
	switch s {
	case ConditionFalse:
		return false, true
	case ConditionTrue:
		return true, true
	default:
		return false, false
	}
}

func (s ConditionState) String() string {
	switch s {
	case ConditionNotEvaluated:
		return "not-evaluated"
	case ConditionFalse:
		return "false"
	case ConditionTrue:
		return "true"
	default:
		return fmt.Sprintf("ConditionState(%d)", s)
	}
}

// EvaluationStatus distinguishes completed decisions from evaluations that
// exited through panic or another interruption before EndDecision.
type EvaluationStatus uint8

const (
	EvaluationCompleted EvaluationStatus = iota
	EvaluationAborted
)

// UnknownTestID is used when a runtime cannot reliably associate an
// evaluation with a test or subtest.
const UnknownTestID = "unknown"

// DecisionEvaluation is the complete runtime evidence for one decision
// evaluation. Aborted evaluations are retained for diagnostics but cannot
// establish any coverage metric.
type DecisionEvaluation struct {
	DecisionID   DecisionID       `json:"decisionId"`
	EvaluationID EvaluationID     `json:"evaluationId"`
	RunID        string           `json:"runId,omitempty"`
	PackagePath  string           `json:"packagePath,omitempty"`
	ProcessID    int              `json:"processId,omitempty"`
	TestID       string           `json:"testId"`
	Conditions   []ConditionState `json:"conditions"`
	Result       bool             `json:"result"`
	Status       EvaluationStatus `json:"status"`
}

// EvaluationIdentity is the collision-free key used when data from separate
// package test processes is merged.
type EvaluationIdentity struct {
	RunID        string       `json:"runId"`
	PackagePath  string       `json:"packagePath"`
	ProcessID    int          `json:"processId"`
	EvaluationID EvaluationID `json:"evaluationId"`
}

// Identity returns the merge-safe identity for this evaluation.
func (e DecisionEvaluation) Identity() EvaluationIdentity {
	return EvaluationIdentity{
		RunID:        e.RunID,
		PackagePath:  e.PackagePath,
		ProcessID:    e.ProcessID,
		EvaluationID: e.EvaluationID,
	}
}

// CoverageMetric names an independently aggregated coverage measure.
type CoverageMetric string

const (
	CoverageMetricStatement                 CoverageMetric = "statement"
	CoverageMetricFunction                  CoverageMetric = "function"
	CoverageMetricDecision                  CoverageMetric = "decision"
	CoverageMetricSwitchClauseBody          CoverageMetric = "switch-clause-body"
	CoverageMetricTypeSwitchClauseBody      CoverageMetric = "type-switch-clause-body"
	CoverageMetricSelectClauseBody          CoverageMetric = "select-clause-body"
	CoverageMetricSwitchClauseSelection     CoverageMetric = "switch-clause-selection"
	CoverageMetricTypeSwitchClauseSelection CoverageMetric = "type-switch-clause-selection"
	CoverageMetricCondition                 CoverageMetric = "condition"
	CoverageMetricMCDCUnique                CoverageMetric = "mcdc-unique"
	CoverageMetricMCDCMasking               CoverageMetric = "mcdc-masking"
)

// CoverageStatus is the normative coverage axis. Backend support and analysis
// completeness are represented independently by SupportStatus and
// AnalysisStatus.
type CoverageStatus string

const (
	CoverageCovered            CoverageStatus = "covered"
	CoverageNotCovered         CoverageStatus = "not-covered"
	CoverageAnalysisIncomplete CoverageStatus = "analysis-incomplete"
	CoverageInfeasible         CoverageStatus = "infeasible"
)

// CoverageOutcome describes only whether an applicable obligation was
// observed. It is independent from backend support and analysis completeness.
type CoverageOutcome string

const (
	CoverageOutcomeCovered    CoverageOutcome = "covered"
	CoverageOutcomeNotCovered CoverageOutcome = "not-covered"
	CoverageOutcomeUnknown    CoverageOutcome = "unknown"
)

type SupportStatus string

const (
	SupportSupported   SupportStatus = "supported"
	SupportUnsupported SupportStatus = "unsupported-by-backend"
	SupportUnknown     SupportStatus = "unknown"
)

type AnalysisStatus string

const (
	AnalysisComplete   AnalysisStatus = "complete"
	AnalysisIncomplete AnalysisStatus = "incomplete"
	AnalysisInfeasible AnalysisStatus = "infeasible"
)

// CoverageCount carries the numerator and denominator plus categories that are
// excluded from the denominator by the default reporting policy.
type CoverageCount struct {
	Covered     int `json:"covered"`
	Total       int `json:"total"`
	Unsupported int `json:"unsupported,omitempty"`
	Unknown     int `json:"unknown,omitempty"`
	Aborted     int `json:"aborted,omitempty"`
	Infeasible  int `json:"infeasible,omitempty"`
}

// Percentage returns a stable zero value for an empty denominator.
func (c CoverageCount) Percentage() float64 {
	if c.Total == 0 {
		return 0
	}
	return float64(c.Covered) * 100 / float64(c.Total)
}

// MCDCWitness is the evidence pair for one condition. The completion vectors
// are populated by masking analysis when not-evaluated states had to be given
// counterfactual values; they contain only ConditionFalse/ConditionTrue.
type MCDCWitness struct {
	First                DecisionEvaluation `json:"first"`
	Second               DecisionEvaluation `json:"second"`
	FirstCompletion      []ConditionState   `json:"firstCompletion,omitempty"`
	SecondCompletion     []ConditionState   `json:"secondCompletion,omitempty"`
	UnobservedConditions []uint16           `json:"unobservedConditions,omitempty"`
	MaskedConditions     []uint16           `json:"maskedConditions,omitempty"`
}

// MCDCConditionResult records coverage and evidence for one atomic condition.
type MCDCConditionResult struct {
	ConditionIndex uint16          `json:"conditionIndex"`
	Outcome        CoverageOutcome `json:"outcome"`
	Support        SupportStatus   `json:"support"`
	Analysis       AnalysisStatus  `json:"analysis"`
	Witness        *MCDCWitness    `json:"witness,omitempty"`
	Reason         string          `json:"reason,omitempty"`
}

// MCDCResult is a strategy-specific, decision-level analysis result.
type MCDCResult struct {
	DecisionID          DecisionID            `json:"decisionId"`
	Metric              CoverageMetric        `json:"metric"`
	Outcome             CoverageOutcome       `json:"outcome"`
	Support             SupportStatus         `json:"support"`
	Analysis            AnalysisStatus        `json:"analysis"`
	Conditions          []MCDCConditionResult `json:"conditions"`
	EvaluationsAnalyzed int                   `json:"evaluationsAnalyzed"`
	AbortedEvaluations  int                   `json:"abortedEvaluations,omitempty"`
	InvalidEvaluations  int                   `json:"invalidEvaluations,omitempty"`
	Reason              string                `json:"reason,omitempty"`
}

// CoveredConditions returns the MCDC numerator for this decision.
func (r MCDCResult) CoveredConditions() int {
	covered := 0
	for _, condition := range r.Conditions {
		if condition.Outcome == CoverageOutcomeCovered &&
			condition.Support == SupportSupported &&
			condition.Analysis == AnalysisComplete {
			covered++
		}
	}
	return covered
}

// RunStatus describes the go test subprocess independently from coverage.
type RunStatus string

const (
	RunPassed  RunStatus = "passed"
	RunFailed  RunStatus = "failed"
	RunTimeout RunStatus = "timeout"
)

// RunFailureKind distinguishes build/tool failures from executed test
// failures while RunStatus retains the coarse passed/failed/timeout state.
type RunFailureKind string

const (
	RunFailureNone    RunFailureKind = "none"
	RunFailureBuild   RunFailureKind = "build"
	RunFailureTest    RunFailureKind = "test"
	RunFailureMixed   RunFailureKind = "mixed"
	RunFailureCommand RunFailureKind = "command"
	RunFailureTimeout RunFailureKind = "timeout"
)
