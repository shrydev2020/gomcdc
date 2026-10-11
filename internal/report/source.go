package report

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shrydev2020/gomcdc/v2/internal/analysis"
	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
)

type sourceMapping struct {
	start, end int
	status     string
	reason     string
}

type sourceInterval struct {
	start, end int
	// Annotations are immutable; intervals retain references instead of copying
	// every string-bearing annotation for every overlap.
	anns []*SourceAnnotation
}

type sourceBoundaryEvent struct {
	starts []int
	ends   []int
}

// HTML views belong to rendering and cannot be persisted as analysis evidence.
type htmlReport struct {
	analysis.Result

	Packages []htmlPackage
}
type htmlPackage struct {
	analysis.PackageReport

	Files []htmlFile
}
type htmlFile struct {
	analysis.FileReport

	Source *SourceFileView
}

func sourceView(value analysis.Result, sourceFiles []analysis.SourceFileInput) htmlReport {
	sources := make(map[string]analysis.SourceFileInput, len(sourceFiles))
	for _, source := range sourceFiles {
		sources[source.PackagePath+"\x00"+source.Path] = source
	}
	view := htmlReport{Result: value, Packages: make([]htmlPackage, len(value.Packages))}
	for pi, pkg := range value.Packages {
		p := htmlPackage{PackageReport: pkg, Files: make([]htmlFile, len(pkg.Files))}
		for fi, file := range pkg.Files {
			f := htmlFile{FileReport: file}
			if source, ok := sources[pkg.Path+"\x00"+file.Path]; ok {
				annotations := sourceAnnotations(file, source.Source)
				f.Source = &SourceFileView{Path: file.Path, Source: string(source.Source), Annotations: normalizeAnnotations(annotations, len(source.Source)), Diagnostics: sourceMappingDiagnostics(annotations)}
			}
			p.Files[fi] = f
		}
		view.Packages[pi] = p
	}
	return view
}

func sourceAnnotations(file analysis.FileReport, source []byte) []SourceAnnotation {
	var annotations []SourceAnnotation
	for _, function := range file.Functions {
		for _, statement := range function.Statements {
			mapping := sourceRangeOffsets(statement.Location, source)
			state := "uncovered"
			switch {
			case statement.Metric.Unknown > 0:
				state = "unknown"
			case statement.Metric.Unsupported > 0:
				state = "unsupported"
			case statement.Covered:
				state = "covered"
			}
			annotations = append(annotations, applySourceMapping(mapping, SourceAnnotation{
				Metric: "statement", EntityID: sourceEntityID("statement", statement.Location, mapping), State: state,
				Tooltip: "Statement: " + state,
			}))
		}
		for _, decision := range function.Decisions {
			mapping := sourceRangeOffsets(decision.Location, source)
			state := decisionState(decision)
			annotations = append(annotations, applySourceMapping(mapping, SourceAnnotation{
				Metric:   "decision",
				EntityID: decision.DecisionID, State: state,
				Tooltip: decisionTooltip(decision),
			}))
			for _, condition := range decision.Conditions {
				mapping := sourceRangeOffsets(condition.Location, source)
				conditionState := conditionCoverageState(condition)
				conditionID := condition.ConditionID
				annotations = append(annotations,
					applySourceMapping(mapping, SourceAnnotation{Metric: "condition", EntityID: conditionID, State: conditionState, Tooltip: conditionTooltip(condition)}),
					applySourceMapping(mapping, SourceAnnotation{Metric: "mcdc-unique", EntityID: conditionID + ":mcdc-unique", State: condition.MCDCUnique.Status, Tooltip: conditionTooltip(condition)}),
					applySourceMapping(mapping, SourceAnnotation{Metric: "mcdc-masking", EntityID: conditionID + ":mcdc-masking", State: condition.MCDCMasking.Status, Tooltip: conditionTooltip(condition)}),
				)
			}
		}
		for _, clause := range function.Clauses {
			mapping := sourceRangeOffsets(clause.Location, source)
			bodyState := metricState(clause.BodyCoverage)
			annotations = append(annotations, applySourceMapping(mapping, SourceAnnotation{
				Metric:   "clause-body",
				EntityID: clause.ClauseID, State: bodyState,
				Tooltip: "Clause body: " + bodyState,
			}))
			if clause.SelectionCoverage.Enabled {
				selectionState := metricState(clause.SelectionCoverage)
				annotations = append(annotations, applySourceMapping(mapping, SourceAnnotation{
					Metric:   "clause-selection",
					EntityID: clause.ClauseID, State: selectionState,
					Tooltip: "Clause selection: " + selectionState,
				}))
			}
		}
		for _, noMatch := range function.NoMatches {
			mapping := sourceRangeOffsets(noMatch.Location, source)
			state := metricState(noMatch.SelectionCoverage)
			annotations = append(annotations, applySourceMapping(mapping, SourceAnnotation{
				Metric:   "clause-selection",
				EntityID: noMatch.SwitchID, State: state,
				Tooltip: "No-match selection: " + state,
			}))
		}
	}
	return annotations
}

func applySourceMapping(mapping sourceMapping, annotation SourceAnnotation) SourceAnnotation {
	annotation.StartOffset = mapping.start
	annotation.EndOffset = mapping.end
	annotation.MappingStatus = mapping.status
	annotation.MappingReason = mapping.reason
	return annotation
}

