package resultfile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/config"
	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
	"github.com/shrydev2020/gomcdc/v2/internal/mcdc"
	"github.com/shrydev2020/gomcdc/v2/internal/report"
)

func TestAnalysisSourceOrderIsIndependentOfInputOrder(t *testing.T) {
	t.Parallel()
	a, b := c0Snapshot("00000000000000000000000000000001"), c0Snapshot("00000000000000000000000000000002")
	source := Source{PackagePath: "example.test/p", Path: "other.go", Content: []byte("package p\n")}
	source.SHA256 = Digest(source.Content)
	for _, result := range []*Result{&a, &b} {
		result.Sources = append(result.Sources, source)
		result.Settings.SourceDigests[source.Path] = source.SHA256
	}
	slices.Reverse(b.Sources)
	first, err := Analyze(t.Context(), []Result{a, b}, mcdc.AnalysisBudget{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Analyze(t.Context(), []Result{b, a}, mcdc.AnalysisBudget{})
	if err != nil {
		t.Fatal(err)
	}
	left, _ := Encode(first)
	right, _ := Encode(second)
	if !bytes.Equal(left, right) {
		t.Fatal("source ordering changed the derived result")
	}
}

func TestAnalyzePreservesBuildAndBinaryArgumentBoundaries(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, diagnostic string
		left, right      []string
		goFlags          string
	}{
		{"runtime selection", "", []string{"-run=First"}, []string{"-test.run=Second"}, ""},
		{"binary selection", "incompatible saved results", []string{"-args", "-run=First"}, []string{"-args", "-run=Second"}, ""},
		{"unknown flag", "incompatible saved results", []string{"-custom=first"}, []string{"-custom=second"}, ""},
		{"build flag value resembles runtime flag", "incompatible saved results", []string{"-ldflags", "-run=First"}, []string{"-ldflags", "-run=Second"}, ""},
		{"missing value", "requires a value", nil, []string{"-run"}, ""},
		{"GOFLAGS missing value", "requires a value", nil, nil, "-run"},
		{"GOFLAGS open quote", "unterminated", nil, nil, `-tags="open`},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, b := c0Snapshot(strings.Repeat("a", 32)), c0Snapshot(strings.Repeat("b", 32))
			a.Settings.GoTestArgs, b.Settings.GoTestArgs = test.left, test.left
			if _, err := Analyze(t.Context(), []Result{a, b}, mcdc.AnalysisBudget{}); err != nil {
				t.Fatalf("matching control: %v", err)
			}
			b.Settings.GoTestArgs, b.Settings.GOFLAGS = test.right, test.goFlags
			for range 2 {
				_, err := Analyze(t.Context(), []Result{a, b}, mcdc.AnalysisBudget{})
				if test.diagnostic == "" {
					if err != nil {
						t.Fatalf("compatible runtime selection: %v", err)
					}
				} else if err == nil || !strings.Contains(err.Error(), test.diagnostic) {
					t.Fatalf("want %q, got %v", test.diagnostic, err)
				}
				a, b = b, a
			}
		})
	}
}

