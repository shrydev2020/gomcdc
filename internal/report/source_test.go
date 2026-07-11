package report

import (
	"strings"
	"testing"

	cover "github.com/shrydev2020/gomcdc/internal/coverage"
)

func TestSourceIntervalsSplitOverlappingRanges(t *testing.T) {
	view := SourceFileView{
		Source: "abcdef",
		Annotations: []SourceAnnotation{
			{StartOffset: 0, EndOffset: 4, Metric: "decision", State: "covered"},
			{StartOffset: 2, EndOffset: 6, Metric: "condition", State: "both"},
		},
	}
	intervals := sourceIntervals(view)
	if len(intervals) != 3 {
		t.Fatalf("interval count = %d, want 3", len(intervals))
	}
	want := [][2]int{{0, 2}, {2, 4}, {4, 6}}
	for index, interval := range intervals {
		if got := [2]int{interval.start, interval.end}; got != want[index] {
			t.Fatalf("interval[%d] = %v, want %v", index, got, want[index])
		}
	}
	if len(intervals[1].anns) != 2 {
		t.Fatalf("overlap annotations = %d, want 2", len(intervals[1].anns))
	}
}

func TestSourceRangeOffsetsUseByteColumns(t *testing.T) {
	source := []byte("あa\n")
	location := cover.SourceLocation{
		Start: cover.Position{Line: 1, Column: 1},
		End:   cover.Position{Line: 1, Column: 5},
	}
	start, end := sourceRangeOffsets(location, source)
	if start != 0 || end != 4 {
		t.Fatalf("range = %d:%d, want 0:4", start, end)
	}
}

func TestHTMLSourceSegmentsEscapeTextThroughTemplate(t *testing.T) {
	view := SourceFileView{
		Source: "<x>\n",
		Annotations: []SourceAnnotation{
			{StartOffset: 0, EndOffset: 3, Metric: "statement", State: "covered", Tooltip: "safe"},
		},
	}
	segments := htmlSourceSegments(&view, "")
	if len(segments) != 2 || segments[0].Text != "<x>" || segments[0].Class == "" {
		t.Fatalf("segments = %#v", segments)
	}
}

func TestHTMLSourceSegmentsFilterMetricProjection(t *testing.T) {
	view := SourceFileView{
		Source: "abcdef",
		Annotations: []SourceAnnotation{
			{StartOffset: 0, EndOffset: 2, Metric: "statement", State: "covered"},
			{StartOffset: 0, EndOffset: 2, Metric: "decision", State: "not-covered"},
			{StartOffset: 1, EndOffset: 3, Metric: "condition", State: "true-only"},
		},
	}
	segments := htmlSourceSegments(&view, "statement")
	for _, segment := range segments {
		if strings.Contains(segment.Class, "decision") || strings.Contains(segment.Class, "condition") {
			t.Fatalf("statement projection leaked another metric: %#v", segments)
		}
	}
	if len(htmlSourceSegments(&view, "decision")) == 0 || len(htmlSourceSegments(&view, "condition")) == 0 {
		t.Fatal("metric projections lost their own annotations")
	}
}

func TestSourceAnnotationsUseConditionOccurrenceIDs(t *testing.T) {
	location := cover.SourceLocation{File: "p.go", StartOffset: 0, EndOffset: 1}
	file := FileReport{Functions: []FunctionReport{{Decisions: []DecisionReport{{
		DecisionID: "decision-1",
		Conditions: []ConditionReport{
			{Index: 0, Location: location},
			{Index: 1, Location: location},
		},
	}}}}}
	annotations := sourceAnnotations(file, []byte("ab"))
	ids := make(map[string]bool)
	for _, annotation := range annotations {
		if annotation.Metric != "condition" && annotation.Metric != "mcdc-unique" && annotation.Metric != "mcdc-masking" {
			continue
		}
		if ids[annotation.EntityID] {
			t.Fatalf("duplicate condition annotation ID %q", annotation.EntityID)
		}
		ids[annotation.EntityID] = true
	}
	for _, want := range []string{
		"decision-1:condition:0",
		"decision-1:condition:0:mcdc-unique",
		"decision-1:condition:0:mcdc-masking",
		"decision-1:condition:1",
		"decision-1:condition:1:mcdc-unique",
		"decision-1:condition:1:mcdc-masking",
	} {
		if !ids[want] {
			t.Errorf("missing annotation ID %q", want)
		}
	}
}
