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

	"github.com/shrydev2020/gomcdc/v2/internal/measurement"
	"github.com/shrydev2020/gomcdc/v2/internal/resultfile"
)

func TestEvidencePipelineAcrossExecutions(t *testing.T) {
	configureIntegrationEnvironment(t)
	root, outputs := t.TempDir(), t.TempDir()
	writeIntegrationFile(t, filepath.Join(root, "go.mod"), "module example.test/stages\n\ngo 1.26\n")
	writeIntegrationFile(t, filepath.Join(root, "logic.go"), "package stages\nfunc Choose(a,b bool) bool { if a && b {return true}; return false }\n")
	writeIntegrationFile(t, filepath.Join(root, "logic_test.go"), `package stages
import "testing"
func TestTrue(t *testing.T) { Choose(true,true) }
func TestFalseB(t *testing.T) { Choose(true,false) }
func TestFalseA(t *testing.T) { Choose(false,false) }
`)
	run := func(dir string, want int, args ...string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if code := runAt(t.Context(), dir, args, &stdout, &stderr); code != want {
			t.Fatalf("%v: exit %d want %d: %s", args, code, want, stderr.String())
		}
		return stdout.String()
	}
	var paths []string
	for _, name := range []string{"True", "FalseB", "FalseA"} {
		path := filepath.Join(outputs, name+".json")
		paths = append(paths, path)
		checkout := root
		if name == "FalseB" {
			checkout = t.TempDir()
			for _, file := range []string{"go.mod", "logic.go", "logic_test.go"} {
				data, err := os.ReadFile(filepath.Join(root, file))
				if err != nil {
					t.Fatal(err)
				}
				writeIntegrationFile(t, filepath.Join(checkout, file), string(data))
			}
		}
		run(checkout, ExitSuccess, "record", "--output="+path, "--coverage=all", ".", "--", "-run=^Test"+name+"$")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(`"mcdcMasking"`)) || bytes.Contains(raw, []byte(`"summary"`)) {
			t.Fatal("record stored analysis results")
		}
	}
	baselinePath := filepath.Join(outputs, "baseline.json")
	run(root, ExitSuccess, "test", "--coverage=all", "--save="+baselinePath, ".")
	baseline := readSaved(t, baselinePath)
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	args := append([]string{"merge", "--output=merged.json", "--expect-runs=3"}, paths...)
	run(outputs, ExitSuccess, args...)
	run(outputs, ExitSuccess, "merge", "--output=duplicate.json", "--expect-runs=3", "merged.json", paths[0])
	merged, _ := os.ReadFile(filepath.Join(outputs, "merged.json"))
	duplicate, _ := os.ReadFile(filepath.Join(outputs, "duplicate.json"))
	if !bytes.Equal(merged, duplicate) {
		t.Fatal("evidence union is not idempotent")
	}
	run(outputs, ExitMeasurementFailed, "merge", "--output=merged.json", "--expect-runs=3", paths[0], paths[1])
	after, _ := os.ReadFile(filepath.Join(outputs, "merged.json"))
	if !bytes.Equal(merged, after) {
		t.Fatal("missing shard replaced valid output")
	}
	run(outputs, ExitSuccess, "analyze", "--output=result.json", "merged.json")
	saved := readSaved(t, filepath.Join(outputs, "result.json"))
	if !reflect.DeepEqual(saved.Report.Summary, baseline.Report.Summary) {
		t.Fatalf("pipeline summary differs: %+v vs %+v", saved.Report.Summary, baseline.Report.Summary)
	}
	if !reflect.DeepEqual(saved.Report.Instrumentation, baseline.Report.Instrumentation) {
		t.Fatal("pipeline instrumentation differs")
	}
	if saved.Report.Summary.MCDCMasking.Covered != 2 {
		t.Fatal("missing cross-execution coverage")
	}
	for _, c := range saved.Report.Packages[0].Files[0].Functions[0].Decisions[0].MCDCMasking.Conditions {
		if c.Witness == nil || c.Witness.First.RunID == c.Witness.Second.RunID {
			t.Fatal("witness did not cross executions")
		}
	}
	run(outputs, ExitSuccess, "require", "--complete", "--mcdc-masking=100", "result.json")
	run(outputs, ExitSuccess, "report", "--format=html", "--output=html", "result.json")
	run(outputs, ExitSuccess, "analyze", "--output=partial.json", paths[0])
	run(outputs, ExitCoverageThreshold, "require", "--mcdc-masking=100", "partial.json")
	t.Run("require ignores inflated summary", func(t *testing.T) {
		run(outputs, ExitSuccess, "analyze", "--output=half.json", paths[0], paths[1])
		half := readSaved(t, filepath.Join(outputs, "half.json"))
		if metric := half.Report.Summary.MCDCMasking; metric.Covered != 1 || metric.Total != 2 {
			t.Fatalf("expected 50%% masking coverage: %+v", metric)
		}
		half.Report.Summary.MCDCMasking.Covered = 2
		if err := resultfile.Write(filepath.Join(outputs, "inflated.json"), half); err != nil {
			t.Fatal(err)
		}
		out := run(outputs, ExitCoverageThreshold, "require", "--mcdc-masking=100", "inflated.json")
		if !strings.Contains(out, "1/2; minimum 100.00%") {
			t.Fatalf("require did not use measurement coverage: %s", out)
		}
	})
	// Reanalysis uses archived evidence, never the saved presentation values.
	saved.Report.Summary.MCDCMasking.Covered = 0
	if err := resultfile.Write(filepath.Join(outputs, "modified.json"), saved); err != nil {
		t.Fatal(err)
	}
	run(outputs, ExitSuccess, "require", "--complete", "--mcdc-masking=100", "modified.json")
	run(outputs, ExitSuccess, "reanalyze", "--output=restored.json", "modified.json")
	if readSaved(t, filepath.Join(outputs, "restored.json")).Report.Summary.MCDCMasking.Covered != 2 {
		t.Fatal("reanalyze trusted saved summary")
	}
	// Check source/build conflicts, record identity conflicts and corruption at the archive boundary.
	archive, err := measurement.Decode(bytes.NewReader(merged))
	if err != nil {
		t.Fatal(err)
	}
	conflict := archive
	conflict.Runs = append([]measurement.Run(nil), archive.Runs...)
	conflict.Runs[0].Diagnostics = append([]measurement.Diagnostic{{Code: "different"}}, conflict.Runs[0].Diagnostics...)
	if _, err := measurement.Merge(t.Context(), []measurement.Archive{archive, conflict}, 0); err == nil {
		t.Fatal("conflicting execution accepted")
	}
	changed := bytes.Replace(merged, []byte(`"unknownSources": 0`), []byte(`"unknownSources": 1`), 1)
	if _, err := measurement.Decode(bytes.NewReader(changed)); err == nil {
		t.Fatal("corrupt archive accepted")
	}
}

