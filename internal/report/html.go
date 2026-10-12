package report

import (
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"strconv"
	"strings"

	"github.com/shrydev2020/gomcdc/v2/internal/analysis"
	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
)

type htmlMetric struct {
	Name, Short string
	Summary     analysis.MetricSummary
}

// WriteHTMLReport renders an analysis result as a self-contained HTML document.
func WriteHTMLReport(w io.Writer, report analysis.Result) error {
	return writeHTMLReport(w, report)
}

func writeHTMLReport(w io.Writer, value analysis.Result) error {
	return WriteHTMLWithSources(w, value, nil)
}

// WriteHTMLWithSources renders an analysis result and its original sources
// as a self-contained HTML document.
func WriteHTMLWithSources(w io.Writer, value analysis.Result, sources []analysis.SourceFileInput) error {
	t, err := template.New("report").Funcs(template.FuncMap{
		"metrics": htmlMetrics, "pct": htmlPercentage, "class": htmlMetricClass,
		"triageMetrics": htmlTriageMetrics, "metricStatus": htmlMetricStatus,
		"summaryGaps": htmlSummaryGaps, "summaryState": htmlSummaryState,
		"loc": htmlLocation, "complete": completeness, "sourceSegments": htmlSourceSegments,
		"sourceModes": htmlSourceModes, "decisionAnchor": htmlDecisionAnchor,
		"sourceAnchor": htmlSourceAnchor, "locationRange": htmlLocationRange,
		"witnessView": htmlWitnessView, "evaluationRows": htmlEvaluationRows,
		"logField": htmlLogField,
	}).Parse(htmlDocument + `{{define "report.css"}}` + htmlStyles + `{{end}}`)
	if err != nil {
		return fmt.Errorf("parse HTML report template: %w", err)
	}
	view := struct {
		htmlReport
		Findings []htmlFinding
	}{sourceView(value, sources), htmlFindings(value)}
	if err := t.Execute(w, view); err != nil {
		return fmt.Errorf("render HTML report: %w", err)
	}
	return nil
}

// htmlLogField quotes field delimiters within a tab-delimited observation.
func htmlLogField(value string) string {
	if strings.ContainsAny(value, "\t\n\r\"\\") {
		return strconv.Quote(value)
	}
	return value
}

type htmlSourceSegment struct {
	Text       string
	Class      string
	Tooltip    string
	Metrics    string
	Start, End int
}

func htmlSourceModes() []string {
	return []string{"statement", "decision", "condition", "mcdc", "clause", "combined"}
}

func htmlSourceSegments(view *SourceFileView, metric string) []htmlSourceSegment {
	if view == nil {
		return nil
	}
	filtered := *view
	filtered.Annotations = make([]SourceAnnotation, 0, len(view.Annotations))
	for _, annotation := range view.Annotations {
		if sourceMetricMatches(annotation.Metric, metric) {
			filtered.Annotations = append(filtered.Annotations, annotation)
		}
	}
	segments := make([]htmlSourceSegment, 0)
	for _, interval := range sourceIntervals(filtered) {
		annotations := interval.anns
		var tooltipParts []string
		seenTooltips := make(map[string]struct{})
		var metrics []string
		seenMetrics := make(map[string]struct{})
		for _, annotation := range annotations {
			if annotation.Tooltip != "" {
				if _, seen := seenTooltips[annotation.Tooltip]; !seen {
					tooltipParts = append(tooltipParts, annotation.Tooltip)
					seenTooltips[annotation.Tooltip] = struct{}{}
				}
			}
			if _, seen := seenMetrics[annotation.Metric]; !seen {
				seenMetrics[annotation.Metric] = struct{}{}
				metrics = append(metrics, annotation.Metric)
			}
		}
		var classes []string
		for _, mode := range htmlSourceModes() {
			if metric != "" && mode != metric && mode != "combined" {
				continue
			}
			if severity := htmlSourceSeverity(annotations, mode); severity != "none" {
				classes = append(classes, "view-"+mode+"-"+severity)
			}
		}
		segments = append(segments, htmlSourceSegment{
			Text: view.Source[interval.start:interval.end], Class: strings.Join(classes, " "),
			Tooltip: strings.Join(tooltipParts, "\n"), Metrics: strings.Join(metrics, " "),
			Start: interval.start, End: interval.end,
		})
	}
	return segments
}

// htmlSourceSeverity selects the highest severity within the chosen metric.
func htmlSourceSeverity(annotations []*SourceAnnotation, mode string) string {
	severity := 0
	for _, annotation := range annotations {
		filter := mode
		if mode == "combined" {
			filter = ""
		}
		if !sourceMetricMatches(annotation.Metric, filter) {
			continue
		}
		state := htmlStateSeverity(annotation.State)
		severity = max(severity, state)
	}
	return []string{"none", "covered", "partial", "uncovered", "attention"}[severity]
}

func sourceMetricMatches(annotationMetric, viewMetric string) bool {
	switch viewMetric {
	case "":
		return true
	case "mcdc":
		return annotationMetric == "mcdc-unique" || annotationMetric == "mcdc-masking"
	case "clause":
		return annotationMetric == "clause-body" || annotationMetric == "clause-selection"
	default:
		return annotationMetric == viewMetric
	}
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

func htmlTriageMetrics(s analysis.Summary) []htmlMetric {
	return []htmlMetric{
		{"Statement", "Stmt", s.Statement}, {"Function", "Func", s.Function},
		{"Decision", "Decision", s.Decision}, {"Condition", "Condition", s.Condition},
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
func htmlLocation(l cover.SourceLocation) string {
	return fmt.Sprintf("%s:%d:%d", l.File, l.Start.Line, l.Start.Column)
}

//go:embed template/report.html
var htmlDocument string

func htmlStateSeverity(state string) int {
	switch state {
	case "covered", "both":
		return 1
	case "partial", "true-only", "false-only":
		return 2
	case "not-covered", "uncovered", "not-evaluated":
		return 3
	case "unknown", "unsupported", "unsupported-by-backend", "infeasible", "analysis-incomplete":
		return 4
	default:
		return 0
	}
}
