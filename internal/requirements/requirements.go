// Package requirements decides whether a completed analysis satisfies explicit
// user requirements. It never collects evidence or changes coverage outcomes.
package requirements

import (
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/shrydev2020/gomcdc/v2/internal/analysis"
)

type Requirements struct {
	Complete bool
	Minimum  map[string]float64
}

type Satisfaction struct {
	Requirement string `json:"requirement"`
	Satisfied   bool   `json:"satisfied"`
	Reason      string `json:"reason"`
}

func Metrics(s analysis.Summary) map[string]analysis.MetricSummary {
	return map[string]analysis.MetricSummary{"statement": s.Statement, "function": s.Function, "decision": s.Decision, "condition": s.Condition,
		"switch-clause-body": s.SwitchClauseBody, "type-switch-clause-body": s.TypeSwitchClauseBody, "select-clause-body": s.SelectClauseBody,
		"switch-clause-selection": s.SwitchClauseSelection, "type-switch-clause-selection": s.TypeSwitchClauseSelection,
		"mcdc-unique": s.MCDCUnique, "mcdc-masking": s.MCDCMasking}
}

func Evaluate(result analysis.Result, requested Requirements) ([]Satisfaction, error) {
	if !requested.Complete && len(requested.Minimum) == 0 {
		return nil, errors.New("specify at least one coverage requirement")
	}
	metrics := Metrics(result.Summary)
	var outcomes []Satisfaction
	if requested.Complete {
		complete := result.Run.Complete && !result.Instrumentation.HasGaps()
		for _, m := range metrics {
			if m.Enabled && (m.Unknown > 0 || m.Unsupported > 0 || m.AnalysisIncomplete > 0) {
				complete = false
			}
		}
		reason := "measurement and analysis are complete"
		if !complete {
			reason = "execution, evidence, instrumentation, or analysis is incomplete"
		}
		outcomes = append(outcomes, Satisfaction{"complete", complete, reason})
	}
	names := make([]string, 0, len(requested.Minimum))
	for name := range requested.Minimum {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		minimum := requested.Minimum[name]
		metric, ok := metrics[name]
		if !ok || !metric.Enabled {
			return nil, fmt.Errorf("%s was not measured", name)
		}
		if math.IsNaN(minimum) || math.IsInf(minimum, 0) || minimum < 0 || minimum > 100 {
			return nil, fmt.Errorf("%s minimum must be between 0 and 100", name)
		}
		satisfied := metric.Total > 0 && float64(metric.Covered)*100/float64(metric.Total) >= minimum
		reason := fmt.Sprintf("%d/%d; minimum %.2f%%", metric.Covered, metric.Total, minimum)
		if metric.Total == 0 {
			reason = "no feasible measured obligations"
		}
		if metric.Unknown > 0 || metric.Unsupported > 0 || metric.AnalysisIncomplete > 0 {
			satisfied = false
			reason = "coverage cannot establish the requirement while obligations are unknown, unsupported, or analysis-incomplete"
		}
		outcomes = append(outcomes, Satisfaction{name, satisfied, reason})
	}
	return outcomes, nil
}
