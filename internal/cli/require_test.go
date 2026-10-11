package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/mcdc"
	"github.com/shrydev2020/gomcdc/v2/internal/resultfile"
)

func TestRequireUsesMeasurementEvidence(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, test := range []struct {
		name   string
		change func(*resultfile.Result)
		flags  []string
		want   int
		reason string
	}{
		{"valid", func(*resultfile.Result) {}, []string{"--complete", "--mcdc-masking=100"}, ExitSuccess, "1/1; minimum 100.00%"},
		{"inflated", func(r *resultfile.Result) {
			r.Measurement.Runs[0].Runtime.Evaluations = r.Measurement.Runs[0].Runtime.Evaluations[:1]
		}, []string{"--mcdc-masking=100"}, ExitCoverageThreshold, "0/1; minimum 100.00%"},
		{"deflated", func(r *resultfile.Result) {
			r.Report.Summary.MCDCMasking.Covered = 0
		}, []string{"--mcdc-masking=100"}, ExitSuccess, "1/1; minimum 100.00%"},
		{"unknown", func(r *resultfile.Result) {
			r.Measurement.Runs[0].Probes.Decisions = nil
		}, []string{"--complete", "--mcdc-masking=100"}, ExitCoverageThreshold, "obligations are unknown"},
		{"incomplete execution", func(r *resultfile.Result) {
			r.Measurement.Runs[0].Complete = false
		}, []string{"--complete"}, ExitCoverageThreshold, "complete: unsatisfied"},
		{"unmeasured metric", func(r *resultfile.Result) {
			r.Report.Summary.Decision = r.Report.Summary.MCDCMasking
		}, []string{"--decision=100"}, ExitInvalidUsage, "decision was not measured"},
		{"report solver limit", func(r *resultfile.Result) {
			r.Report.MaskingAnalysisLimits.MaxSolverBytes = 1
		}, []string{"--complete", "--mcdc-masking=100"}, ExitSuccess, "1/1; minimum 100.00%"},
		{"removed report limits", func(r *resultfile.Result) {
			r.Report.MaskingAnalysisLimits = nil
		}, []string{"--complete", "--mcdc-masking=100"}, ExitSuccess, "1/1; minimum 100.00%"},
		{"recorded solver limit", func(r *resultfile.Result) {
			r.AnalysisSettings.MaskingAnalysisLimits.MaxSolverBytes = 1
		}, []string{"--complete", "--mcdc-masking=100"}, ExitCoverageThreshold, "analysis-incomplete"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := saveOfflineBoundaryFixture(t, root, "input", []string{"mcdc-masking"}, mcdc.AnalysisBudget{})
			saved := readSaved(t, path)
			test.change(&saved)
			if err := resultfile.Write(path, saved); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(root, "check.go")); err != nil {
				t.Fatal(err)
			}
			args := append([]string{"require"}, test.flags...)
			args = append(args, path)
			var output bytes.Buffer
			if code := runAt(t.Context(), root, args, &output, &output); code != test.want || !strings.Contains(output.String(), test.reason) {
				t.Fatalf("exit %d, want %d containing %q: %s", code, test.want, test.reason, output.String())
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("require changed the saved result")
			}
		})
	}
}

func TestRequireCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if code := runAt(ctx, t.TempDir(), []string{"require", "--complete", "input.json"}, io.Discard, io.Discard); code != ExitInterrupted {
		t.Fatalf("exit %d, want %d", code, ExitInterrupted)
	}
}