func TestAnalyzeRequiresIndependentPhysicalRuns(t *testing.T) {
	t.Parallel()
	snapshot := func(id string, value bool) Result {
		r := c0Snapshot(id)
		source := []byte("package p\nfunc Check(a bool) bool { if a { return true }; return false }\n")
		r.Sources[0].Content, r.Sources[0].SHA256 = source, Digest(source)
		r.Settings.SourceDigests["p.go"] = Digest(source)
		r.Settings.Coverage = []string{"decision", "condition", "mcdc-unique", "mcdc-masking"}
		state := cover.ConditionFalse
		if value {
			state = cover.ConditionTrue
		}
		location := cover.SourceLocation{File: "p.go", Start: cover.Position{Line: 2, Column: 30}, End: cover.Position{Line: 2, Column: 31}}
		r.Report = report.Build(report.Input{
			ModulePath: "example.test/p", ToolVersion: "test-version", Coverage: config.AllCoverage(),
			RunStatus: cover.RunPassed, FailureKind: cover.RunFailureNone, Complete: true, MeasurementMode: report.MeasurementSingleRun,
			Decisions:   []cover.DecisionMetadata{{ID: 1, Package: "example.test/p", Function: "Check", Kind: cover.DecisionIf, Location: location, Expression: "a", ExpressionTree: cover.NewConditionExpression(0), Conditions: []cover.ConditionMetadata{{ID: 2, Index: 0, Location: location, Expression: "a"}}}},
			Evaluations: []cover.DecisionEvaluation{{DecisionID: 1, EvaluationID: 1, RunID: id, PackagePath: "example.test/p", ProcessID: 1, Conditions: []cover.ConditionState{state}, Result: value, Status: cover.EvaluationCompleted}},
		})
		return r
	}
	for _, test := range []struct{ name, runID, diagnostic string }{
		{"independent", strings.Repeat("b", 32), ""},
		{"overlapping", strings.Repeat("a", 32), "overlap the same physical run"},
		{"missing", "", "missing its run identity"},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, b := snapshot(strings.Repeat("a", 32), true), snapshot(strings.Repeat("b", 32), false)
			b.Report.Packages[0].Files[0].Functions[0].Decisions[0].Evaluations[0].RunID = test.runID
			// Distinct evaluations from one physical run still represent overlapping
			// exports, even when the evaluation identities themselves do not collide.
			b.Report.Packages[0].Files[0].Functions[0].Decisions[0].Evaluations[0].EvaluationID = "2"
			for range 2 {
				merged, err := Analyze(t.Context(), []Result{a, b}, mcdc.AnalysisBudget{})
				if test.diagnostic != "" {
					if err == nil || !strings.Contains(err.Error(), test.diagnostic) || merged.Report.SchemaVersion != "" {
						t.Fatalf("ambiguous evidence: %v", err)
					}
				} else if err != nil || len(merged.Inputs) != 2 || merged.Report.Summary.MCDCMasking.Covered != 1 || merged.Report.Summary.MCDCUnique.Covered != 1 {
					t.Fatalf("independent observations should form one witness: %+v %v", merged.Report.Summary, err)
				}
				a, b = b, a
			}
		})
	}
}

func TestCanceledAnalysisPublicationPreservesDestination(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "saved.json")
	if err := os.WriteFile(path, []byte("previous"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := WriteContext(ctx, path, c0Snapshot("00000000000000000000000000000001")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "previous" {
		t.Fatal("cancellation replaced the existing result")
	}
}

func c0Snapshot(id string) Result {
	r := fixture()
	r.Settings.InvocationID = id
	r.Settings.Coverage = []string{"statement"}
	r.Settings.Packages = []string{"example.test/p"}
	r.Report.Run = report.Run{Status: cover.RunPassed, FailureKind: cover.RunFailureNone, Complete: true, Results: report.RunResults{Test: report.ResultPassed, Measurement: report.ResultPassed, Integrity: report.ResultPassed, Strict: report.ResultNotRequested, Threshold: report.ResultNotRequested}}
	r.Report.MeasurementMode = report.MeasurementStandardCover
	r.Report.Summary.Statement.Enabled = true
	r.Report.Packages[0].Status = "passed"
	r.Report.Packages[0].Files[0].Functions[0].Statements[0].Count = 3
	r.Report.Packages[0].Files[0].Functions[0].Statements[0].Metric = report.MetricSummary{Enabled: true, Total: 1, Covered: 1, Percentage: new(float64(100))}
	return r
}

func TestAnalyzeDistinctC0InvocationsAndLegacyDeduplication(t *testing.T) {
	t.Parallel()
	a, b := c0Snapshot("00000000000000000000000000000001"), c0Snapshot("00000000000000000000000000000002")
	merged, err := Analyze(t.Context(), []Result{a, b, a}, mcdc.AnalysisBudget{})
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Inputs) != 2 || merged.Report.Packages[0].Files[0].Functions[0].Statements[0].Count != 6 || merged.Report.Summary.Statement.Covered != 1 {
		t.Fatal("separate identical C0-only runs collapsed or doubled the denominator")
	}
	encoded, err := Encode(merged)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	again, err := Analyze(t.Context(), []Result{decoded, a}, mcdc.AnalysisBudget{})
	if err != nil {
		t.Fatal(err)
	}
	reencoded, _ := Encode(again)
	if !bytes.Equal(encoded, reencoded) {
		t.Fatal("nested archive counted an original twice")
	}
	legacy := c0Snapshot("")
	legacyBytes, err := Encode(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(legacyBytes, []byte(`"version": "1.0"`)) {
		t.Fatal("legacy codec version changed")
	}
	legacy, err = Decode(bytes.NewReader(legacyBytes))
	if err != nil {
		t.Fatal(err)
	}
	result, err := Analyze(t.Context(), []Result{legacy, legacy}, mcdc.AnalysisBudget{})
	if err != nil || len(result.Inputs) != 1 {
		t.Fatalf("legacy snapshot deduplication failed: %v", err)
	}
}

func TestAnalyzeChecksMeasurementCompatibility(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*Result)
	}{
		{"source", func(r *Result) {
			r.Sources[0].Content = append(r.Sources[0].Content, '\n')
			r.Sources[0].SHA256 = Digest(r.Sources[0].Content)
			r.Settings.SourceDigests["p.go"] = r.Sources[0].SHA256
		}},
		{"build flags", func(r *Result) { r.Settings.GoTestArgs = []string{"-tags=other"} }},
		{"GOFLAGS", func(r *Result) { r.Settings.GOFLAGS = "-race" }},
		{"toolchain", func(r *Result) { r.Settings.GoEnvironment["GOVERSION"] = "go1.99.0" }},
		{"packages", func(r *Result) { r.Settings.Packages = append(r.Settings.Packages, "example.test/other") }},
		{"coverage", func(r *Result) { r.Settings.Coverage = append(r.Settings.Coverage, "decision") }},
		{"module config", func(r *Result) {
			r.Settings.ModuleConfigDigests = map[string]string{"go.mod": Digest([]byte("different"))}
		}},
		{"binary arguments", func(r *Result) { r.Settings.GoTestArgs = []string{"-args", "-custom=changed"} }},
		{"mode", func(r *Result) { r.Report.MeasurementMode = report.MeasurementSingleRun }},
		{"tool", func(r *Result) { r.Report.ToolVersion = "different" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, b := c0Snapshot("00000000000000000000000000000001"), c0Snapshot("00000000000000000000000000000002")
			test.mutate(&b)
			if _, err := Analyze(t.Context(), []Result{a, b}, mcdc.AnalysisBudget{}); err == nil {
				t.Fatal("incompatible measurements accepted")
			}
		})
	}
	a, b := c0Snapshot("00000000000000000000000000000001"), c0Snapshot("00000000000000000000000000000002")
	a.Settings.GoTestArgs = []string{"-run", "TestOne", "-tags=common"}
	b.Settings.GoTestArgs = []string{"-test.run=TestTwo", "-tags=common", "-parallel=2"}
	a.Settings.GOFLAGS = "-run=TestOne -tags=common"
	b.Settings.GOFLAGS = "-tags=common -run=TestTwo"
	a.Settings.Strict = true
	b.Settings.FailUnder = map[string]float64{"statement": 100}
	if _, err := Analyze(t.Context(), []Result{a, b}, mcdc.AnalysisBudget{}); err != nil {
		t.Fatalf("compatible test shards rejected: %v", err)
	}
}

