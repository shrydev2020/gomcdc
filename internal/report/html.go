package report

import (
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"strings"

	cover "github.com/shrydev2020/gomcdc/internal/coverage"
)

type htmlMetric struct {
	Name, Short string
	Summary     MetricSummary
}

// WriteHTML writes one self-contained static HTML report.
func WriteHTML(w io.Writer, input Input) error {
	report := Build(input)
	attachSourceViews(&report, input)
	return writeHTMLReport(w, report)
}

func writeHTMLReport(w io.Writer, value Report) error {
	t, err := template.New("report").Funcs(template.FuncMap{
		"metrics": htmlMetrics, "pct": htmlPercentage, "class": htmlMetricClass,
		"loc": htmlLocation, "complete": completeness, "sourceSegments": htmlSourceSegments,
	}).Parse(htmlDocument)
	if err != nil {
		return fmt.Errorf("parse HTML report template: %w", err)
	}
	if err := t.Execute(w, value); err != nil {
		return fmt.Errorf("render HTML report: %w", err)
	}
	return nil
}

type htmlSourceSegment struct {
	Text    string
	Class   string
	Tooltip string
	Metrics string
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
		var metrics []string
		seenMetrics := make(map[string]struct{})
		for _, annotation := range annotations {
			if annotation.Tooltip != "" {
				tooltipParts = append(tooltipParts, annotation.Tooltip)
			}
			if _, seen := seenMetrics[annotation.Metric]; !seen {
				seenMetrics[annotation.Metric] = struct{}{}
				metrics = append(metrics, annotation.Metric)
			}
		}
		class := sourceClass(annotations)
		for _, metric := range metrics {
			class += " metric-" + strings.ReplaceAll(metric, "_", "-")
		}
		segments = append(segments, htmlSourceSegment{
			Text: view.Source[interval.start:interval.end], Class: class,
			Tooltip: strings.Join(tooltipParts, " | "), Metrics: strings.Join(metrics, " "),
		})
	}
	return segments
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

func htmlMetrics(s Summary) []htmlMetric {
	return []htmlMetric{
		{"Statement", "Stmt", s.Statement}, {"Function", "Func", s.Function}, {"Decision", "Decision", s.Decision},
		{"Switch clause body", "Sw body", s.SwitchClauseBody}, {"Type switch clause body", "Type body", s.TypeSwitchClauseBody},
		{"Select clause body", "Select body", s.SelectClauseBody}, {"Switch clause selection", "Sw select", s.SwitchClauseSelection},
		{"Type switch clause selection", "Type select", s.TypeSwitchClauseSelection}, {"Condition", "Condition", s.Condition},
		{"Unique-Cause MC/DC", "UC MC/DC", s.MCDCUnique}, {"Masking MC/DC", "Mask MC/DC", s.MCDCMasking},
	}
}
func htmlPercentage(m MetricSummary) string {
	if m.Percentage == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.2f%%", *m.Percentage)
}
func htmlMetricClass(m MetricSummary) string {
	switch {
	case !m.Enabled:
		return "disabled"
	case m.Unknown > 0 || m.Unsupported > 0:
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
