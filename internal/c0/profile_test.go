package c0_test

import (
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/internal/c0"
)

func TestParseProfileModesMergeDuplicateBlocks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mode      c0.Mode
		wantCount uint64
	}{
		{name: "set uses OR", mode: c0.ModeSet, wantCount: 2},
		{name: "count uses addition", mode: c0.ModeCount, wantCount: 4},
		{name: "atomic uses addition", mode: c0.ModeAtomic, wantCount: 4},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := "mode: " + string(test.mode) + "\n" +
				"example.com/p/p.go:2.1,2.8 1 2\n" +
				"example.com/p/p.go:2.1,2.8 1 2\n"
			profile, err := c0.ParseProfile(strings.NewReader(input))
			if err != nil {
				t.Fatalf("ParseProfile: %v", err)
			}
			if profile.Mode != test.mode {
				t.Fatalf("mode = %q, want %q", profile.Mode, test.mode)
			}
			if got := profile.Files[0].Blocks[0].Count; got != test.wantCount {
				t.Fatalf("merged count = %d, want %d", got, test.wantCount)
			}
		})
	}
}

func TestParseProfileSortsFilesAndBlocksAndAllowsPathPunctuation(t *testing.T) {
	t.Parallel()

	input := "mode: count\r\n" +
		"z dir/file.go:9.2,9.8 1 1\r\n" +
		"C:\\tmp dir\\a.go:3.0,3.4 1 0\r\n" +
		"z dir/file.go:2.1,2.8 1 2\r\n"
	profile, err := c0.ParseProfile(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseProfile: %v", err)
	}
	if got, want := []string{profile.Files[0].Path, profile.Files[1].Path}, []string{"C:\\tmp dir\\a.go", "z dir/file.go"}; !equalStrings(got, want) {
		t.Fatalf("file order = %v, want %v", got, want)
	}
	blocks := profile.Files[1].Blocks
	if got, want := []int{blocks[0].Position.Start.Line, blocks[1].Position.Start.Line}, []int{2, 9}; !equalInts(got, want) {
		t.Fatalf("block order = %v, want %v", got, want)
	}
}

func TestParseProfileRejectsMalformedInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		wantInError string
	}{
		{name: "empty", input: "", wantInError: "missing mode line"},
		{name: "bad mode line", input: "mode:\n", wantInError: "bad mode line"},
		{name: "unsupported mode", input: "mode: histogram\n", wantInError: "unsupported mode"},
		{name: "empty data line", input: "mode: set\n\n", wantInError: "empty profile line"},
		{name: "missing fields", input: "mode: set\na.go:1.1,1.2 1\n", wantInError: "NumStmt"},
		{name: "negative count", input: "mode: count\na.go:1.1,1.2 1 -1\n", wantInError: "Count"},
		{name: "backwards range", input: "mode: set\na.go:2.1,1.2 1 0\n", wantInError: "precedes start"},
		{
			name:        "inconsistent NumStmt",
			input:       "mode: set\na.go:1.1,1.2 1 0\na.go:1.1,1.2 2 1\n",
			wantInError: "inconsistent NumStmt",
		},
		{
			name:        "count overflow",
			input:       "mode: count\na.go:1.1,1.2 1 18446744073709551615\na.go:1.1,1.2 1 1\n",
			wantInError: "coverage count overflow",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := c0.ParseProfile(strings.NewReader(test.input))
			if err == nil {
				t.Fatal("ParseProfile succeeded, want error")
			}
			if !strings.Contains(err.Error(), test.wantInError) {
				t.Fatalf("error = %q, want it to contain %q", err, test.wantInError)
			}
		})
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func equalInts(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
