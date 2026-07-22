// Package backend describes instrumentation capabilities independently from
// runtime coverage evidence. A capability says what a producer can measure;
// it does not say whether a particular test execution covered an entity.
package backend

import (
	"math"
	"sort"

	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
)

// Capability identifies one semantically distinct instrumentation feature.
type Capability string

const (
	CapabilityStatementCoverage                 Capability = "statementCoverage"
	CapabilityFunctionCoverage                  Capability = "functionCoverage"
	CapabilityIfDecision                        Capability = "ifDecision"
	CapabilityForDecision                       Capability = "forDecision"
	CapabilityConditionlessSwitchDecision       Capability = "conditionlessSwitchDecision"
	CapabilityConditionCoverage                 Capability = "conditionCoverage"
	CapabilityMCDCUnique                        Capability = "mcdcUnique"
	CapabilityMCDCMasking                       Capability = "mcdcMasking"
	CapabilitySwitchClauseBody                  Capability = "switchClauseBody"
	CapabilityTypeSwitchClauseBody              Capability = "typeSwitchClauseBody"
	CapabilitySelectClauseBody                  Capability = "selectClauseBody"
	CapabilitySwitchClauseSelection             Capability = "switchClauseSelection"
	CapabilityTypeSwitchClauseSelection         Capability = "typeSwitchClauseSelection"
	CapabilityExpressionSwitchMatchedExpression Capability = "expressionSwitchMatchedExpression"
	CapabilityTypeSwitchMatchedTypeAlternative  Capability = "typeSwitchMatchedTypeAlternative"
	CapabilityDirectCaseSelection               Capability = "directCaseSelection"
	CapabilityFallthroughEdge                   Capability = "fallthroughEdge"
	CapabilityCFGEdge                           Capability = "cfgEdge"
	CapabilityImplicitBranch                    Capability = "implicitBranch"
)

// CapabilityStatus distinguishes explicit lack of backend support from a
// producer whose support cannot be determined.
type CapabilityStatus string

const (
	CapabilitySupported            CapabilityStatus = "supported"
	CapabilityUnsupportedByBackend CapabilityStatus = "unsupported-by-backend"
	CapabilityUnknown              CapabilityStatus = "unknown"
)

// CapabilitySet is the deterministic contract advertised by a backend.
type CapabilitySet map[Capability]CapabilityStatus

// Clone prevents callers from mutating a backend's advertised contract.
func (set CapabilitySet) Clone() CapabilitySet {
	clone := make(CapabilitySet, len(set))
	for capability, status := range set {
		clone[capability] = status
	}
	return clone
}

// Status returns unknown for a capability the backend did not advertise.
func (set CapabilitySet) Status(capability Capability) CapabilityStatus {
	if status, found := set[capability]; found {
		return status
	}
	return CapabilityUnknown
}

// InstrumentationBackend is the producer-side semantic boundary. Reporters
// consume the advertised set and never infer unavailable selection events.
type InstrumentationBackend interface {
	Capabilities() CapabilitySet
}

// ProducerCapabilities names one concrete producer in a composite
// measurement. The aggregate tool capability must not obscure which backend
// supplied C0 versus AST evidence.
type ProducerCapabilities struct {
	Backend      string        `json:"backend"`
	Capabilities CapabilitySet `json:"capabilities"`
}

// InstrumentationCoverage accounts for static entities independently from
// their runtime covered/not-covered state. Supported is a capability count;
// Instrumented is the subset for which probes were actually produced.
type InstrumentationCoverage struct {
	Discovered   int     `json:"discovered"`
	Supported    int     `json:"supported"`
	Instrumented int     `json:"instrumented"`
	Unsupported  int     `json:"unsupported"`
	Unknown      int     `json:"unknown"`
	Percentage   float64 `json:"percentage"`
}

