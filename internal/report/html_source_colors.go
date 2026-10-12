package report

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

type htmlColorEvent struct{ offset, mode, severity, delta int }

func htmlAnnotationMode(metric string) int {
	switch metric {
	case "statement":
		return 0
	case "decision":
		return 1
	case "condition":
		return 2
	case "mcdc-unique", "mcdc-masking":
		return 3
	case "clause-body", "clause-selection":
		return 4
	default:
		return -1
	}
}

// walkHTMLColors selects interval colors using active counts per metric and state.
func walkHTMLColors(length int, annotations []SourceAnnotation, yield func(start, end int, classes string) bool) error {
	events := make([]htmlColorEvent, 0, len(annotations)*2+1)
	for _, a := range annotations {
		if a.MappingStatus == "unknown" || a.State == "disabled" || a.State == "" {
			continue
		}
		mode, severity := htmlAnnotationMode(a.Metric), htmlStateSeverity(a.State)
		if mode < 0 {
			return fmt.Errorf("annotation %q has unknown metric %q", a.EntityID, a.Metric)
		}
		if severity == 0 {
			return fmt.Errorf("annotation %q has unknown state %q", a.EntityID, a.State)
		}
		if a.StartOffset < 0 || a.EndOffset <= a.StartOffset || a.EndOffset > length {
			return fmt.Errorf("annotation %q range [%d,%d) exceeds source length %d", a.EntityID, a.StartOffset, a.EndOffset, length)
		}
		events = append(events, htmlColorEvent{a.StartOffset, mode, severity, 1}, htmlColorEvent{a.EndOffset, mode, severity, -1})
	}
	events = append(events, htmlColorEvent{offset: length})
	slices.SortFunc(events, func(a, b htmlColorEvent) int { return cmp.Compare(a.offset, b.offset) })
	var active [6][5]int
	names := htmlSourceModes()
	states := []string{"none", "covered", "partial", "uncovered", "attention"}
	start := 0
	for i := 0; i < len(events); {
		end := events[i].offset
		if end > start {
			var classes []string
			for mode := range active {
				for severity := 4; severity > 0; severity-- {
					if active[mode][severity] > 0 {
						classes = append(classes, "view-"+names[mode]+"-"+states[severity])
						break
					}
				}
			}
			if !yield(start, end, strings.Join(classes, " ")) {
				return nil
			}
			start = end
		}
		for i < len(events) && events[i].offset == end {
			e := events[i]
			if e.delta != 0 {
				active[e.mode][e.severity] += e.delta
				active[5][e.severity] += e.delta
			}
			i++
		}
	}
	return nil
}

func htmlSourceModes() []string {
	return []string{"statement", "decision", "condition", "mcdc", "clause", "combined"}
}

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
