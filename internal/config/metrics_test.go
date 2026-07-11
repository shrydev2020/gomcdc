package config

import (
	"reflect"
	"testing"
)

func TestParseCoverageAliases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value string
		want  []string
	}{
		{"all", []string{"clause", "condition", "decision", "function", "mcdc-masking", "mcdc-unique", "statement"}},
		{"c0,c1,c2,mcdc", []string{"condition", "decision", "mcdc-masking", "mcdc-unique", "statement"}},
		{"statement,function,decision,clause,condition,mcdc-unique,mcdc-masking", []string{"clause", "condition", "decision", "function", "mcdc-masking", "mcdc-unique", "statement"}},
	}
	for _, test := range tests {
		set, err := ParseCoverage(test.value)
		if err != nil {
			t.Fatalf("ParseCoverage(%q): %v", test.value, err)
		}
		if got := set.Names(); !reflect.DeepEqual(got, test.want) {
			t.Errorf("ParseCoverage(%q).Names() = %v, want %v", test.value, got, test.want)
		}
	}
}

func TestParseCoverageRejectsUnknownAndEmpty(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", ",,", "branch"} {
		if _, err := ParseCoverage(value); err == nil {
			t.Errorf("ParseCoverage(%q) error = nil", value)
		}
	}
}