func sourceEntityID(prefix string, location cover.SourceLocation, mapping sourceMapping) string {
	if mapping.status == "mapped" {
		return fmt.Sprintf("%s:%d:%d", prefix, mapping.start, mapping.end)
	}
	return fmt.Sprintf("%s:%s:%d:%d:%d:%d", prefix, location.File, location.Start.Line, location.Start.Column, location.End.Line, location.End.Column)
}

func sourceRangeOffsets(location cover.SourceLocation, source []byte) sourceMapping {
	start, end := location.StartOffset, location.EndOffset
	if start >= 0 && end > start && end <= len(source) {
		return sourceMapping{start: start, end: end, status: "mapped"}
	}
	fallbackStart, startOK := lineColumnOffset(source, location.Start.Line, location.Start.Column)
	fallbackEnd, endOK := lineColumnOffset(source, location.End.Line, location.End.Column)
	if startOK && endOK && fallbackStart >= 0 && fallbackEnd > fallbackStart && fallbackEnd <= len(source) {
		return sourceMapping{start: fallbackStart, end: fallbackEnd, status: "mapped"}
	}
	return sourceMapping{
		status: "unknown",
		reason: fmt.Sprintf("source location %s:%d:%d-%d:%d cannot be mapped to %d source bytes", location.File, location.Start.Line, location.Start.Column, location.End.Line, location.End.Column, len(source)),
	}
}

func sourceMappingDiagnostics(annotations []SourceAnnotation) []SourceDiagnostic {
	var diagnostics []SourceDiagnostic
	for _, annotation := range annotations {
		if annotation.MappingStatus != "unknown" {
			continue
		}
		diagnostics = append(diagnostics, SourceDiagnostic{
			Metric: annotation.Metric, EntityID: annotation.EntityID, Reason: annotation.MappingReason,
		})
	}
	return diagnostics
}

func decisionTooltip(decision analysis.DecisionReport) string {
	return fmt.Sprintf("Decision %s: %s\ntrue: %s\nfalse: %s\nnot evaluated: %d",
		decision.DecisionID, decision.Expression, outcomeLabel(decision.DecisionCoverage.True),
		outcomeLabel(decision.DecisionCoverage.False), decision.NotEvaluated)
}

func conditionTooltip(condition analysis.ConditionReport) string {
	return fmt.Sprintf("Condition #%d: %s\ntrue: %s\nfalse: %s\nnot evaluated: %d\nUnique-Cause MC/DC: %s\nMasking MC/DC: %s",
		condition.Index, condition.Expression, outcomeLabel(condition.True), outcomeLabel(condition.False),
		condition.NotEvaluated, condition.MCDCUnique.Status, condition.MCDCMasking.Status)
}

func outcomeLabel(covered bool) string {
	if covered {
		return "covered"
	}
	return "not covered"
}

func lineColumnOffset(source []byte, line, column int) (int, bool) {
	if line <= 0 || column <= 0 {
		return 0, false
	}
	currentLine, offset := 1, 0
	for offset < len(source) && currentLine < line {
		if source[offset] == '\n' {
			currentLine++
		}
		offset++
	}
	if currentLine != line {
		return len(source), false
	}
	if column <= 1 {
		return offset, true
	}
	target := offset + column - 1
	if target > len(source) {
		return len(source), false
	}
	return target, true
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
	events := make(map[int]sourceBoundaryEvent, len(view.Annotations)*2)
	for index, annotation := range view.Annotations {
		if annotation.StartOffset < 0 || annotation.EndOffset <= annotation.StartOffset ||
			annotation.EndOffset > len(view.Source) {
			continue
		}
		boundaries = append(boundaries, annotation.StartOffset, annotation.EndOffset)
		startEvent := events[annotation.StartOffset]
		startEvent.starts = append(startEvent.starts, index)
		events[annotation.StartOffset] = startEvent
		endEvent := events[annotation.EndOffset]
		endEvent.ends = append(endEvent.ends, index)
		events[annotation.EndOffset] = endEvent
	}
	sort.Ints(boundaries)
	unique := boundaries[:0]
	for _, boundary := range boundaries {
		if len(unique) == 0 || unique[len(unique)-1] != boundary {
			unique = append(unique, boundary)
		}
	}
	intervals := make([]sourceInterval, 0, len(unique))
	active := make([]int, 0, len(view.Annotations))
	positions := make([]int, len(view.Annotations))
	for index := range positions {
		positions[index] = -1
	}
	for index := 0; index+1 < len(unique); index++ {
		start, end := unique[index], unique[index+1]
		if start == end {
			continue
		}
		event := events[start]
		for _, annotationIndex := range event.ends {
			position := positions[annotationIndex]
			if position < 0 {
				continue
			}
			last := active[len(active)-1]
			active[position] = last
			positions[last] = position
			active = active[:len(active)-1]
			positions[annotationIndex] = -1
		}
		for _, annotationIndex := range event.starts {
			positions[annotationIndex] = len(active)
			active = append(active, annotationIndex)
		}
		activeAnnotations := make([]*SourceAnnotation, 0, len(active))
		for _, annotationIndex := range active {
			activeAnnotations = append(activeAnnotations, &view.Annotations[annotationIndex])
		}
		intervals = append(intervals, sourceInterval{start: start, end: end, anns: activeAnnotations})
	}
	return intervals
}

func metricState(metric analysis.MetricSummary) string {
	switch {
	case metric.AnalysisIncomplete > 0:
		return "analysis-incomplete"
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

func decisionState(decision analysis.DecisionReport) string {
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

func conditionCoverageState(condition analysis.ConditionReport) string {
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

func sourceClass(annotations []*SourceAnnotation) string {
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
