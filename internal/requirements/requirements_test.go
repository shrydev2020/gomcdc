package requirements

import (
	"math"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/analysis"
)

func TestRequirementsCannotBeSatisfiedByMissingKnowledge(t *testing.T) {
	for _, tt := range []struct {
		name   string
		metric analysis.MetricSummary
		want   bool
	}{
		{"covered", analysis.MetricSummary{Enabled: true, Covered: 2, Total: 2}, true},
		{"uncovered", analysis.MetricSummary{Enabled: true, Covered: 1, Total: 2}, false},
		{"unknown", analysis.MetricSummary{Enabled: true, Covered: 2, Total: 2, Unknown: 1}, false},
		{"unsupported", analysis.MetricSummary{Enabled: true, Covered: 2, Total: 2, Unsupported: 1}, false},
		{"unfinished", analysis.MetricSummary{Enabled: true, Covered: 2, Total: 2, AnalysisIncomplete: 1}, false},
		{"empty", analysis.MetricSummary{Enabled: true}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result := analysis.Result{Summary: analysis.Summary{MCDCMasking: tt.metric}}
			out, err := Evaluate(result, Requirements{Minimum: map[string]float64{"mcdc-masking": 100}})
			if err != nil || len(out) != 1 || out[0].Satisfied != tt.want {
				t.Fatalf("%+v %v", out, err)
			}
		})
	}
	for _, minimum := range []float64{-1, 101, math.NaN(), math.Inf(1)} {
		_, err := Evaluate(analysis.Result{Summary: analysis.Summary{Decision: analysis.MetricSummary{Enabled: true}}}, Requirements{Minimum: map[string]float64{"decision": minimum}})
		if err == nil {
			t.Fatal("invalid percentage accepted")
		}
	}
}
func TestCompletenessRetainsExecutionFailure(t *testing.T) {
	result := analysis.Result{Run: analysis.Run{Complete: false}, Summary: analysis.Summary{MCDCMasking: analysis.MetricSummary{Enabled: true, Covered: 2, Total: 2}}}
	out, err := Evaluate(result, Requirements{Complete: true, Minimum: map[string]float64{"mcdc-masking": 100}})
	if err != nil || len(out) != 2 || out[0].Satisfied || !out[1].Satisfied {
		t.Fatalf("%+v %v", out, err)
	}
}