// Add classifies count entities and records whether a supported entity was
// instrumented. It is intended for deterministic report construction.
func (coverage *InstrumentationCoverage) Add(status CapabilityStatus, count int, instrumented bool) {
	if count <= 0 {
		return
	}
	coverage.Discovered += count
	switch status {
	case CapabilitySupported:
		coverage.Supported += count
		if instrumented {
			coverage.Instrumented += count
		} else {
			coverage.Unknown += count
		}
	case CapabilityUnsupportedByBackend:
		coverage.Unsupported += count
	default:
		coverage.Unknown += count
	}
	coverage.recalculate()
}

// Merge aggregates another entity accounting record.
func (coverage *InstrumentationCoverage) Merge(other InstrumentationCoverage) {
	coverage.Discovered += other.Discovered
	coverage.Supported += other.Supported
	coverage.Instrumented += other.Instrumented
	coverage.Unsupported += other.Unsupported
	coverage.Unknown += other.Unknown
	coverage.recalculate()
}

func (coverage *InstrumentationCoverage) recalculate() {
	if coverage.Discovered == 0 {
		coverage.Percentage = 0
		return
	}
	value := float64(coverage.Instrumented) * 100 / float64(coverage.Discovered)
	coverage.Percentage = math.Round(value*100) / 100
}

// InstrumentationMetric is one requested metric's static probe accounting.
type InstrumentationMetric struct {
	Metric   string                  `json:"metric"`
	Coverage InstrumentationCoverage `json:"coverage"`
}

// InstrumentationReport is the explicit denominator behind the statement
// that unsupported and unknown entities were excluded from coverage metrics.
type InstrumentationReport struct {
	Total   InstrumentationCoverage `json:"total"`
	Metrics []InstrumentationMetric `json:"metrics"`
}

// NewInstrumentationReport sorts metric names and computes the total.
func NewInstrumentationReport(metrics map[string]InstrumentationCoverage) InstrumentationReport {
	names := make([]string, 0, len(metrics))
	for name := range metrics {
		names = append(names, name)
	}
	sort.Strings(names)
	report := InstrumentationReport{Metrics: make([]InstrumentationMetric, 0, len(names))}
	for _, name := range names {
		coverage := metrics[name]
		report.Metrics = append(report.Metrics, InstrumentationMetric{Metric: name, Coverage: coverage})
		report.Total.Merge(coverage)
	}
	return report
}

// HasGaps reports a strict-mode failure condition. A supported entity whose
// probe was not produced is unknown instrumentation, even if its capability
// classification itself was known.
func (report InstrumentationReport) HasGaps() bool {
	return report.Total.Unsupported > 0 ||
		report.Total.Unknown > 0 ||
		report.Total.Instrumented < report.Total.Supported
}

// ASTBackend is the source-rewriting producer used for decision-family and
// clause-body metrics. In dual-run mode it does not produce C0/function data.
type ASTBackend struct{}

var _ InstrumentationBackend = ASTBackend{}

// Capabilities reports the AST backend's exact measurement boundary.
func (ASTBackend) Capabilities() CapabilitySet {
	return CapabilitySet{
		CapabilityStatementCoverage:                 CapabilityUnsupportedByBackend,
		CapabilityFunctionCoverage:                  CapabilityUnsupportedByBackend,
		CapabilityIfDecision:                        CapabilitySupported,
		CapabilityForDecision:                       CapabilitySupported,
		CapabilityConditionlessSwitchDecision:       CapabilitySupported,
		CapabilityConditionCoverage:                 CapabilitySupported,
		CapabilityMCDCUnique:                        CapabilitySupported,
		CapabilityMCDCMasking:                       CapabilitySupported,
		CapabilitySwitchClauseBody:                  CapabilitySupported,
		CapabilityTypeSwitchClauseBody:              CapabilitySupported,
		CapabilitySelectClauseBody:                  CapabilitySupported,
		CapabilitySwitchClauseSelection:             CapabilityUnsupportedByBackend,
		CapabilityTypeSwitchClauseSelection:         CapabilityUnsupportedByBackend,
		CapabilityExpressionSwitchMatchedExpression: CapabilityUnsupportedByBackend,
		CapabilityTypeSwitchMatchedTypeAlternative:  CapabilityUnsupportedByBackend,
		CapabilityDirectCaseSelection:               CapabilityUnsupportedByBackend,
		CapabilityFallthroughEdge:                   CapabilityUnsupportedByBackend,
		CapabilityCFGEdge:                           CapabilityUnsupportedByBackend,
		CapabilityImplicitBranch:                    CapabilityUnsupportedByBackend,
	}
}

