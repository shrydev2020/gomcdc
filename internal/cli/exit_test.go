package cli

import (
	"testing"

	"github.com/shrydev2020/gomcdc/internal/report"
)

func TestClassifyExitFollowsD28Precedence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                                   string
		invalid, measurement, tests, threshold bool
		want                                   int
	}{
		{"success", false, false, false, false, ExitSuccess},
		{"threshold", false, false, false, true, ExitCoverageThreshold},
		{"tests", false, false, true, true, ExitTestsFailed},
		{"measurement", false, true, true, true, ExitMeasurementFailed},
		{"invalid", true, true, true, true, ExitInvalidUsage},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			if got := classifyExit(test.invalid, test.measurement, test.tests, test.threshold); got != test.want {
				t.Fatalf("classifyExit() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestSummaryAnalysisIncompleteCountsEnabledMetricsOnly(t *testing.T) {
	t.Parallel()
	summary := report.Summary{
		Decision:    report.MetricSummary{Enabled: true, AnalysisIncomplete: 2},
		MCDCUnique:  report.MetricSummary{Enabled: true, AnalysisIncomplete: 3},
		MCDCMasking: report.MetricSummary{Enabled: false, AnalysisIncomplete: 100},
	}
	if got := summaryAnalysisIncomplete(summary); got != 5 {
		t.Fatalf("summaryAnalysisIncomplete() = %d, want 5", got)
	}
}
