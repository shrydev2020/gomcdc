package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/analysis"
	"github.com/shrydev2020/gomcdc/v2/internal/measurement"
	"github.com/shrydev2020/gomcdc/v2/internal/resultfile"
)

func readSaved(t *testing.T, path string) resultfile.Result {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	result, err := resultfile.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestMergeAndReanalyzeOfflineAcrossTestShards(t *testing.T) {
	configureIntegrationEnvironment(t)
	root, outputs := t.TempDir(), t.TempDir()
	marker := filepath.Join(outputs, "runs")
	t.Setenv("GOMCDC_OFFLINE_TEST_MARKER", marker)
	writeIntegrationFile(t, filepath.Join(root, "go.mod"), "module example.test/offline\n\ngo 1.26\n")
	writeIntegrationFile(t, filepath.Join(root, "logic.go"), `package offline
func Choose(a,b bool) int {
 if a && b { return 1 }
 switch a { case true: return 2; default: return 3 }
}
`)
	writeIntegrationFile(t, filepath.Join(root, "logic_test.go"), `package offline
import("testing";"os")
func mark(t *testing.T) { f,e:=os.OpenFile(os.Getenv("GOMCDC_OFFLINE_TEST_MARKER"),os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600);if e!=nil { t.Fatal(e) };defer f.Close();f.WriteString("x") }
func TestTrue(t *testing.T) { mark(t);Choose(true,true) }
func TestFalseB(t *testing.T) { mark(t);Choose(true,false) }
func TestFalseA(t *testing.T) { mark(t);Choose(false,false);t.Error("expected failure") }
`)
	for _, test := range []string{"TestTrue", "TestFalseB", "TestFalseA", "Test"} {
		var stderr bytes.Buffer
		code := runAt(t.Context(), root, []string{"test", "--coverage=all", "--strict", "--fail-under-mcdc-masking=100", "--save=" + filepath.Join(outputs, test+".json"), ".", "--", "-run", "^" + test}, io.Discard, &stderr)
		want := ExitCoverageThreshold
		if test == "TestFalseA" || test == "Test" {
			want = ExitTestsFailed
		}
		if code != want {
			t.Fatalf("%s exit %d want %d: %s", test, code, want, stderr.String())
		}
	}
	for _, test := range []string{"TestTrue", "TestFalseB", "TestFalseA"} {
		saved := readSaved(t, filepath.Join(outputs, test+".json"))
		if err := measurement.Write(t.Context(), filepath.Join(outputs, test+".measurement.json"), saved.Measurement); err != nil {
			t.Fatal(err)
		}
	}
	baseline := readSaved(t, filepath.Join(outputs, "Test.json"))
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	run := func(args ...string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if code := runAt(t.Context(), outputs, args, &stdout, &stderr); code != ExitSuccess {
			t.Fatalf("%v exit=%d: %s", args, code, stderr.String())
		}
	}
	run("analyze", "--output=limited.json", "--mcdc-masking-max-solver-bytes=1", "TestTrue.measurement.json", "TestFalseA.measurement.json", "TestFalseB.measurement.json")
	limited := readSaved(t, filepath.Join(outputs, "limited.json"))
	if limited.Report.Summary.MCDCMasking.AnalysisIncomplete == 0 {
		t.Fatal("small solver budget did not preserve incomplete analysis")
	}
	run("reanalyze", "--output=complete.json", "--mcdc-masking-max-solver-bytes=67108864", "limited.json")
	complete := readSaved(t, filepath.Join(outputs, "complete.json"))
	if !reflect.DeepEqual(complete.Report.Summary, baseline.Report.Summary) {
		t.Fatalf("merged summary differs from a single complete execution:\n%+v\n%+v", complete.Report.Summary, baseline.Report.Summary)
	}
	if complete.Report.Summary.MCDCMasking.Covered != 2 || complete.Report.Summary.MCDCMasking.AnalysisIncomplete != 0 {
		t.Fatalf("cross-run witnesses missing: %+v", complete.Report.Summary.MCDCMasking)
	}
	if len(complete.Measurement.Runs) != 3 || complete.Report.MeasurementMode != analysis.MeasurementMerged || complete.Report.Run.Results.Test != analysis.ResultFailed || complete.Report.Run.Results.Strict != analysis.ResultNotRequested || complete.Report.Run.Results.Threshold != analysis.ResultNotRequested {
		t.Fatalf("wrong provenance/outcomes: %+v", complete.Report.Run)
	}
	for _, origin := range complete.Report.ContributingRuns {
		found := false
		for _, diagnostic := range origin.Errors {
			if diagnostic.Code == "coverage-threshold-failed" {
				found = true
			}
		}
		if !found {
			t.Fatal("original threshold diagnostic lost")
		}
		if origin.Run.Results.Threshold != analysis.ResultFailed {
			t.Fatal("original threshold failure lost")
		}
	}
	// A witness must actually join different executions, not just retain a
	// preexisting percentage from either shard.
	d := complete.Report.Packages[0].Files[0].Functions[0].Decisions[0]
	for _, condition := range d.MCDCMasking.Conditions {
		if condition.Witness == nil || condition.Witness.First.RunID == condition.Witness.Second.RunID {
			t.Fatal("MC/DC witness does not join independent runs")
		}
	}
	if err := measurement.Write(t.Context(), filepath.Join(outputs, "combined.measurement.json"), complete.Measurement); err != nil {
		t.Fatal(err)
	}
	run("analyze", "--output=nested.json", "combined.measurement.json", "TestTrue.measurement.json", "TestFalseB.measurement.json")
	run("analyze", "--output=reversed.json", "TestFalseB.measurement.json", "TestFalseA.measurement.json", "TestTrue.measurement.json")
	want, _ := resultfile.Encode(complete)
	for _, name := range []string{"nested.json", "reversed.json"} {
		got, err := os.ReadFile(filepath.Join(outputs, name))
		if err != nil || !bytes.Equal(want, got) {
			t.Fatalf("%s differs after flattening/deduplication: %v", name, err)
		}
	}
	run("report", "--format=html", "--output=html", "complete.json")
	run("report", "--format=json", "--output=rendered.json", "complete.json")
	run("report", "complete.json")
	if runs, err := os.ReadFile(marker); err != nil || string(runs) != "xxxxxx" {
		t.Fatalf("offline operation reran tests: %q %v", runs, err)
	}
	// Mismatch must leave the destination intact.
	incompatible := readSaved(t, filepath.Join(outputs, "TestTrue.json"))
	incompatible.Measurement.Plan.Settings.GOFLAGS = "-race"
	incompatible.Measurement.Runs[0].Settings.GOFLAGS = "-race"
	if err := measurement.Write(t.Context(), filepath.Join(outputs, "incompatible.json"), incompatible.Measurement); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if code := runAt(t.Context(), outputs, []string{"merge", "--output=complete.json", "TestFalseA.measurement.json", "incompatible.json"}, io.Discard, &stderr); code != ExitMeasurementFailed || !strings.Contains(stderr.String(), "incompatible") {
		t.Fatalf("mismatch accepted: %d %s", code, stderr.String())
	}
	after, _ := os.ReadFile(filepath.Join(outputs, "complete.json"))
	if !bytes.Equal(want, after) {
		t.Fatal("failed merge overwrote destination")
	}
}

func TestOfflineAnalysisUsageAndCancellation(t *testing.T) {
	for _, args := range [][]string{
		{"merge"}, {"reanalyze", "--output=x", "one", "two"},
		{"merge", "--output=-", "one", "two"}, {"reanalyze", "--output=x", "--mcdc-masking-max-search-states=0", "one"},
		{"reanalyze", "--output=one", "one"},
	} {
		if code := runAt(t.Context(), t.TempDir(), args, io.Discard, io.Discard); code != ExitInvalidUsage {
			t.Fatalf("%v: exit %d", args, code)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if code := runAt(ctx, t.TempDir(), []string{"reanalyze", "--output=x", "one"}, io.Discard, io.Discard); code != ExitInterrupted {
		t.Fatalf("cancel exit %d", code)
	}
}