func TestEvidenceStageUsage(t *testing.T) {
	for _, args := range [][]string{{"record", "."}, {"record", "--output=x", "--strict", "."}, {"record", "--output=x", "--format=json", "."}, {"analyze"}, {"analyze", "--output=x", "--expect-runs=-1", "y"}, {"require"}} {
		if code := runAt(t.Context(), t.TempDir(), args, io.Discard, io.Discard); code != ExitInvalidUsage {
			t.Fatalf("%v: %d", args, code)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if code := runAt(ctx, t.TempDir(), []string{"analyze", "--output=x", "y"}, io.Discard, io.Discard); code != ExitInterrupted {
		t.Fatalf("cancel exit %d", code)
	}
	var out bytes.Buffer
	if code := runAt(t.Context(), t.TempDir(), []string{"record", "--help"}, &out, io.Discard); code != ExitSuccess || !strings.Contains(out.String(), "record") {
		t.Fatal("missing record help")
	}
}

func TestRecordRepositoryWithMultipleMainPackages(t *testing.T) {
	configureIntegrationEnvironment(t)
	root, outputs := t.TempDir(), t.TempDir()
	writeIntegrationFile(t, filepath.Join(root, "go.mod"), "module example.test/commands\n\ngo 1.26\n")
	for _, name := range []string{"first", "second"} {
		writeIntegrationFile(t, filepath.Join(root, "cmd", name, "main.go"), "package main\nvar enabled bool\nfunc main(){if enabled {enabled=false}}\n")
		writeIntegrationFile(t, filepath.Join(root, "cmd", name, "main_test.go"), "package main\nimport \"testing\"\nfunc TestMainPaths(t *testing.T){enabled=true; main(); main()}\n")
	}
	path := filepath.Join(outputs, "commands.json")
	var stderr bytes.Buffer
	if code := runAt(t.Context(), root, []string{"record", "--coverage=decision", "--output=" + path, "./cmd/..."}, io.Discard, &stderr); code != ExitSuccess {
		t.Fatalf("record: %d %s", code, stderr.String())
	}
	if code := runAt(t.Context(), outputs, []string{"analyze", "--output=result.json", path}, io.Discard, &stderr); code != ExitSuccess {
		t.Fatalf("analyze: %d %s", code, stderr.String())
	}
	saved := readSaved(t, filepath.Join(outputs, "result.json"))
	if len(saved.Report.Packages) != 2 || saved.Report.Summary.Decision.Covered != 4 {
		t.Fatalf("multiple entry packages missing: %+v", saved.Report.Summary.Decision)
	}
}

func TestRecordEmptySwitchesStillEmitsNoMatchProbes(t *testing.T) {
	configureIntegrationEnvironment(t)
	root, outputs := t.TempDir(), t.TempDir()
	writeIntegrationFile(t, filepath.Join(root, "go.mod"), "module example.test/empty-switch\n\ngo 1.26\n")
	writeIntegrationFile(t, filepath.Join(root, "switch.go"), `package emptyswitch
func Choose(value int) {switch value {}}
func ChooseType(value any) {switch value.(type) {}}
`)
	writeIntegrationFile(t, filepath.Join(root, "switch_test.go"), `package emptyswitch
import "testing"
func TestEmpty(t *testing.T) {Choose(1); ChooseType(1)}
`)
	path := filepath.Join(outputs, "measurement.json")
	var stderr bytes.Buffer
	for _, command := range [][]string{
		{"record", "--output=" + path, "--coverage=switch-clause-selection,type-switch-clause-selection", "."},
		{"analyze", "--output=" + filepath.Join(outputs, "result.json"), path},
		{"require", "--complete", "--switch-clause-selection=100", "--type-switch-clause-selection=100", filepath.Join(outputs, "result.json")},
	} {
		if code := runAt(t.Context(), root, command, io.Discard, &stderr); code != ExitSuccess {
			t.Fatalf("%v: %d %s", command, code, stderr.String())
		}
	}
	result := readSaved(t, filepath.Join(outputs, "result.json"))
	if result.Report.Summary.SwitchClauseSelection.Covered != 1 || result.Report.Summary.TypeSwitchClauseSelection.Covered != 1 {
		t.Fatalf("empty switch selections lost: %+v", result.Report.Summary)
	}
}
