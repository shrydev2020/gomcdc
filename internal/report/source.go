package report

import (
	"fmt"
	"sort"
	"strings"

	cover "github.com/shrydev2020/gomcdc/internal/coverage"
)

// SourceFileInput is immutable original source supplied by the analyzer.
type SourceFileInput struct {
	PackagePath string
	Path        string
	Source      []byte
}

// SourceFileView is the source-centered representation used by HTML reports.
type SourceFileView struct {
	Path        string             `json:"path"`
	Source      string             `json:"source"`
	Annotations []SourceAnnotation `json:"annotations"`
}

// SourceAnnotation identifies one metric over an original source byte range.
type SourceAnnotation struct {
	StartOffset int    `json:"startOffset"`
	EndOffset   int    `json:"endOffset"`
	Metric      string `json:"metric"`
	EntityID    string `json:"entityId"`
	State       string `json:"state"`
	Tooltip     string `json:"tooltip"`
}

type sourceInterval struct {
	start, end int
	anns       []SourceAnnotation
}

func attachSourceViews(report *Report, input Input) {
	sources := make(map[string]SourceFileInput, len(input.SourceFiles))
	for _, source := range input.SourceFiles {
		sources[source.PackagePath+"\x00"+source.Path] = source
	}
	for packageIndex := range report.Packages {
		pkg := &report.Packages[packageIndex]
		for fileIndex := range pkg.Files {
			file := &pkg.Files[fileIndex]
			source, ok := sources[pkg.Path+"\x00"+file.Path]
			if !ok {
				continue
			}
			view := SourceFileView{Path: file.Path, Source: string(source.Source)}
			view.Annotations = sourceAnnotations(*file, source.Source)
			file.Source = &view
		}
	}
}

func sourceAnnotations(file FileReport, source []byte) []SourceAnnotation {
	var annotations []SourceAnnotation
	for _, function := range file.Functions {
		for _, statement := range function.Statements {
			start, end := sourceRangeOffsets(statement.Location, source)
			state := "uncovered"
			switch {
			case statement.Metric.Unknown > 0:
				state = "unknown"
			case statement.Metric.Unsupported > 0:
				state = "unsupported"
			case statement.Covered:
				state = "covered"
			}
			annotations = append(annotations, SourceAnnotation{
				StartOffset: start, EndOffset: end, Metric: "statement",
				EntityID: fmt.Sprintf("statement:%d:%d", start, end), State: state,
				Tooltip: "Statement: " + state,
			})
		}
		for _, decision := range function.Decisions {
			start, end := sourceRangeOffsets(decision.Location, source)
			state := decisionState(decision)
			annotations = append(annotations, SourceAnnotation{
				StartOffset: start, EndOffset: end, Metric: "decision",
				EntityID: decision.DecisionID, State: state,
				Tooltip: "Decision: " + state,
			})
			for _, condition := range decision.Conditions {
				cstart, cend := sourceRangeOffsets(condition.Location, source)
				conditionState := conditionCoverageState(condition)
				annotations = append(annotations,
					SourceAnnotation{StartOffset: cstart, EndOffset: cend, Metric: "condition", EntityID: decision.DecisionID, State: conditionState, Tooltip: "Condition: " + conditionState},
					SourceAnnotation{StartOffset: cstart, EndOffset: cend, Metric: "mcdc-unique", EntityID: decision.DecisionID, State: condition.MCDCUnique.Status, Tooltip: "Unique-Cause MC/DC: " + condition.MCDCUnique.Status},
					SourceAnnotation{StartOffset: cstart, EndOffset: cend, Metric: "mcdc-masking", EntityID: decision.DecisionID, State: condition.MCDCMasking.Status, Tooltip: "Masking MC/DC: " + condition.MCDCMasking.Status},
				)
			}
		}
		for _, clause := range function.Clauses {
			start, end := sourceRangeOffsets(clause.Location, source)
			bodyState := metricState(clause.BodyCoverage)
			annotations = append(annotations, SourceAnnotation{
				StartOffset: start, EndOffset: end, Metric: "clause-body",
				EntityID: clause.ClauseID, State: bodyState,
				Tooltip: "Clause body: " + bodyState,
			})
			if clause.SelectionCoverage.Enabled {
				selectionState := metricState(clause.SelectionCoverage)
				annotations = append(annotations, SourceAnnotation{
					StartOffset: start, EndOffset: end, Metric: "clause-selection",
					EntityID: clause.ClauseID, State: selectionState,
					Tooltip: "Clause selection: " + selectionState,
				})
			}
		}
	}
	return normalizeAnnotations(annotations, len(source))
}

