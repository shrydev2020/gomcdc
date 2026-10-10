package c0

import "testing"

func TestCoverLayoutRecognizesObservedCounterMetadata(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		positions []uint64
		counts    []uint64
		want      CoverLayout
	}{
		{
			name:      "logical basic block",
			positions: []uint64{100, 104, 0},
			counts:    []uint64{2},
		},
		{
			name:      "split ranges with block counts",
			positions: []uint64{5, 6, 65538, 7, 8, 65538},
			counts:    []uint64{2, 2},
			want:      CoverLayout{PhysicalPositions: true, SplitCodeRanges: true},
		},
		{
			name:      "split ranges with range counts",
			positions: []uint64{5, 6, 65538, 7, 8, 65538},
			counts:    []uint64{1, 1},
			want:      CoverLayout{PhysicalPositions: true, SplitCodeRanges: true, RangeStatementCounts: true},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := coverLayoutForMetadata(test.positions, test.counts)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("layout = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestCoverLayoutRejectsUnrecognizedCounterMetadata(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		positions []uint64
		counts    []uint64
	}{
		{"different counts", []uint64{5, 6, 65538, 7, 8, 65538}, []uint64{1, 2}},
		{"different coordinates", []uint64{5, 6, 65538, 8, 9, 65538}, []uint64{1, 1}},
		{"missing coordinate", []uint64{5, 6, 65538, 7, 8}, []uint64{1, 1}},
		{"empty metadata", nil, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if layout, err := coverLayoutForMetadata(test.positions, test.counts); err == nil {
				t.Fatalf("accepted unrecognized metadata as %+v", layout)
			}
		})
	}
}
