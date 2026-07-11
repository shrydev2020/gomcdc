package config

import (
	"fmt"
	"sort"
	"strings"

	cover "github.com/shrydev2020/gomcdc/internal/coverage"
)

// Metric is the shared coverage-domain metric type. This package owns only
// CLI aliases and selection policy, not a parallel metric vocabulary.
type Metric = cover.CoverageMetric

const (
	MetricStatement   = cover.CoverageMetricStatement
	MetricFunction    = cover.CoverageMetricFunction
	MetricDecision    = cover.CoverageMetricDecision
	MetricClause      = cover.CoverageMetricClause
	MetricCondition   = cover.CoverageMetricCondition
	MetricMCDCUnique  = cover.CoverageMetricMCDCUnique
	MetricMCDCMasking = cover.CoverageMetricMCDCMasking
)

var allMetrics = []Metric{
	MetricStatement,
	MetricFunction,
	MetricDecision,
	MetricClause,
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
	for _, raw := range strings.Split(value, ",") {
		name := strings.TrimSpace(strings.ToLower(raw))
		if name == "" {
			continue
		}
		var expanded []Metric
		switch name {
		case "all":
			expanded = allMetrics
		case "c0", "statement":
			expanded = []Metric{MetricStatement}
		case "function":
			expanded = []Metric{MetricFunction}
		case "c1", "decision":
			expanded = []Metric{MetricDecision}
		case "clause":
			expanded = []Metric{MetricClause}
		case "c2", "condition":
			expanded = []Metric{MetricCondition}
		case "mcdc":
			expanded = []Metric{MetricMCDCUnique, MetricMCDCMasking}
		case "mcdc-unique":
			expanded = []Metric{MetricMCDCUnique}
		case "mcdc-masking":
			expanded = []Metric{MetricMCDCMasking}
		default:
			return nil, fmt.Errorf("unknown coverage metric %q", raw)
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