func sourceRangeOffsets(location cover.SourceLocation, source []byte) (int, int) {
	start, end := location.StartOffset, location.EndOffset
	if start < 0 || end <= start || end > len(source) {
		start = lineColumnOffset(source, location.Start.Line, location.Start.Column)
		end = lineColumnOffset(source, location.End.Line, location.End.Column)
	}
	if start < 0 {
		start = 0
	}
	if end > len(source) {
		end = len(source)
	}
	if end < start {
		end = start
	}
	return start, end
}

func lineColumnOffset(source []byte, line, column int) int {
	if line <= 0 {
		return 0
	}
	currentLine, offset := 1, 0
	for offset < len(source) && currentLine < line {
		if source[offset] == '\n' {
			currentLine++
		}
		offset++
	}
	if currentLine != line {
		return len(source)
	}
	if column <= 1 {
		return offset
	}
	target := offset + column - 1
	if target > len(source) {
		return len(source)
	}
	return target
}

func normalizeAnnotations(annotations []SourceAnnotation, sourceLength int) []SourceAnnotation {
	filtered := annotations[:0]
	for _, annotation := range annotations {
		if annotation.StartOffset < 0 || annotation.EndOffset <= annotation.StartOffset {
			continue
		}
		if annotation.StartOffset > sourceLength {
			continue
		}
		if annotation.EndOffset > sourceLength {
			annotation.EndOffset = sourceLength
		}
		filtered = append(filtered, annotation)
	}
	return filtered
}

func sourceIntervals(view SourceFileView) []sourceInterval {
	boundaries := []int{0, len(view.Source)}
	for _, annotation := range view.Annotations {
		boundaries = append(boundaries, annotation.StartOffset, annotation.EndOffset)
	}
	sort.Ints(boundaries)
	unique := boundaries[:0]
	for _, boundary := range boundaries {
		if len(unique) == 0 || unique[len(unique)-1] != boundary {
			unique = append(unique, boundary)
		}
	}
	intervals := make([]sourceInterval, 0, len(unique))
	for index := 0; index+1 < len(unique); index++ {
		start, end := unique[index], unique[index+1]
		if start == end {
			continue
		}
		var active []SourceAnnotation
		for _, annotation := range view.Annotations {
			if annotation.StartOffset <= start && end <= annotation.EndOffset {
				active = append(active, annotation)
			}
		}
		intervals = append(intervals, sourceInterval{start: start, end: end, anns: active})
	}
	return intervals
}

func metricState(metric MetricSummary) string {
	switch {
	case metric.Unknown > 0:
		return "unknown"
	case metric.Unsupported > 0:
		return "unsupported"
	case metric.Total == 0:
		return "not-covered"
	case metric.Covered == metric.Total:
		return "covered"
	case metric.Covered == 0:
		return "not-covered"
	default:
		return "partial"
	}
}

func decisionState(decision DecisionReport) string {
	if decision.Summary.Decision.Unknown > 0 {
		return "unknown"
	}
	if decision.Summary.Decision.Unsupported > 0 {
		return "unsupported"
	}
	if decision.DecisionCoverage.True && decision.DecisionCoverage.False {
		return "covered"
	}
	if decision.DecisionCoverage.True {
		return "true-only"
	}
	if decision.DecisionCoverage.False {
		return "false-only"
	}
	if decision.NotEvaluated > 0 {
		return "not-evaluated"
	}
	return "not-covered"
}

func conditionCoverageState(condition ConditionReport) string {
	switch {
	case condition.Metric.Unknown > 0:
		return "unknown"
	case condition.Metric.Unsupported > 0:
		return "unsupported"
	case condition.True && condition.False:
		return "both"
	case condition.True:
		return "true-only"
	case condition.False:
		return "false-only"
	case condition.NotEvaluated > 0:
		return "not-evaluated"
	default:
		return "not-covered"
	}
}

func sourceClass(annotations []SourceAnnotation) string {
	classes := make([]string, 0, len(annotations))
	seen := make(map[string]struct{}, len(annotations))
	for _, annotation := range annotations {
		metric := strings.ReplaceAll(annotation.Metric, "_", "-")
		class := "ann-" + metric + "-" + strings.ReplaceAll(annotation.State, " ", "-")
		if _, ok := seen[class]; ok {
			continue
		}
		seen[class] = struct{}{}
		classes = append(classes, class)
	}
	sort.Strings(classes)
	return strings.Join(classes, " ")
}
