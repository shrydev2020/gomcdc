package config

import (
	"reflect"
	"testing"
)

func TestParseCoverageCanonicalNames(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value string
		want  []string
	}{
		{"all", []string{"condition", "decision", "function", "mcdc-masking", "mcdc-unique", "select-clause-body", "statement", "switch-clause-body", "switch-clause-selection", "type-switch-clause-body", "type-switch-clause-selection"}},
		{"statement,decision,condition,mcdc-unique,mcdc-masking", []string{"condition", "decision", "mcdc-masking", "mcdc-unique", "statement"}},
		{"switch-clause-body,type-switch-clause-body,select-clause-body,switch-clause-selection,type-switch-clause-selection", []string{"select-clause-body", "switch-clause-body", "switch-clause-selection", "type-switch-clause-body", "type-switch-clause-selection"}},
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
	for _, value := range []string{
		"", ",,", "decision,", "decision,,condition",
		"Decision", " decision", "decision ", "ALL",
		"branch", "c0", "c1", "c2", "mcdc", "clause",
	} {
		if _, err := ParseCoverage(value); err == nil {
			t.Errorf("ParseCoverage(%q) error = nil", value)
		}
	}
}
