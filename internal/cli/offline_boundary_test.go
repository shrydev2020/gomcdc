package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/config"
	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
	"github.com/shrydev2020/gomcdc/v2/internal/mcdc"
	"github.com/shrydev2020/gomcdc/v2/internal/report"
	"github.com/shrydev2020/gomcdc/v2/internal/resultfile"
)

func saveOfflineBoundaryFixture(t *testing.T, root, name string, metrics []string, budget mcdc.AnalysisBudget) string {
	t.Helper()
	selection, err := config.ParseCoverage(strings.Join(metrics, ","))
	if err != nil {
		t.Fatal(err)
	}
	const pkg = "example.test/offline"
	source := []byte("package offline\nfunc Check(a bool) bool { if a { return true }; return false }\n")
	location := cover.SourceLocation{File: "check.go", Start: cover.Position{Line: 2, Column: 30}, End: cover.Position{Line: 2, Column: 31}}
	built := report.Build(report.Input{
		ModulePath: pkg, ToolVersion: "fixture", Coverage: selection, MaskingAnalysisBudget: budget,
		RunStatus: cover.RunPassed, FailureKind: cover.RunFailureNone, Complete: true, MeasurementMode: report.MeasurementSingleRun,
		Results:          report.RunResults{Test: report.ResultPassed, Measurement: report.ResultPassed, Integrity: report.ResultPassed, Strict: report.ResultNotRequested, Threshold: report.ResultNotRequested},
		ProducerOutcomes: []report.ProducerOutcome{{Producer: report.ProducerASTRuntime, Integrity: report.ProducerIntegrityValid, Completeness: report.ProducerCompletenessComplete, Mapping: report.ProducerMappingComplete, Usability: report.ProducerUsabilityAccepted}},
		PackageStatuses:  map[string]string{pkg: "passed"},
		Decisions:        []cover.DecisionMetadata{{ID: 1, Package: pkg, Function: "Check", Kind: cover.DecisionIf, Location: location, Expression: "a", ExpressionTree: cover.NewConditionExpression(0), Conditions: []cover.ConditionMetadata{{ID: 2, Index: 0, Location: location, Expression: "a"}}}},
		Evaluations: []cover.DecisionEvaluation{
			{DecisionID: 1, EvaluationID: 1, RunID: name, PackagePath: pkg, ProcessID: 1, TestID: "TestTrue", Conditions: []cover.ConditionState{cover.ConditionTrue}, Result: true, Status: cover.EvaluationCompleted},
			{DecisionID: 1, EvaluationID: 2, RunID: name, PackagePath: pkg, ProcessID: 1, TestID: "TestFalse", Conditions: []cover.ConditionState{cover.ConditionFalse}, Result: false, Status: cover.EvaluationCompleted},
		},
	})
	saved := resultfile.New(resultfile.Settings{
		InvocationID: resultfile.Digest([]byte(name))[:32], WorkingDirectory: ".", Coverage: metrics, Packages: []string{pkg},
		GoEnvironment: map[string]string{"GOVERSION": "go1.26.0", "GOOS": "linux", "GOARCH": "amd64"},
		SourceDigests: map[string]string{"check.go": resultfile.Digest(source)},
	}, built, []report.SourceFileInput{{PackagePath: pkg, Path: "check.go", Source: source}})
	path := filepath.Join(root, name+".json")
	if err := resultfile.Write(path, saved); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOfflineAnalysisLimitInheritanceAndOverrides(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	stored := mcdc.AnalysisBudget{MaxEvaluationPairs: 11, MaxSearchStates: 23, MaxSolverBytes: 4096}
	for _, command := range []string{"merge", "reanalyze"} {
		for _, test := range []struct {
			name, flag string
			update     func(*mcdc.AnalysisBudget)
		}{
			{"omitted", "", func(*mcdc.AnalysisBudget) {}},
			{"pairs", "--mcdc-masking-max-evaluation-pairs=17", func(b *mcdc.AnalysisBudget) { b.MaxEvaluationPairs = 17 }},
			{"states", "--mcdc-masking-max-search-states=31", func(b *mcdc.AnalysisBudget) { b.MaxSearchStates = 31 }},
			{"bytes", "--mcdc-masking-max-solver-bytes=8192", func(b *mcdc.AnalysisBudget) { b.MaxSolverBytes = 8192 }},
		} {
			t.Run(command+"/"+test.name, func(t *testing.T) {
				root := t.TempDir()
				first := saveOfflineBoundaryFixture(t, root, "first", []string{"mcdc-masking"}, stored)
				want := stored
				args := []string{command, "--output=derived.json"}
				if test.flag != "" {
					args = append(args, test.flag)
				}
				args = append(args, first)
				if command == "merge" {
					// Different original limits must lead to deterministic merge defaults.
					second := saveOfflineBoundaryFixture(t, root, "second", []string{"mcdc-masking"}, mcdc.AnalysisBudget{MaxEvaluationPairs: 7, MaxSearchStates: 13, MaxSolverBytes: 2048})
					args = append(args, second)
					want = mcdc.EffectiveMaskingAnalysisBudget(mcdc.AnalysisBudget{})
				}
				test.update(&want)
				var diagnostics bytes.Buffer
				if code := runAt(t.Context(), root, args, io.Discard, &diagnostics); code != ExitSuccess {
					t.Fatalf("exit %d: %s", code, diagnostics.String())
				}
				got := readSaved(t, filepath.Join(root, "derived.json"))
				expected := &report.MaskingAnalysisLimits{MaxEvaluationPairs: want.MaxEvaluationPairs, MaxSearchStates: want.MaxSearchStates, MaxSolverBytes: want.MaxSolverBytes}
				if !reflect.DeepEqual(got.Report.MaskingAnalysisLimits, expected) {
					t.Fatalf("limits = %+v, want %+v", got.Report.MaskingAnalysisLimits, expected)
				}
				if got.Report.Summary.MCDCMasking.Covered != 1 || got.Report.Summary.MCDCMasking.Total != 1 {
					t.Fatalf("complete fixture lost its witness: %+v", got.Report.Summary.MCDCMasking)
				}
				original := readSaved(t, first)
				if *original.Report.MaskingAnalysisLimits != (report.MaskingAnalysisLimits{MaxEvaluationPairs: 11, MaxSearchStates: 23, MaxSolverBytes: 4096}) {
					t.Fatal("original limits changed")
				}
			})
		}
	}
}

func TestOfflineLimitsRequireSavedMaskingSelection(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, command := range []string{"merge", "reanalyze"} {
		for _, flag := range []string{"--mcdc-masking-max-evaluation-pairs=17", "--mcdc-masking-max-search-states=31", "--mcdc-masking-max-solver-bytes=8192"} {
			t.Run(command+"/"+flag, func(t *testing.T) {
				root := t.TempDir()
				input := saveOfflineBoundaryFixture(t, root, "input", []string{"decision"}, mcdc.AnalysisBudget{})
				output := filepath.Join(root, "existing.json")
				writeIntegrationFile(t, output, "previous result")
				args := []string{command, "--output=" + output, flag, input}
				if command == "merge" {
					args = append(args, input)
				}
				var diagnostics bytes.Buffer
				if code := runAt(t.Context(), root, args, io.Discard, &diagnostics); code != ExitInvalidUsage || !strings.Contains(diagnostics.String(), "require mcdc-masking") {
					t.Fatalf("exit %d: %s", code, diagnostics.String())
				}
				assertIntegrationFileContents(t, output, "previous result")
			})
		}
	}
}

func TestOfflineAnalysisProtectsEveryAliasedInput(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"merge", "reanalyze"} {
		for _, kind := range []string{"symlink", "hardlink", "parent symlink"} {
			for _, target := range []string{"first", "second"} {
				if command == "reanalyze" && target == "second" {
					continue
				}
				t.Run(command+"/"+kind+"/"+target, func(t *testing.T) {
					root := t.TempDir()
					first := saveOfflineBoundaryFixture(t, root, "first", []string{"decision"}, mcdc.AnalysisBudget{})
					second := saveOfflineBoundaryFixture(t, root, "second", []string{"decision"}, mcdc.AnalysisBudget{})
					original := filepath.Join(root, target+".json")
					alias := filepath.Join(root, "alias.json")
					var err error
					switch kind {
					case "symlink":
						err = os.Symlink(original, alias)
					case "hardlink":
						err = os.Link(original, alias)
					case "parent symlink":
						err = os.Symlink(root, filepath.Join(root, "alias-directory"))
						alias = filepath.Join(root, "alias-directory", target+".json")
					}
					if err != nil {
						t.Fatal(err)
					}
					before := map[string][]byte{}
					for _, path := range []string{first, second} {
						data, err := os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						before[path] = data
					}
					args := []string{command, "--output=" + alias, first}
					if command == "merge" {
						args = append(args, second)
					}
					var diagnostics bytes.Buffer
					if code := runAt(t.Context(), root, args, io.Discard, &diagnostics); code != ExitInvalidUsage || !strings.Contains(diagnostics.String(), "different files") {
						t.Fatalf("alias exit %d: %s", code, diagnostics.String())
					}
					for path, want := range before {
						got, err := os.ReadFile(path)
						if err != nil || !bytes.Equal(got, want) {
							t.Fatalf("input %s changed: %v", path, err)
						}
					}
				})
			}
		}
	}
}
