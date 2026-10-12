package report

import (
	"fmt"
	"strings"

	"github.com/shrydev2020/gomcdc/v2/internal/analysis"
)

type htmlMetric struct {
	Name, Short string
	Summary     analysis.MetricSummary
}

func htmlMetrics(s analysis.Summary) []htmlMetric {
	return []htmlMetric{
		{"Statement", "Stmt", s.Statement}, {"Function", "Func", s.Function}, {"Decision", "Decision", s.Decision},
		{"Switch clause body", "Sw body", s.SwitchClauseBody}, {"Type switch clause body", "Type body", s.TypeSwitchClauseBody},
		{"Select clause body", "Select body", s.SelectClauseBody}, {"Switch clause selection", "Sw select", s.SwitchClauseSelection},
		{"Type switch clause selection", "Type select", s.TypeSwitchClauseSelection}, {"Condition", "Condition", s.Condition},
		{"Unique-Cause MC/DC", "UC MC/DC", s.MCDCUnique}, {"Masking MC/DC", "Mask MC/DC", s.MCDCMasking},
	}
}

func htmlMetricStatus(metric analysis.MetricSummary) string {
	switch {
	case !metric.Enabled:
		return "disabled"
	case metric.AnalysisIncomplete > 0:
		return "analysis-incomplete"
	case metric.Unknown > 0:
		return "unknown"
	case metric.Unsupported > 0:
		return "unsupported"
	case metric.Infeasible > 0:
		return "infeasible"
	case metric.Total == 0:
		return "empty"
	case metric.Covered == metric.Total:
		return "complete"
	case metric.Covered == 0:
		return "not-covered"
	default:
		return "partial"
	}
}

func htmlSummaryGaps(summary analysis.Summary) int {
	gaps := 0
	for _, metric := range htmlMetrics(summary) {
		if !metric.Summary.Enabled {
			continue
		}
		gaps += metric.Summary.Total - metric.Summary.Covered
		gaps += metric.Summary.Unknown + metric.Summary.Unsupported + metric.Summary.Infeasible + metric.Summary.AnalysisIncomplete
	}
	return gaps
}

func htmlSummaryState(summary analysis.Summary) string {
	if htmlSummaryGaps(summary) > 0 {
		return "attention"
	}
	for _, metric := range htmlMetrics(summary) {
		if metric.Summary.Enabled && metric.Summary.Total > 0 {
			return "complete"
		}
	}
	return "empty"
}
func htmlPercentage(m analysis.MetricSummary) string {
	if m.Percentage == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.2f%%", *m.Percentage)
}
func htmlMetricClass(m analysis.MetricSummary) string {
	switch {
	case !m.Enabled:
		return "disabled"
	case m.Unknown > 0 || m.Unsupported > 0 || m.AnalysisIncomplete > 0:
		return "attention"
	case m.Total == 0:
		return "empty"
	case m.Covered == m.Total:
		return "covered"
	case m.Covered == 0:
		return "uncovered"
	default:
		return "partial"
	}
}

func (s *htmlReportRenderer) metrics(summary analysis.Summary) string {
	var b strings.Builder
	b.WriteString(`<div class="metrics">`)
	for _, m := range htmlMetrics(summary) {
		fmt.Fprintf(&b, `<div class="metric %s"><div class="name" title="%s">%s</div>%s<div class="value">%s</div><div>%d / %d</div><small>unknown %d · unsupported %d · infeasible %d · analysis-incomplete %d</small></div>`, htmlMetricClass(m.Summary), m.Name, m.Name, s.output.status(htmlMetricStatus(m.Summary)), htmlPercentage(m.Summary), m.Summary.Covered, m.Summary.Total, m.Summary.Unknown, m.Summary.Unsupported, m.Summary.Infeasible, m.Summary.AnalysisIncomplete)
	}
	b.WriteString(`</div>`)
	return b.String()
}
