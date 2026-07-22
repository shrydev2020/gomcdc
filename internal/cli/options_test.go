package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/report"
)

func TestHTMLOutputRequiresDirectory(t *testing.T) {
	t.Parallel()
	if _, err := parseOptions([]string{"--format=html", "./..."}, &bytes.Buffer{}); err == nil {
		t.Fatal("HTML format without output directory was accepted")
	}
	if _, err := parseOptions([]string{"--format=html", "--output=coverage-html", "./..."}, &bytes.Buffer{}); err != nil {
		t.Fatalf("valid HTML options: %v", err)
	}
}

func TestWriteHTMLReportCreatesIndex(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	opts := options{format: "html", output: "coverage-html"}
	input := report.Input{ModulePath: "example.test/m"}
	if err := writeReport(opts, report.Build(input), directory, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(directory, "coverage-html", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(contents, []byte("example.test/m")) || !bytes.Contains(contents, []byte("<!doctype html>")) {
		t.Fatalf("unexpected HTML output: %s", contents)
	}
}

func TestParseOptions(t *testing.T) {
	t.Parallel()
	var diagnostics bytes.Buffer
	opts, err := parseOptions([]string{
		"--coverage", "decision",
		"--format=json",
		"--exclude", "**/mock_*.go",
		"./...", "./cmd/...",
		"--", "-run", "TestOne",
	}, &diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(opts.patterns, []string{"./...", "./cmd/..."}) {
		t.Fatalf("patterns = %#v", opts.patterns)
	}
	if !reflect.DeepEqual(opts.goTestArgs, []string{"-run", "TestOne"}) {
		t.Fatalf("goTestArgs = %#v", opts.goTestArgs)
	}
	if opts.format != "json" || len(opts.excludes) != 1 {
		t.Fatalf("unexpected options: %#v", opts)
	}
}

func TestParseOptionsRequiresPackagePattern(t *testing.T) {
	t.Parallel()
	if _, err := parseOptions(nil, &bytes.Buffer{}); err == nil {
		t.Fatal("missing package pattern was accepted")
	}
}

func TestParseOptionsStrictMode(t *testing.T) {
	t.Parallel()
	opts, err := parseOptions([]string{"--strict", "./..."}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.strict {
		t.Fatal("--strict was not enabled")
	}
}

func TestParseOptionsMaskingAnalysisBudget(t *testing.T) {
	t.Parallel()
	opts, err := parseOptions([]string{
		"--coverage=mcdc-masking",
		"--mcdc-masking-max-evaluation-pairs=17",
		"--mcdc-masking-max-search-states=23",
		"--mcdc-masking-max-solver-bytes=4096",
		"./...",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	budget := opts.maskingAnalysisBudget()
	if budget.MaxEvaluationPairs != 17 || budget.MaxSearchStates != 23 || budget.MaxSolverBytes != 4096 {
		t.Fatalf("masking analysis budget = %#v", budget)
	}
}

func TestMaskingAnalysisBudgetRejectsZeroAndDisabledMetric(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"--coverage=mcdc-masking", "--mcdc-masking-max-search-states=0", "./..."},
		{"--coverage=decision", "--mcdc-masking-max-solver-bytes=4096", "./..."},
	} {
		if _, err := parseOptions(args, &bytes.Buffer{}); err == nil {
			t.Errorf("invalid Masking analysis budget was accepted: %v", args)
		}
	}
}

func TestMeasurementFlagRecognizesEveryOwnedGoTestFlag(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"count", "cover", "coverprofile", "covermode", "coverpkg", "json", "overlay", "toolexec"} {
		if got := measurementFlag([]string{"-" + name + "=value"}); got != name {
			t.Errorf("measurementFlag(-%s) = %q", name, got)
		}
	}
	if got := measurementFlag([]string{"-run=TestX", "-args", "-count=2"}); got != "" {
		t.Fatalf("test-binary argument was treated as a go test conflict: %q", got)
	}
}

func TestParseOptionsRejectsAmbiguousThresholdAliases(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"--fail-under-c1=70", "--fail-under-c2=70", "--fail-under-mcdc=70"} {
		if _, err := parseOptions([]string{name, "./..."}, &bytes.Buffer{}); err == nil {
			t.Errorf("ambiguous threshold option %q was accepted", name)
		}
	}
}

func TestParseOptionsRejectsNonFiniteThreshold(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"NaN", "+Inf", "-Inf"} {
		if _, err := parseOptions([]string{"--fail-under-decision=" + value}, &bytes.Buffer{}); err == nil {
			t.Errorf("threshold %q was accepted", value)
		}
	}
}

func TestThresholdRejectsDisabledMetric(t *testing.T) {
	t.Parallel()
	_, err := parseOptions([]string{"--coverage=decision", "--fail-under-statement=80"}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("threshold for a disabled metric was accepted")
	}
}

func TestThresholdFailsForEmptyDenominator(t *testing.T) {
	t.Parallel()
	if !belowThreshold(report.MetricSummary{Enabled: true}, 0) {
		t.Fatal("empty denominator passed a threshold")
	}
}

func TestThresholdUsesExactCountsRatherThanRoundedPercentage(t *testing.T) {
	t.Parallel()
	metric := report.MetricSummary{Enabled: true, Covered: 2, Total: 3}
	if !belowThreshold(metric, 66.669) {
		t.Fatal("2/3 incorrectly passed 66.669% because of display rounding")
	}
	if belowThreshold(metric, 66.666) {
		t.Fatal("2/3 incorrectly failed 66.666%")
	}
}

func TestEveryCanonicalMetricHasAnIndependentThreshold(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		summary report.Summary
	}{
		{"statement", report.Summary{Statement: passingMetric()}},
		{"function", report.Summary{Function: passingMetric()}},
		{"decision", report.Summary{Decision: passingMetric()}},
		{"switch-clause-body", report.Summary{SwitchClauseBody: passingMetric()}},
		{"type-switch-clause-body", report.Summary{TypeSwitchClauseBody: passingMetric()}},
		{"select-clause-body", report.Summary{SelectClauseBody: passingMetric()}},
		{"switch-clause-selection", report.Summary{SwitchClauseSelection: passingMetric()}},
		{"type-switch-clause-selection", report.Summary{TypeSwitchClauseSelection: passingMetric()}},
		{"condition", report.Summary{Condition: passingMetric()}},
		{"mcdc-unique", report.Summary{MCDCUnique: passingMetric()}},
		{"mcdc-masking", report.Summary{MCDCMasking: passingMetric()}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			opts, err := parseOptions([]string{
				"--coverage=" + test.name,
				"--fail-under-" + test.name + "=100",
				"./...",
			}, &bytes.Buffer{})
			if err != nil {
				t.Fatalf("parseOptions: %v", err)
			}
			failures := thresholdFailures(opts, report.Summary{})
			if len(failures) != 1 || !strings.Contains(failures[0], test.name) {
				t.Fatalf("threshold flag did not produce its own failure: %v", failures)
			}
			if failures := thresholdFailures(opts, test.summary); len(failures) != 0 {
				t.Fatalf("threshold was wired to the wrong metric: %v", failures)
			}
		})
	}
}

func passingMetric() report.MetricSummary {
	percentage := 100.0
	return report.MetricSummary{Enabled: true, Covered: 1, Total: 1, Percentage: &percentage}
}
