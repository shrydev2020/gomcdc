package report

import (
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
	segments := htmlSourceSegments(&view)
	if len(segments) != 2 || segments[0].Text != "<x>" || segments[0].Class == "" {
		t.Fatalf("segments = %#v", segments)
	}
}
