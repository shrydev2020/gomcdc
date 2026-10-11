package analysis

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/analyzer"
	"github.com/shrydev2020/gomcdc/v2/internal/c0"
	"github.com/shrydev2020/gomcdc/v2/internal/config"
	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
	"github.com/shrydev2020/gomcdc/v2/internal/evidence"
	"github.com/shrydev2020/gomcdc/v2/internal/mcdc"
	"github.com/shrydev2020/gomcdc/v2/internal/measurement"
	"github.com/shrydev2020/gomcdc/v2/internal/runtimecov"
)

// selectionArchive is synthetic accepted evidence for one conjunction with a
// true evaluation and one short-circuiting false evaluation per condition.
func selectionArchive(t testing.TB, conditions int) measurement.Archive {
	t.Helper()
	const module = "example.test/selection"
	names := make([]string, conditions)
	for i := range names {
		names[i] = fmt.Sprintf("a%d", i)
	}
	source := []byte(fmt.Sprintf("package selection\nfunc F(%s bool) bool { if %s { return true }; return false }\n", strings.Join(names, ","), strings.Join(names, " && ")))
	root := t.TempDir()
	path := filepath.Join(root, "logic.go")
	if err := os.WriteFile(path, source, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := analyzer.AnalyzeFile(analyzer.FileOptions{Path: path, ModuleDir: root, ModulePath: module, PackagePath: module})
	if err != nil {
		t.Fatal(err)
	}
	d := file.Decisions[0].Metadata
	inventory, err := c0.BuildInventory("logic.go", source)
	if err != nil {
		t.Fatal(err)
	}
	covered := c0.AcceptedSourceEvidence{PackagePath: module, OriginalPath: "logic.go", OriginalSource: source, Inventory: inventory}
	for i, block := range inventory.Blocks {
		b := c0.AcceptedBlockEvidence{BlockIndex: i, Count: 1, Observed: true}
		for range block.Statements {
			b.Statements = append(b.Statements, c0.AcceptedStatementEvidence{Count: 1, Observed: true})
		}
		covered.Blocks = append(covered.Blocks, b)
	}
	id := strings.Repeat("a", 32)
	settings := measurement.Settings{InvocationID: id, WorkingDirectory: ".", Packages: []string{module}, Coverage: []string{"statement", "function", "decision", "condition", "mcdc-unique", "mcdc-masking"}, GoEnvironment: map[string]string{"GOVERSION": "go1.26.0", "GOOS": "linux", "GOARCH": "amd64"}, SourceDigests: map[string]string{"logic.go": measurement.Digest(source)}}
	planSettings, err := measurement.BuildSettings(settings)
	if err != nil {
		t.Fatal(err)
	}
	run := measurement.Run{ID: id, RuntimeID: id, Settings: settings, Status: cover.RunPassed, FailureKind: cover.RunFailureNone, Complete: true, Mode: "single-run", Results: measurement.RunResults{Test: measurement.ResultPassed, Measurement: measurement.ResultPassed, Integrity: measurement.ResultPassed, Strict: measurement.ResultNotRequested, Threshold: measurement.ResultNotRequested}, Packages: map[string]string{module: "passed"}, Probes: measurement.Probes{Decisions: []cover.DecisionID{d.ID}}, Cover: &c0.AcceptedCoverEvidence{Mode: c0.ModeCount, Sources: []c0.AcceptedSourceEvidence{covered}}}
	for _, producer := range []evidence.ProducerName{evidence.ProducerASTRuntime, evidence.ProducerGoCover} {
		run.Producers = append(run.Producers, evidence.ProducerOutcome{Producer: producer, Integrity: evidence.ProducerIntegrityValid, Completeness: evidence.ProducerCompletenessComplete, Mapping: evidence.ProducerMappingComplete, Usability: evidence.ProducerUsabilityAccepted})
	}
	for i := range conditions + 1 {
		states := make([]cover.ConditionState, conditions)
		for j := range states {
			switch {
			case i == 0 || j < i-1:
				states[j] = cover.ConditionTrue
			case j == i-1:
				states[j] = cover.ConditionFalse
			default:
				states[j] = cover.ConditionNotEvaluated
			}
		}
		run.Runtime.Evaluations = append(run.Runtime.Evaluations, cover.DecisionEvaluation{DecisionID: d.ID, EvaluationID: cover.EvaluationID(i + 1), RunID: id, PackagePath: module, ProcessID: 1, TestID: fmt.Sprintf("Test%d", i), Conditions: states, Result: i == 0, Status: cover.EvaluationCompleted})
	}
	return measurement.Archive{Plan: measurement.Plan{Definition: measurement.Definition, ToolVersion: "fixture", Module: module, Settings: planSettings, Sources: []measurement.Source{{PackagePath: module, Path: "logic.go", Content: source, SHA256: measurement.Digest(source)}}, Decisions: []cover.DecisionMetadata{d}}, Runs: []measurement.Run{run}}
}

func TestMeasurementMetricSelection(t *testing.T) {
	for _, runs := range []int{1, 2} {
		t.Run(fmt.Sprintf("runs=%d", runs), func(t *testing.T) {
			archive := selectionArchive(t, 3)
			if runs == 2 {
				second := archive.Runs[0]
				second.ID, second.RuntimeID = strings.Repeat("b", 32), strings.Repeat("b", 32)
				second.Settings.InvocationID = second.ID
				second.Runtime = runtimecov.RecordedEvidence{Evaluations: slices.Clone(second.Runtime.Evaluations)}
				for i := range second.Runtime.Evaluations {
					second.Runtime.Evaluations[i].RunID = second.ID
				}
				archive.Runs = append(archive.Runs, second)
			}
			before, _ := json.Marshal(archive)
			full, err := FromMeasurement(t.Context(), archive, mcdc.AnalysisBudget{})
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range archive.Plan.Settings.Coverage {
				selection := config.CoverageSet{config.Metric(name): true}
				selected, err := FromMeasurementMetrics(t.Context(), archive, mcdc.AnalysisBudget{}, selection)
				if err != nil {
					t.Fatal(err)
				}
				// Compare all summary fields without importing requirements, which
				// consumes analysis. Only the requested metric may be enabled.
				got, want := reflect.ValueOf(selected.Summary), reflect.ValueOf(full.Summary)
				enabled := reflect.ValueOf(newSummary(selection))
				for i := range got.NumField() {
					metric := got.Field(i).Interface().(MetricSummary)
					if metric.Enabled != enabled.Field(i).Interface().(MetricSummary).Enabled {
						t.Fatalf("%s enabled an unexpected metric: %+v", name, selected.Summary)
					}
					if metric.Enabled && !reflect.DeepEqual(metric, want.Field(i).Interface()) {
						t.Fatalf("%s differs from full analysis: %+v vs %+v", name, metric, want.Field(i).Interface())
					}
				}
				if name != "mcdc-masking" && selected.MaskingAnalysisLimits != nil {
					t.Fatal("unrequested Masking analysis ran")
				}
			}
			limited, err := FromMeasurement(t.Context(), archive, mcdc.AnalysisBudget{MaxSolverBytes: 1})
			if err != nil || limited.Summary.MCDCMasking.AnalysisIncomplete == 0 {
				t.Fatalf("fixture did not exercise the solver limit: %+v %v", limited.Summary, err)
			}
			selected, err := FromMeasurementMetrics(t.Context(), archive, mcdc.AnalysisBudget{MaxSolverBytes: 1}, config.CoverageSet{config.MetricStatement: true})
			if err != nil || !reflect.DeepEqual(selected.Summary.Statement, full.Summary.Statement) || selected.Summary.MCDCMasking.Enabled {
				t.Fatalf("statement selection changed coverage: %+v %v", selected.Summary, err)
			}
			after, _ := json.Marshal(archive)
			if !bytes.Equal(before, after) {
				t.Fatal("metric selection modified the measurement archive")
			}
		})
	}
}

func TestMeasurementSelectionRetainsArchiveValidation(t *testing.T) {
	archive := selectionArchive(t, 2)
	for _, selection := range []config.CoverageSet{{}, {config.MetricSwitchClauseBody: true}, {config.Metric("invalid"): true}} {
		if _, err := FromMeasurementMetrics(t.Context(), archive, mcdc.AnalysisBudget{}, selection); err == nil {
			t.Fatalf("invalid selection accepted: %v", selection)
		}
	}
	archive.Plan.Decisions[0].ID++
	if _, err := FromMeasurementMetrics(t.Context(), archive, mcdc.AnalysisBudget{}, config.CoverageSet{config.MetricStatement: true}); err == nil {
		t.Fatal("corrupt unrequested AST evidence accepted")
	}
}

func BenchmarkMeasurementMetricSelection(b *testing.B) {
	archive := selectionArchive(b, 16)
	for _, test := range []struct {
		name    string
		metrics config.CoverageSet
	}{
		{"all", nil},
		{"statement", config.CoverageSet{config.MetricStatement: true}},
	} {
		b.Run(test.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				result, err := FromMeasurementMetrics(b.Context(), archive, mcdc.AnalysisBudget{}, test.metrics)
				if err != nil || result.Summary.Statement.Covered == 0 {
					b.Fatalf("analysis: %+v %v", result.Summary.Statement, err)
				}
			}
		})
	}
}