func TestDerivedInputsRemainAuthoritativeAndIdentitiesAreChecked(t *testing.T) {
	t.Parallel()
	a, b := c0Snapshot("00000000000000000000000000000001"), c0Snapshot("00000000000000000000000000000002")
	merged, err := Analyze(t.Context(), []Result{a, b}, mcdc.AnalysisBudget{})
	if err != nil {
		t.Fatal(err)
	}
	// Even a stale derived summary cannot authorize new evidence on reanalysis.
	merged.Report.Summary.Statement.Covered = 999
	result, err := Analyze(t.Context(), []Result{merged}, mcdc.AnalysisBudget{})
	if err != nil || result.Report.Summary.Statement.Covered != 1 {
		t.Fatalf("derived summary became evidence: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*Result)
	}{
		{"changed input", func(r *Result) { r.Inputs[0].Settings.Strict = !r.Inputs[0].Settings.Strict }},
		{"missing origin", func(r *Result) { r.Report.ContributingRuns = nil }},
		{"duplicate input", func(r *Result) {
			r.Inputs = append(r.Inputs, r.Inputs[0])
			r.Report.ContributingRuns = append(r.Report.ContributingRuns, r.Report.ContributingRuns[0])
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, _ := json.Marshal(result)
			var copy Result
			json.Unmarshal(encoded, &copy)
			test.mutate(&copy)
			if _, err := Encode(copy); err == nil {
				t.Fatal("invalid analysis provenance accepted")
			}
		})
	}
	// Same invocation with a different export is not an independent run.
	b.Settings.InvocationID = a.Settings.InvocationID
	b.Settings.Strict = true
	if _, err := Analyze(t.Context(), []Result{a, b}, mcdc.AnalysisBudget{}); err == nil {
		t.Fatal("conflicting invocation accepted")
	}
	if result.Report.Summary.Statement.Total != 1 {
		t.Fatal("unexpected denominator")
	}
}