// StandardCoverBackend is the unchanged-source Go cover producer used for C0
// and the derived function-execution metric in standard-cover modes.
type StandardCoverBackend struct{}

var _ InstrumentationBackend = StandardCoverBackend{}

func (StandardCoverBackend) Capabilities() CapabilitySet {
	return CapabilitySet{
		CapabilityStatementCoverage: CapabilitySupported,
		CapabilityFunctionCoverage:  CapabilitySupported,
	}
}

// CompilerAwareBackend relocates generated switch markers while lowering the
// compiler IR, so dispatch selection remains distinct from body fallthrough.
type CompilerAwareBackend struct{}

var _ InstrumentationBackend = CompilerAwareBackend{}

func (CompilerAwareBackend) Capabilities() CapabilitySet {
	return CapabilitySet{
		CapabilitySwitchClauseSelection:             CapabilitySupported,
		CapabilityTypeSwitchClauseSelection:         CapabilitySupported,
		CapabilityExpressionSwitchMatchedExpression: CapabilitySupported,
		CapabilityTypeSwitchMatchedTypeAlternative:  CapabilitySupported,
		CapabilityDirectCaseSelection:               CapabilitySupported,
	}
}

// OrchestratedBackend is the tool-level union of the standard-cover, AST, and
// compiler-aware producers. A capability is supported when at least one
// concrete producer supports it; an explicit unsupported status is retained
// otherwise.
type OrchestratedBackend struct{}

var _ InstrumentationBackend = OrchestratedBackend{}

func (OrchestratedBackend) Capabilities() CapabilitySet {
	return MergeCapabilitySets(
		(StandardCoverBackend{}).Capabilities(),
		(ASTBackend{}).Capabilities(),
		(CompilerAwareBackend{}).Capabilities(),
	)
}

// OrchestratedProducers returns a fresh, deterministic producer breakdown for
// reports.
func OrchestratedProducers() []ProducerCapabilities {
	return []ProducerCapabilities{
		{Backend: "ast", Capabilities: (ASTBackend{}).Capabilities()},
		{Backend: "compiler-aware", Capabilities: (CompilerAwareBackend{}).Capabilities()},
		{Backend: "standard-cover", Capabilities: (StandardCoverBackend{}).Capabilities()},
	}
}

// MergeCapabilitySets constructs a tool-level union without treating an
// unadvertised capability as stronger than an explicit backend boundary.
func MergeCapabilitySets(sets ...CapabilitySet) CapabilitySet {
	merged := make(CapabilitySet)
	for _, set := range sets {
		for capability, status := range set {
			current, exists := merged[capability]
			switch {
			case status == CapabilitySupported:
				merged[capability] = status
			case !exists || current == CapabilityUnknown:
				merged[capability] = status
			}
		}
	}
	return merged
}

// DecisionCapability maps a source decision kind to its advertised feature.
// The false return deliberately means unknown, rather than unsupported.
func DecisionCapability(kind cover.DecisionKind) (Capability, bool) {
	switch kind {
	case cover.DecisionIf:
		return CapabilityIfDecision, true
	case cover.DecisionFor:
		return CapabilityForDecision, true
	case cover.DecisionSwitchCase:
		return CapabilityConditionlessSwitchDecision, true
	default:
		return "", false
	}
}

// ClauseBodyCapability maps only formal source-body metrics. Exact case
// selection and fallthrough edges are intentionally not returned here.
func ClauseBodyCapability(kind cover.ClauseKind) (Capability, bool) {
	switch kind {
	case cover.ClauseExpressionSwitch, cover.ClauseConditionlessSwitch:
		return CapabilitySwitchClauseBody, true
	case cover.ClauseTypeSwitch:
		return CapabilityTypeSwitchClauseBody, true
	case cover.ClauseSelect:
		return CapabilitySelectClauseBody, true
	default:
		return "", false
	}
}
