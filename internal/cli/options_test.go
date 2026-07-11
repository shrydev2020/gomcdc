package cli

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/shrydev2020/gomcdc/internal/config"
	"github.com/shrydev2020/gomcdc/internal/report"
)

func TestParseOptions(t *testing.T) {
	t.Parallel()
	var diagnostics bytes.Buffer
	opts, err := parseOptions([]string{
		"--coverage", "c1",
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

func TestParseOptionsDefaultsToCurrentPackage(t *testing.T) {
	t.Parallel()
	opts, err := parseOptions(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(opts.patterns, []string{"."}) {
		t.Fatalf("patterns = %#v, want current package", opts.patterns)
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

func TestParseOptionsRejectsLaterPhase(t *testing.T) {
	t.Parallel()
	opts, err := parseOptions([]string{"--coverage=c0,c1,c2,mcdc", "--fail-under-c2=70", "./..."}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.metrics.Enabled(config.MetricStatement) || !opts.metrics.Enabled(config.MetricMCDCMasking) {
		t.Fatalf("alias metrics = %v", opts.metrics.Names())
	}
	if !opts.failUnderCondition.set || opts.failUnderCondition.value != 70 {
		t.Fatalf("C2 threshold alias = %#v", opts.failUnderCondition)
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

func TestThresholdEnablesItsMetric(t *testing.T) {
	t.Parallel()
	opts, err := parseOptions([]string{"--coverage=decision", "--fail-under-statement=80"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.metrics.Enabled(config.MetricDecision) || !opts.metrics.Enabled(config.MetricStatement) {
		t.Fatalf("metrics = %v", opts.metrics.Names())
	}
}

func TestSpecialDenominatorPolicyIsValidated(t *testing.T) {
	t.Parallel()
	opts, err := parseOptions([]string{"--special-denominator=include"}, &bytes.Buffer{})
	if err != nil || opts.specialDenom != "include" {
		t.Fatalf("include policy: opts=%#v err=%v", opts, err)
	}
	if _, err := parseOptions([]string{"--special-denominator=maybe"}, &bytes.Buffer{}); err == nil {
		t.Fatal("invalid special denominator policy was accepted")
	}
}

func TestThresholdUsesExactCountsRatherThanRoundedPercentage(t *testing.T) {
	t.Parallel()
	metric := report.MetricSummary{Enabled: true, Covered: 2, Total: 3, Percentage: 66.67}
	if !belowThreshold(metric, 66.669) {
		t.Fatal("2/3 incorrectly passed 66.669% because of display rounding")
	}
	if belowThreshold(metric, 66.666) {
		t.Fatal("2/3 incorrectly failed 66.666%")
	}
}
