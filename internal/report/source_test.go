package report

import (
	"reflect"
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
	if again := sourceIntervals(view); !reflect.DeepEqual(intervals, again) {
		t.Fatal("source interval ordering is not deterministic")
	}
}

func TestSourceRangeOffsetsUseByteColumns(t *testing.T) {
	source := []byte("あa\n")
	location := cover.SourceLocation{
		Start: cover.Position{Line: 1, Column: 1},
		End:   cover.Position{Line: 1, Column: 5},
	}
	mapping := sourceRangeOffsets(location, source)
	if mapping.start != 0 || mapping.end != 4 || mapping.status != "mapped" {
		t.Fatalf("mapping = %#v, want 0:4 mapped", mapping)
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
			{ConditionID: "0x0000000000000001", Index: 0, Expression: "a", Location: location, True: true, NotEvaluated: 2, MCDCUnique: MCDCConditionReport{Status: "not-covered"}, MCDCMasking: MCDCConditionReport{Status: "covered"}},
			{ConditionID: "0x0000000000000002", Index: 1, Expression: "b", Location: location},
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
		"0x0000000000000001",
		"0x0000000000000001:mcdc-unique",
		"0x0000000000000001:mcdc-masking",
		"0x0000000000000002",
		"0x0000000000000002:mcdc-unique",
		"0x0000000000000002:mcdc-masking",
	} {
		if !ids[want] {
			t.Errorf("missing annotation ID %q", want)
		}
	}
	for _, annotation := range annotations {
		if annotation.Metric == "condition" && annotation.EntityID == "0x0000000000000001" {
			for _, required := range []string{"Condition #0: a", "true: covered", "false: not covered", "not evaluated: 2", "Unique-Cause MC/DC: not-covered", "Masking MC/DC: covered"} {
				if !strings.Contains(annotation.Tooltip, required) {
					t.Errorf("condition tooltip missing %q: %q", required, annotation.Tooltip)
				}
			}
		}
	}
}

func TestSourceMappingFailureProducesDiagnostic(t *testing.T) {
	location := cover.SourceLocation{File: "p.go", Start: cover.Position{Line: 9, Column: 1}, End: cover.Position{Line: 9, Column: 2}}
	file := FileReport{Functions: []FunctionReport{{Decisions: []DecisionReport{{
		DecisionID: "decision-1", Location: location,
	}}}}}
	annotations := sourceAnnotations(file, []byte("a\n"))
	diagnostics := sourceMappingDiagnostics(annotations)
	if len(diagnostics) != 1 || diagnostics[0].Metric != "decision" || diagnostics[0].EntityID != "decision-1" {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	if diagnostics[0].Reason == "" {
		t.Fatal("mapping diagnostic has no reason")
	}
	if got := normalizeAnnotations(annotations, 2); len(got) != 0 {
		t.Fatalf("unmapped annotations survived normalization: %#v", got)
	}
}

func BenchmarkSourceIntervalsSweepLine(b *testing.B) {
	view := SourceFileView{Source: strings.Repeat("x", 100_000), Annotations: make([]SourceAnnotation, 2_000)}
	for index := range view.Annotations {
		start := index * 40
		view.Annotations[index] = SourceAnnotation{StartOffset: start, EndOffset: start + 2_000, Metric: "condition", State: "both"}
	}
	b.ResetTimer()
	for range b.N {
		_ = sourceIntervals(view)
	}
}
