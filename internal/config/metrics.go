package config

import (
	"fmt"
	"sort"
	"strings"

	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
)

// Metric is the shared coverage-domain metric type. This package owns only
// CLI aliases and selection policy, not a parallel metric vocabulary.
type Metric = cover.CoverageMetric

const (
	MetricStatement                 = cover.CoverageMetricStatement
	MetricFunction                  = cover.CoverageMetricFunction
	MetricDecision                  = cover.CoverageMetricDecision
	MetricSwitchClauseBody          = cover.CoverageMetricSwitchClauseBody
	MetricTypeSwitchClauseBody      = cover.CoverageMetricTypeSwitchClauseBody
	MetricSelectClauseBody          = cover.CoverageMetricSelectClauseBody
	MetricSwitchClauseSelection     = cover.CoverageMetricSwitchClauseSelection
	MetricTypeSwitchClauseSelection = cover.CoverageMetricTypeSwitchClauseSelection
	MetricCondition                 = cover.CoverageMetricCondition
	MetricMCDCUnique                = cover.CoverageMetricMCDCUnique
	MetricMCDCMasking               = cover.CoverageMetricMCDCMasking
)

var allMetrics = []Metric{
	MetricStatement,
	MetricFunction,
	MetricDecision,
	MetricSwitchClauseBody,
	MetricTypeSwitchClauseBody,
	MetricSelectClauseBody,
	MetricSwitchClauseSelection,
	MetricTypeSwitchClauseSelection,
	MetricCondition,
	MetricMCDCUnique,
	MetricMCDCMasking,
}

type CoverageSet map[Metric]bool

func AllCoverage() CoverageSet {
	set := make(CoverageSet, len(allMetrics))
	for _, metric := range allMetrics {
		set[metric] = true
	}
	return set
}

func ParseCoverage(value string) (CoverageSet, error) {
	set := make(CoverageSet)
	for _, name := range strings.Split(value, ",") {
		if name == "" {
			return nil, fmt.Errorf("coverage metric list contains an empty token")
		}
		var expanded []Metric
		switch name {
		case "all":
			expanded = allMetrics
		case "statement":
			expanded = []Metric{MetricStatement}
		case "function":
			expanded = []Metric{MetricFunction}
		case "decision":
			expanded = []Metric{MetricDecision}
		case "switch-clause-body":
			expanded = []Metric{MetricSwitchClauseBody}
		case "type-switch-clause-body":
			expanded = []Metric{MetricTypeSwitchClauseBody}
		case "select-clause-body":
			expanded = []Metric{MetricSelectClauseBody}
		case "switch-clause-selection":
			expanded = []Metric{MetricSwitchClauseSelection}
		case "type-switch-clause-selection":
			expanded = []Metric{MetricTypeSwitchClauseSelection}
		case "condition":
			expanded = []Metric{MetricCondition}
		case "mcdc-unique":
			expanded = []Metric{MetricMCDCUnique}
		case "mcdc-masking":
			expanded = []Metric{MetricMCDCMasking}
		default:
			return nil, fmt.Errorf("unknown coverage metric %q", name)
		}
		for _, metric := range expanded {
			set[metric] = true
		}
	}
	if len(set) == 0 {
		return nil, fmt.Errorf("coverage metric list must not be empty")
	}
	return set, nil
}

func (s CoverageSet) Enabled(metric Metric) bool { return s[metric] }

func (s CoverageSet) Names() []string {
	result := make([]string, 0, len(s))
	for metric, enabled := range s {
		if enabled {
			result = append(result, string(metric))
		}
	}
	sort.Strings(result)
	return result
}
