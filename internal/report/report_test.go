package report_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/internal/backend"
	"github.com/shrydev2020/gomcdc/internal/c0"
	"github.com/shrydev2020/gomcdc/internal/config"
	cover "github.com/shrydev2020/gomcdc/internal/coverage"
	"github.com/shrydev2020/gomcdc/internal/report"
)

func TestSchemaVersionIsCurrent(t *testing.T) {
	t.Parallel()
	if report.SchemaVersion != "2.0" {
		t.Fatalf("SchemaVersion = %q, want schema 2.0", report.SchemaVersion)
	}
}

func TestBuildWeightedAggregationAndMergesC0Function(t *testing.T) {
	t.Parallel()

	input := weightedInput()
	built := report.Build(input)
	assertMetric(t, "module statement", built.Summary.Statement, true, 3, 4, 75, 0, 0, 0, 0)
	assertMetric(t, "module function", built.Summary.Function, true, 2, 2, 100, 0, 0, 0, 0)
	assertMetric(t, "module decision", built.Summary.Decision, true, 4, 6, 66.67, 0, 0, 0, 0)
	if got, want := packagePaths(built), []string{"example.com/m/a", "example.com/m/b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("packages = %v, want %v", got, want)
	}
	assertMetric(t, "package a decision", built.Packages[0].Summary.Decision, true, 2, 4, 50, 0, 0, 0, 0)
	assertMetric(t, "package b decision", built.Packages[1].Summary.Decision, true, 2, 2, 100, 0, 0, 0, 0)

	function := built.Packages[0].Files[0].Functions[0]
	if function.Name != "Check" || len(function.Statements) != 2 || len(function.Decisions) != 2 {
		t.Fatalf("C0 and decisions did not merge into Check: %#v", function)
	}
	assertMetric(t, "merged function statement", function.Summary.Statement, true, 1, 2, 50, 0, 0, 0, 0)
	assertMetric(t, "merged function decision", function.Summary.Decision, true, 2, 4, 50, 0, 0, 0, 0)
}

func TestBuildLeavesHTMLSourceProjectionToWriteHTML(t *testing.T) {
	t.Parallel()
	input := report.Input{
		ModulePath: "example.com/m",
		SourceFiles: []report.SourceFileInput{{
			PackagePath: "example.com/m/p",
			Path:        "p.go",
			Source:      []byte("package p\n"),
		}},
		PackageStatuses: map[string]string{"example.com/m/p": "passed"},
	}
	built := report.Build(input)
	if source := built.Packages[0].Files[0].Source; source != nil {
		t.Fatal("Build attached an HTML-only source projection")
	}
	var html bytes.Buffer
	projected := report.WithSourceViews(built, input.SourceFiles)
	if source := built.Packages[0].Files[0].Source; source != nil {
		t.Fatal("WithSourceViews mutated its input report")
	}
	cleared := report.WithSourceViews(projected, nil)
	if source := cleared.Packages[0].Files[0].Source; source != nil {
		t.Fatal("WithSourceViews retained a stale source projection")
	}
	if err := report.WriteHTMLReport(&html, projected); err != nil {
		t.Fatalf("WriteHTMLReport: %v", err)
	}
	if !strings.Contains(html.String(), "package p") {
		t.Fatal("WriteHTML omitted the source projection")
	}
	for _, required := range []string{"name=\"source-view-0-0\"", "source-view-combined", "source-view-mcdc", "source-view-clause"} {
		if !strings.Contains(html.String(), required) {
			t.Errorf("WriteHTML missing source view %q", required)
		}
	}
	if strings.Contains(html.String(), "source-view-$pi-$fi") {
		t.Fatal("WriteHTML leaked unexpanded template variables into radio names")
	}
}

func TestWithRunResultsAndErrorsMatchesRebuildAndCopiesErrors(t *testing.T) {
	t.Parallel()

	input := weightedInput()
	built := report.Build(input)
	results := report.RunResults{
		Test:        report.ResultPassed,
		Measurement: report.ResultPassed,
		Integrity:   report.ResultPassed,
		Strict:      report.ResultFailed,
		Threshold:   report.ResultPassed,
	}
	errors := []report.ReportError{{
		Phase: "validation", Code: "strict-coverage-gap", Message: "requested coverage contains a gap",
	}}

	updated := report.WithRunResultsAndErrors(built, results, errors)
	wantInput := input
	wantInput.Results = results
	wantInput.Errors = errors
	want := report.Build(wantInput)
	updatedJSON, err := report.RenderJSONReport(updated)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := report.RenderJSONReport(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(updatedJSON, wantJSON) {
		t.Fatalf("run-results update differs from rebuilding coverage:\nupdated:\n%s\nrebuilt:\n%s", updatedJSON, wantJSON)
	}

	errors[0].Message = "mutated by caller"
	if updated.Errors[0].Message == errors[0].Message {
		t.Fatal("updated report retained caller mutation authority over errors")
	}
	if !reflect.DeepEqual(built.Summary, updated.Summary) || !reflect.DeepEqual(built.Packages, updated.Packages) {
		t.Fatal("run-results update changed the coverage hierarchy")
	}
}

func TestJSONAlwaysCarriesTypedErrorsWithoutSourceSnapshot(t *testing.T) {
	t.Parallel()
	input := report.Input{
		ModulePath: "example.com/m",
		SourceFiles: []report.SourceFileInput{{
			PackagePath: "example.com/m/p", Path: "p.go", Source: []byte("private source sentinel"),
		}},
		Errors: []report.ReportError{{
			Phase: "analysis", Code: "source-analysis-failed", Message: "source analysis did not complete",
			Package: "example.com/m/p", Path: "p.go",
		}},
	}
	encoded, err := report.RenderJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, required := range []string{`"errors": [`, `"phase": "analysis"`, `"path": "p.go"`} {
		if !strings.Contains(text, required) {
			t.Fatalf("JSON missing %q:\n%s", required, text)
		}
	}
	if strings.Contains(text, "private source sentinel") {
		t.Fatal("JSON leaked the HTML source snapshot")
	}
	empty, err := report.RenderJSON(report.Input{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(empty), `"errors": []`) {
		t.Fatalf("empty JSON report omitted its errors array:\n%s", empty)
	}
}

func TestJSONSchemaUsesExactSpecificationKeys(t *testing.T) {
	t.Parallel()
	encoded, err := report.RenderJSON(report.Input{
		Coverage: config.AllCoverage(),
		Errors: []report.ReportError{{
			Phase: "test", Code: "fixture", Message: "fixture", Package: "example.com/m/p", Path: "p.go",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &root); err != nil {
		t.Fatal(err)
	}
	assertJSONKeys(t, "root", root, []string{
		"schemaVersion", "toolVersion", "module", "run", "measurementMode", "measurements",
		"producerOutcomes", "capabilities", "backendCapabilities", "instrumentationCoverage",
		"summary", "packages", "errors",
	})
	var run map[string]json.RawMessage
	if err := json.Unmarshal(root["run"], &run); err != nil {
		t.Fatal(err)
	}
	assertJSONKeys(t, "run", run, []string{"status", "failureKind", "complete", "results"})
	var results map[string]json.RawMessage
	if err := json.Unmarshal(run["results"], &results); err != nil {
		t.Fatal(err)
	}
	assertJSONKeys(t, "run.results", results, []string{"test", "measurement", "integrity", "strict", "threshold"})
	var resultValues map[string]report.ResultStatus
	if err := json.Unmarshal(run["results"], &resultValues); err != nil {
		t.Fatal(err)
	}
	if resultValues["test"] != report.ResultNotRun || resultValues["strict"] != report.ResultNotRequested {
		t.Fatalf("run.results defaults are outside the D28 enum: %#v", resultValues)
	}
	var summary map[string]json.RawMessage
	if err := json.Unmarshal(root["summary"], &summary); err != nil {
		t.Fatal(err)
	}
	assertJSONKeys(t, "summary", summary, []string{
		"statement", "function", "decision", "switchClauseBody", "typeSwitchClauseBody",
		"selectClauseBody", "switchClauseSelection", "typeSwitchClauseSelection",
		"condition", "mcdcUnique", "mcdcMasking",
	})
	var metric map[string]json.RawMessage
	if err := json.Unmarshal(summary["decision"], &metric); err != nil {
		t.Fatal(err)
	}
	assertJSONKeys(t, "summary.decision", metric, []string{
		"enabled", "covered", "total", "percentage", "unsupported", "unknown", "infeasible", "analysisIncomplete",
	})
	var instrumentation map[string]json.RawMessage
	if err := json.Unmarshal(root["instrumentationCoverage"], &instrumentation); err != nil {
		t.Fatal(err)
	}
	assertJSONKeys(t, "instrumentationCoverage", instrumentation, []string{"total", "metrics"})
	var instrumentationTotal map[string]json.RawMessage
	if err := json.Unmarshal(instrumentation["total"], &instrumentationTotal); err != nil {
		t.Fatal(err)
	}
	assertJSONKeys(t, "instrumentationCoverage.total", instrumentationTotal, []string{
		"discovered", "supported", "instrumented", "unsupported", "unknown", "percentage",
	})
	var errors []map[string]json.RawMessage
	if err := json.Unmarshal(root["errors"], &errors); err != nil {
		t.Fatal(err)
	}
	if len(errors) != 1 {
		t.Fatalf("errors length = %d, want 1", len(errors))
	}
	assertJSONKeys(t, "errors[0]", errors[0], []string{"phase", "code", "message", "package", "path"})
}

func assertJSONKeys(t *testing.T, name string, value map[string]json.RawMessage, want []string) {
	t.Helper()
	got := make([]string, 0, len(value))
	for key := range value {
		got = append(got, key)
	}
	slices.Sort(got)
	want = slices.Clone(want)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("%s keys = %v, want %v", name, got, want)
	}
}

func TestMCDCAnalysisIncompleteIsSeparateFromUnknownSupport(t *testing.T) {
	t.Parallel()
	decision := singleConditionDecision(99, "example.com/m/p", "p.go", "Malformed")
	decision.ExpressionTree = nil
	built := report.Build(report.Input{
		ModulePath: "example.com/m",
		Coverage:   config.CoverageSet{config.MetricMCDCMasking: true},
		Decisions:  []cover.DecisionMetadata{decision},
		PackageStatuses: map[string]string{
			"example.com/m/p": "passed",
		},
	})
	metric := built.Summary.MCDCMasking
	if metric.AnalysisIncomplete != 1 || metric.Unknown != 0 || metric.Total != 0 {
		t.Fatalf("MC/DC analysis-incomplete summary = %#v", metric)
	}
}

func TestWriteHTMLReportsSourceMappingDiagnostics(t *testing.T) {
	t.Parallel()
	location := cover.SourceLocation{File: "p.go", Start: cover.Position{Line: 9, Column: 1}, End: cover.Position{Line: 9, Column: 2}}
	input := report.Input{
		ModulePath: "example.com/m",
		SourceFiles: []report.SourceFileInput{{
			PackagePath: "example.com/m/p", Path: "p.go", Source: []byte("package p\n"),
		}},
		Decisions:       []cover.DecisionMetadata{{ID: 1, Package: "example.com/m/p", Function: "Check", Location: location, Expression: "a"}},
		PackageStatuses: map[string]string{"example.com/m/p": "passed"},
	}
	var html bytes.Buffer
	if err := report.WriteHTML(&html, input); err != nil {
		t.Fatalf("WriteHTML: %v", err)
	}
	for _, required := range []string{"Source mapping diagnostics", "decision 0x0000000000000001", "cannot be mapped"} {
		if !strings.Contains(html.String(), required) {
			t.Errorf("HTML missing mapping diagnostic %q", required)
		}
	}
}

func TestANDShortCircuitDistinguishesUniqueAndMasking(t *testing.T) {
	t.Parallel()

	decision := andDecision(10, "example.com/m/p", "p.go", "Allow")
	input := report.Input{
		ModulePath: "example.com/m",
		Coverage:   config.AllCoverage(),
		RunStatus:  cover.RunPassed,
		Complete:   true,
		Decisions:  []cover.DecisionMetadata{decision},
		Evaluations: []cover.DecisionEvaluation{
			completedEvaluation(10, 1, false, cover.ConditionFalse, cover.ConditionNotEvaluated),
			completedEvaluation(10, 2, true, cover.ConditionTrue, cover.ConditionTrue),
			completedEvaluation(10, 3, false, cover.ConditionTrue, cover.ConditionFalse),
		},
	}
	built := report.Build(input)
	got := built.Packages[0].Files[0].Functions[0].Decisions[0]
	assertMetric(t, "unique", got.MCDCUnique.Metric, true, 1, 1, 100, 0, 0, 0, 1)
	assertMetric(t, "masking", got.MCDCMasking.Metric, true, 2, 2, 100, 0, 0, 0, 0)
	if got.MCDCUnique.Conditions[0].Status != string(cover.CoverageInfeasible) ||
		got.MCDCMasking.Conditions[0].Status != string(cover.CoverageCovered) {
		t.Fatalf("condition 0 statuses unique=%q masking=%q", got.MCDCUnique.Conditions[0].Status, got.MCDCMasking.Conditions[0].Status)
	}
	if got.MCDCMasking.Conditions[0].Witness == nil {
		t.Fatal("masking condition 0 has no witness")
	}
	witness := got.MCDCMasking.Conditions[0].Witness
	if got, want := witness.FirstCompletion, []string{"false", "true"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first completion = %v, want %v", got, want)
	}
	if got, want := witness.SecondCompletion, []string{"true", "true"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second completion = %v, want %v", got, want)
	}
	if got.Conditions[1].NotEvaluated != 1 || !got.Conditions[1].True || !got.Conditions[1].False {
		t.Fatalf("condition evidence = %#v", got.Conditions[1])
	}
	if got.Evaluations[0].EvaluationID != "0x0000000000000001" || got.Evaluations[0].RunID != "run" || got.Evaluations[0].ProcessID != 42 {
		t.Fatalf("evaluation provenance/ID = %#v", got.Evaluations[0])
	}

	text := report.RenderText(input)
	if !strings.HasPrefix(text, "gomcdc unknown report schema 2.0\n") {
		t.Fatalf("text report does not distinguish tool and schema identities:\n%s", text)
	}
	for _, required := range []string{"Unique-Cause MC/DC", "Masking MC/DC", "witness=", "[false,not-evaluated] -> false", "[true,true] -> true", "completions=([false,true] [true,true])"} {
		if !strings.Contains(text, required) {
			t.Fatalf("text report missing %q:\n%s", required, text)
		}
	}
}

func TestConditionlessSwitchReportsSkippedDecisionAsNotEvaluated(t *testing.T) {
	t.Parallel()

	first := singleConditionDecision(40, "example.com/m/p", "p.go", "Choose")
	first.Kind = cover.DecisionSwitchCase
	first.Expression = "a"
	second := singleConditionDecision(41, "example.com/m/p", "p.go", "Choose")
	second.Kind = cover.DecisionSwitchCase
	second.Expression = "b"
	second.Location.Start.Line, second.Location.End.Line = 11, 11
	second.Conditions[0].Expression = "b"
	second.Conditions[0].Location = location("p.go", 11)

	input := report.Input{
		ModulePath: "example.com/m",
		Coverage:   config.AllCoverage(),
		RunStatus:  cover.RunPassed,
		Complete:   true,
		Decisions:  []cover.DecisionMetadata{first, second},
		Evaluations: []cover.DecisionEvaluation{
			completedEvaluation(40, 1, true, cover.ConditionTrue),
		},
		NotEvaluatedDecisions: []cover.DecisionNotEvaluatedObservation{{
			DecisionID: 41, CauseDecisionID: 40, CauseEvaluationID: 1,
			RunID: "run", PackagePath: "example.com/m/p", ProcessID: 42,
		}},
	}
	built := report.Build(input)
	decisions := built.Packages[0].Files[0].Functions[0].Decisions
	if len(decisions) != 2 {
		t.Fatalf("decisions = %#v", decisions)
	}
	target := decisions[1]
	if target.DecisionID != "0x0000000000000029" || target.NotEvaluated != 1 || target.DecisionCoverage.True || target.DecisionCoverage.False {
		t.Fatalf("skipped decision = %#v", target)
	}
	if got := target.Conditions[0].NotEvaluated; got != 1 {
		t.Fatalf("skipped condition not-evaluated = %d, want 1", got)
	}
	if text := report.RenderText(input); !strings.Contains(text, "Decision Coverage: true=false false=false not-evaluated=1") {
		t.Fatalf("text report omitted skipped decision evidence:\n%s", text)
	}
}

func TestAbortedEvaluationsAreDiagnosticAndDoNotRemoveObligations(t *testing.T) {
	t.Parallel()

	decision := singleConditionDecision(20, "example.com/m/p", "p.go", "Check")
	input := report.Input{
		ModulePath: "example.com/m", Coverage: config.AllCoverage(), RunStatus: cover.RunFailed,
		Decisions: []cover.DecisionMetadata{decision},
		Evaluations: []cover.DecisionEvaluation{{
			DecisionID: 20, EvaluationID: 9, RunID: "run", PackagePath: "example.com/m/p",
			ProcessID: 7, TestID: "TestPanic", Conditions: []cover.ConditionState{cover.ConditionTrue},
			Status: cover.EvaluationAborted,
		}},
	}
	built := report.Build(input)
	decisionReport := built.Packages[0].Files[0].Functions[0].Decisions[0]
	assertMetric(t, "decision", decisionReport.Summary.Decision, true, 0, 2, 0, 0, 0, 0, 0)
	assertMetric(t, "condition", decisionReport.Summary.Condition, true, 0, 2, 0, 0, 0, 0, 0)
	assertMetric(t, "unique", decisionReport.Summary.MCDCUnique, true, 0, 1, 0, 0, 0, 0, 0)
	assertMetric(t, "masking", decisionReport.Summary.MCDCMasking, true, 0, 1, 0, 0, 0, 0, 0)
	if decisionReport.DecisionCoverage.True || decisionReport.Conditions[0].True || decisionReport.Conditions[0].NotEvaluated != 0 {
		t.Fatalf("aborted evaluation established evidence: %#v", decisionReport)
	}
	if decisionReport.Evaluations[0].Status != "aborted" {
		t.Fatalf("evaluation status = %q", decisionReport.Evaluations[0].Status)
	}
}

func TestUnknownAndUnsupportedAreNotInDenominator(t *testing.T) {
	t.Parallel()

	failedDecision := singleConditionDecision(30, "example.com/m/failed", "failed.go", "Failed")
	unsupportedDecision := singleConditionDecision(31, "example.com/m/passed", "passed.go", "Unsupported")
	unsupportedDecision.ExpressionTree = &cover.BooleanExpression{Kind: "xor"}
	input := report.Input{
		ModulePath: "example.com/m", Coverage: config.AllCoverage(), RunStatus: cover.RunFailed,
		Decisions: []cover.DecisionMetadata{failedDecision, unsupportedDecision},
		Clauses: []cover.ClauseMetadata{
			{ID: 100, Package: "example.com/m/failed", Function: "Failed", Kind: cover.ClauseSelect, Role: cover.ClauseCase, Location: location("failed.go", 8)},
			{ID: 101, Package: "example.com/m/passed", Function: "Unsupported", Kind: "future-clause", Role: cover.ClauseCase, Location: location("passed.go", 8)},
		},
		PackageStatuses: map[string]string{
			"example.com/m/failed": "started",
			"example.com/m/passed": "passed",
		},
	}
	built := report.Build(input)
	failed := built.Packages[0]
	if failed.Path != "example.com/m/failed" || failed.Evidence {
		t.Fatalf("failed package = %#v", failed)
	}
	assertMetric(t, "unknown decision", failed.Summary.Decision, true, 0, 0, 0, 0, 2, 0, 0)
	assertMetric(t, "unknown condition", failed.Summary.Condition, true, 0, 0, 0, 0, 2, 0, 0)
	assertMetric(t, "unknown unique", failed.Summary.MCDCUnique, true, 0, 0, 0, 0, 1, 0, 0)
	assertMetric(t, "unknown clause", failed.Summary.SelectClauseBody, true, 0, 0, 0, 0, 1, 0, 0)

	passed := built.Packages[1]
	assertMetric(t, "passed absent decision", passed.Summary.Decision, true, 0, 2, 0, 0, 0, 0, 0)
	assertMetric(t, "unsupported masking", passed.Summary.MCDCMasking, true, 0, 0, 0, 1, 0, 0, 0)
	assertMetric(t, "unclassified future clause", passed.Summary.SelectClauseBody, true, 0, 0, 0, 0, 0, 0, 0)
}

func TestStandardCoverEvidenceDoesNotMakeFailedASTMeasurementKnown(t *testing.T) {
	t.Parallel()

	decision := singleConditionDecision(35, "example.com/m/p", "p.go", "Check")
	input := report.Input{
		ModulePath: "example.com/m", Coverage: config.AllCoverage(), RunStatus: cover.RunFailed,
		Decisions:       []cover.DecisionMetadata{decision},
		PackageStatuses: map[string]string{"example.com/m/p": "build-failed"},
		C0: &c0.Report{
			Mode: c0.ModeSet, ModulePath: "example.com/m",
			Packages: []c0.PackageReport{{
				Path: "example.com/m/p", Evidence: true,
				Files: []c0.FileReport{{
					Path: "p.go", Evidence: true,
					Functions: []c0.FunctionReport{{
						Name: "Check", Position: c0Range(9, 1, 12, 2), Evidence: true, CompleteEvidence: true,
						Summary: c0.Summary{Functions: c0.Counts{Covered: 1, Total: 1}},
						Blocks:  []c0.StatementBlock{c0Block(10, 1, 11, 2, 1, 1)},
					}},
				}},
			}},
		},
	}
	built := report.Build(input)
	if !built.Packages[0].Evidence {
		t.Fatal("combined package evidence should retain the standard-cover run")
	}
	decisionReport := built.Packages[0].Files[0].Functions[0].Decisions[0]
	assertMetric(t, "AST decision with only C0 evidence", decisionReport.Summary.Decision, true, 0, 0, 0, 0, 2, 0, 0)
	assertMetric(t, "AST condition with only C0 evidence", decisionReport.Summary.Condition, true, 0, 0, 0, 0, 2, 0, 0)

	input.ASTPackageStatuses = map[string]string{"example.com/m/p": "passed"}
	built = report.Build(input)
	decisionReport = built.Packages[0].Files[0].Functions[0].Decisions[0]
	assertMetric(t, "AST pass remains distinct from standard build failure", decisionReport.Summary.Decision, true, 0, 2, 0, 0, 0, 0, 0)
}

func TestC0InventoryWithoutProfileEvidenceIsUnknown(t *testing.T) {
	t.Parallel()

	staticFunction := c0.FunctionReport{
		Name:     "Broken",
		Position: c0Range(3, 1, 6, 2),
		Summary:  c0.Summary{Functions: c0.Counts{Total: 1}},
		Blocks: []c0.StatementBlock{{
			Position:   c0Range(4, 2, 5, 8),
			Statements: 2,
			Summary: c0.Summary{
				Statements: c0.Counts{Total: 2},
				Blocks:     c0.Counts{Total: 1},
			},
		}},
	}
	input := report.Input{
		ModulePath: "example.com/m",
		Coverage:   config.AllCoverage(),
		RunStatus:  cover.RunFailed,
		Decisions: []cover.DecisionMetadata{
			singleConditionDecision(90, "example.com/m/broken", "broken.go", "Broken"),
		},
		PackageStatuses: map[string]string{"example.com/m/broken": "failed"},
		C0: &c0.Report{Packages: []c0.PackageReport{{
			Path:  "example.com/m/broken",
			Files: []c0.FileReport{{Path: "broken.go", Functions: []c0.FunctionReport{staticFunction}}},
		}}},
	}

	built := report.Build(input)
	if built.Packages[0].Evidence {
		t.Fatalf("inventory-only package Evidence = true")
	}
	assertMetric(t, "static statement", built.Summary.Statement, true, 0, 0, 0, 0, 2, 0, 0)
	assertMetric(t, "static function", built.Summary.Function, true, 0, 0, 0, 0, 1, 0, 0)
	assertMetric(t, "static AST decision", built.Summary.Decision, true, 0, 0, 0, 0, 2, 0, 0)

	profileBacked := staticFunction
	profileBacked.Evidence = true
	profileBacked.CompleteEvidence = true
	profileBacked.Blocks = slices.Clone(staticFunction.Blocks)
	for index := range profileBacked.Blocks {
		profileBacked.Blocks[index].Evidence = true
	}
	input.RunStatus = cover.RunPassed
	input.PackageStatuses["example.com/m/broken"] = "passed"
	input.Decisions = nil
	input.C0.Packages[0].Evidence = true
	input.C0.Packages[0].Files[0].Evidence = true
	input.C0.Packages[0].Files[0].Functions[0] = profileBacked
	built = report.Build(input)
	assertMetric(t, "profile-backed zero-count statement", built.Summary.Statement, true, 0, 2, 0, 0, 0, 0, 0)
	assertMetric(t, "profile-backed zero-count function", built.Summary.Function, true, 0, 1, 0, 0, 0, 0, 0)
}

func TestProducerIntegrityFailureForcesSurvivingEvidenceToUnknown(t *testing.T) {
	t.Parallel()

	decision := andDecision(101, "example.com/m/p", "p.go", "Allow")
	input := report.Input{
		ModulePath:  "example.com/m",
		Coverage:    config.AllCoverage(),
		RunStatus:   cover.RunPassed,
		Complete:    false,
		Decisions:   []cover.DecisionMetadata{decision},
		Evaluations: []cover.DecisionEvaluation{completedEvaluation(101, 1, true, cover.ConditionTrue, cover.ConditionTrue)},
		ProducerOutcomes: []report.ProducerOutcome{
			{Producer: report.ProducerASTRuntime, Usability: report.ProducerUsabilityRejected},
			{Producer: report.ProducerGoCover, Usability: report.ProducerUsabilityRejected},
		},
		PackageStatuses:    map[string]string{"example.com/m/p": "passed"},
		ASTPackageStatuses: map[string]string{"example.com/m/p": "passed"},
		C0: &c0.Report{Packages: []c0.PackageReport{{
			Path: "example.com/m/p", Evidence: true,
			Files: []c0.FileReport{{Path: "p.go", Evidence: true, Functions: []c0.FunctionReport{{
				Name: "Allow", Position: c0Range(3, 1, 8, 2), Evidence: true, CompleteEvidence: true,
				Summary: c0.Summary{Functions: c0.Counts{Covered: 1, Total: 1}},
				Blocks:  []c0.StatementBlock{c0Block(4, 1, 5, 2, 1, 1)},
			}}}},
		}}},
	}

	built := report.Build(input)
	assertMetric(t, "corrupt C0 statement", built.Summary.Statement, true, 0, 0, 0, 0, 1, 0, 0)
	assertMetric(t, "corrupt C0 function", built.Summary.Function, true, 0, 0, 0, 0, 1, 0, 0)
	assertMetric(t, "corrupt AST decision", built.Summary.Decision, true, 0, 0, 0, 0, 2, 0, 0)
	assertMetric(t, "corrupt AST condition", built.Summary.Condition, true, 0, 0, 0, 0, 4, 0, 0)
	assertMetric(t, "corrupt AST unique", built.Summary.MCDCUnique, true, 0, 0, 0, 0, 2, 0, 0)
	assertMetric(t, "corrupt AST masking", built.Summary.MCDCMasking, true, 0, 0, 0, 0, 2, 0, 0)
	if built.Packages[0].Evidence {
		t.Fatal("integrity-damaged package must not claim valid evidence")
	}
}

func TestProducerUsabilityGatesBodyAndSelectionIndependently(t *testing.T) {
	t.Parallel()

	clause := cover.ClauseMetadata{
		ID: 1, SwitchID: 2, Package: "example.com/m/p", Function: "Choose",
		Kind: cover.ClauseExpressionSwitch, Role: cover.ClauseCase,
		Expressions: []string{"1"}, Location: location("p.go", 4),
	}
	base := report.Input{
		Coverage: config.AllCoverage(), Clauses: []cover.ClauseMetadata{clause},
		ClauseObservations: []cover.ClauseObservation{
			{ClauseID: 1, Event: cover.ClauseBodyExecution},
			{ClauseID: 1, SwitchID: 2, Event: cover.ClauseDirectSelection, AlternativeKnown: true},
		},
		PackageStatuses:    map[string]string{"example.com/m/p": "passed"},
		ASTPackageStatuses: map[string]string{"example.com/m/p": "passed"},
	}

	bodyRejected := base
	bodyRejected.ProducerOutcomes = []report.ProducerOutcome{
		{Producer: report.ProducerASTRuntime, Usability: report.ProducerUsabilityRejected},
		{Producer: report.ProducerCompilerSelection, Usability: report.ProducerUsabilityAccepted},
	}
	got := report.Build(bodyRejected).Packages[0].Files[0].Functions[0].Clauses[0]
	if got.BodyCoverage.Unknown != 1 || got.SelectionCoverage.Covered != 1 || got.SelectionCoverage.Total != 1 {
		t.Fatalf("body rejection leaked into compiler evidence: %#v", got)
	}

	selectionRejected := base
	selectionRejected.ProducerOutcomes = []report.ProducerOutcome{
		{Producer: report.ProducerASTRuntime, Usability: report.ProducerUsabilityAccepted},
		{Producer: report.ProducerCompilerSelection, Usability: report.ProducerUsabilityRejected},
	}
	got = report.Build(selectionRejected).Packages[0].Files[0].Functions[0].Clauses[0]
	if got.BodyCoverage.Covered != 1 || got.BodyCoverage.Total != 1 || got.SelectionCoverage.Unknown != 1 {
		t.Fatalf("compiler rejection leaked into AST body evidence: %#v", got)
	}
}

func TestClauseCoverageKeepsBodyAndSelectionEvidenceDistinct(t *testing.T) {
	t.Parallel()

	clauses := []cover.ClauseMetadata{
		{ID: 1, Package: "p", Function: "Switch", Kind: cover.ClauseExpressionSwitch, Role: cover.ClauseCase, Index: 0, Location: location("p.go", 10)},
		{ID: 2, Package: "p", Function: "Switch", Kind: cover.ClauseSelect, Role: cover.ClauseDefault, Index: 1, Location: location("p.go", 20)},
	}
	input := report.Input{
		ModulePath: "m", Coverage: config.AllCoverage(), RunStatus: cover.RunPassed, Complete: true,
		Clauses:   clauses,
		NoMatches: []cover.NoMatchMetadata{{SwitchID: 3, Package: "p", Function: "Switch", Kind: cover.ClauseExpressionSwitch, Location: location("p.go", 30)}},
		ClauseObservations: []cover.ClauseObservation{
			{ClauseID: 1, Event: cover.ClauseBodyExecution},
			{ClauseID: 2, Event: cover.ClauseDirectSelection},
			{ClauseID: 2, Event: cover.ClauseBodyExecution},
		},
	}
	built := report.Build(input)
	got := built.Packages[0].Files[0].Functions[0].Clauses
	assertMetric(t, "switch clause body", built.Summary.SwitchClauseBody, true, 1, 1, 100, 0, 0, 0, 0)
	assertMetric(t, "select clause body", built.Summary.SelectClauseBody, true, 1, 1, 100, 0, 0, 0, 0)
	assertMetric(t, "switch clause selection", built.Summary.SwitchClauseSelection, true, 0, 2, 0, 0, 0, 0, 0)
	if got[0].Role != cover.ClauseCase || got[0].BodyExecutions != 1 || got[0].BodyCoverage.Covered != 1 {
		t.Fatalf("fallthrough case = %#v", got[0])
	}
	if got[1].Role != cover.ClauseDefault {
		t.Fatalf("clause roles/order = %#v", got)
	}
	if text := report.RenderText(input); !strings.Contains(text, "Select Clause Body Coverage") ||
		!strings.Contains(text, "direct-selections=0 selected-alternatives=[]") {
		t.Fatalf("text does not label select coverage:\n%s", text)
	}
}

func TestBuildDoesNotInventMissingRunProvenance(t *testing.T) {
	t.Parallel()
	built := report.Build(report.Input{})
	if built.MeasurementMode != "" || built.Run.FailureKind != "" {
		t.Fatalf("missing provenance was inferred: mode=%q failureKind=%q", built.MeasurementMode, built.Run.FailureKind)
	}
	if built.Run.Results != (report.RunResults{
		Test: report.ResultNotRun, Measurement: report.ResultNotRun, Integrity: report.ResultNotRun,
		Strict: report.ResultNotRequested, Threshold: report.ResultNotRequested,
	}) {
		t.Fatalf("missing result axes were not normalized: %#v", built.Run.Results)
	}
}

func TestZeroMetricsAndDisabledMetricsStayPresent(t *testing.T) {
	t.Parallel()

	input := report.Input{ModulePath: "example.com/empty", Coverage: config.CoverageSet{}, RunStatus: cover.RunPassed, Complete: true}
	built := report.Build(input)
	for name, metric := range metrics(built.Summary) {
		assertMetric(t, name, metric, false, 0, 0, 0, 0, 0, 0, 0)
	}
	jsonValue, err := report.RenderJSON(input)
	if err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	if !bytes.Contains(jsonValue, []byte(`"percentage": null`)) ||
		bytes.Contains(jsonValue, []byte(`"clause"`)) ||
		bytes.Contains(jsonValue, []byte(`"clauseBody"`)) ||
		!bytes.Contains(jsonValue, []byte(`"switchClauseSelection"`)) ||
		!bytes.Contains(jsonValue, []byte(`"mcdcMasking"`)) ||
		jsonValue[len(jsonValue)-1] != '\n' {
		t.Fatalf("zero JSON schema/newline mismatch:\n%s", jsonValue)
	}
	text := report.RenderText(input)
	for _, metric := range []string{
		"Statement Coverage", "Function Coverage", "Decision Coverage",
		"Switch Clause Body Coverage", "Type Switch Clause Body Coverage", "Select Clause Body Coverage",
		"Switch Clause Selection Coverage", "Type Switch Clause Selection Coverage",
		"Condition Coverage", "Unique-Cause MC/DC", "Masking MC/DC",
	} {
		if !strings.Contains(text, metric+": enabled=false n/a") {
			t.Fatalf("text missing disabled %s:\n%s", metric, text)
		}
	}
}

func TestDisabledMCDCReportsStayPresentWithoutAnalysis(t *testing.T) {
	t.Parallel()

	decision := andDecision(10, "example.com/m/p", "p.go", "Allow")
	built := report.Build(report.Input{
		ModulePath: "example.com/m",
		Coverage: config.CoverageSet{
			config.MetricDecision:  true,
			config.MetricCondition: true,
		},
		RunStatus: cover.RunPassed,
		Complete:  true,
		Decisions: []cover.DecisionMetadata{decision},
		Evaluations: []cover.DecisionEvaluation{
			completedEvaluation(10, 1, false, cover.ConditionFalse, cover.ConditionNotEvaluated),
			completedEvaluation(10, 2, true, cover.ConditionTrue, cover.ConditionTrue),
			completedEvaluation(10, 3, false, cover.ConditionTrue, cover.ConditionFalse),
		},
	})
	got := built.Packages[0].Files[0].Functions[0].Decisions[0]
	for name, analysis := range map[string]report.MCDCAnalysisReport{
		"unique":  got.MCDCUnique,
		"masking": got.MCDCMasking,
	} {
		if analysis.Enabled || analysis.Status != "disabled" {
			t.Errorf("%s disabled state = enabled %t, status %q", name, analysis.Enabled, analysis.Status)
		}
		if analysis.EvaluationsAnalyzed != 0 || analysis.AbortedEvaluations != 0 || analysis.InvalidEvaluations != 0 {
			t.Errorf("%s disabled analysis retained work counters: %#v", name, analysis)
		}
		if len(analysis.Conditions) != len(decision.Conditions) {
			t.Errorf("%s condition slots = %d, want %d", name, len(analysis.Conditions), len(decision.Conditions))
		}
		for _, condition := range analysis.Conditions {
			if condition.Status != "disabled" || condition.Witness != nil {
				t.Errorf("%s disabled condition = %#v", name, condition)
			}
		}
	}
}

func TestTextMetricUsesSpecificationCoverageForm(t *testing.T) {
	t.Parallel()

	percentage := 75.0
	text := report.RenderTextReport(report.Report{
		Summary: report.Summary{
			Statement: report.MetricSummary{
				Enabled: true, Covered: 3, Total: 4, Percentage: &percentage,
			},
		},
	})
	if !strings.Contains(text, "Statement Coverage: enabled=true 3 / 4 = 75.00%") {
		t.Fatalf("text does not use covered / total = percentage form:\n%s", text)
	}
}

func TestJSONReportNormalizesNilErrorsToArray(t *testing.T) {
	t.Parallel()

	encoded, err := report.RenderJSONReport(report.Report{})
	if err != nil {
		t.Fatalf("RenderJSONReport: %v", err)
	}
	if !bytes.Contains(encoded, []byte(`"errors": []`)) {
		t.Fatalf("errors must be a JSON array:\n%s", encoded)
	}
}

func TestInstrumentationCoverageAccountsForUnsupportedAndUnknownEntities(t *testing.T) {
	t.Parallel()

	supportedDecision := singleConditionDecision(201, "p", "p.go", "Supported")
	unknownDecision := singleConditionDecision(202, "p", "p.go", "Future")
	unknownDecision.Kind = "future-decision"
	built := report.Build(report.Input{
		ModulePath: "m",
		Coverage: config.CoverageSet{
			config.MetricDecision:              true,
			config.MetricSelectClauseBody:      true,
			config.MetricSwitchClauseSelection: true,
			config.MetricMCDCMasking:           true,
		},
		Decisions: []cover.DecisionMetadata{supportedDecision, unknownDecision},
		Clauses: []cover.ClauseMetadata{
			{ID: 1, Kind: cover.ClauseSelect, Role: cover.ClauseCase},
		},
		NoMatches:              []cover.NoMatchMetadata{{SwitchID: 2, Kind: cover.ClauseExpressionSwitch}},
		InstrumentationUnknown: 1,
	})

	if got := built.Capabilities.Status(backend.CapabilityDirectCaseSelection); got != backend.CapabilitySupported {
		t.Fatalf("direct selection capability = %q", got)
	}
	want := backend.InstrumentationCoverage{
		Discovered: 7, Supported: 4, Instrumented: 4, Unsupported: 0, Unknown: 3, Percentage: 57.14,
	}
	if got := built.Instrumentation.Total; got != want {
		t.Fatalf("instrumentation total = %#v, want %#v", got, want)
	}
	if !built.Instrumentation.HasGaps() {
		t.Fatal("strict instrumentation gaps were not detected")
	}
	text := report.RenderText(report.Input{
		Coverage: config.CoverageSet{config.MetricSelectClauseBody: true,
			config.MetricSwitchClauseSelection: true},
		NoMatches: []cover.NoMatchMetadata{{SwitchID: 2, Kind: cover.ClauseExpressionSwitch}},
	})
	for _, required := range []string{
		"Backend capabilities:",
		"directCaseSelection: supported",
		"Instrumentation coverage (requested metric entities): discovered=1 supported=1 instrumented=1 unsupported=0 unknown=0",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("text report missing %q:\n%s", required, text)
		}
	}
}

func TestRepeatedConditionOccurrencesRemainSupportedAcrossInstrumentationAndSummary(t *testing.T) {
	t.Parallel()
	decision := andDecision(301, "example.com/m/p", "p.go", "Repeated")
	decision.Expression = "a && a"
	decision.Conditions[1].Expression = "a"
	built := report.Build(report.Input{
		ModulePath: "example.com/m",
		Coverage:   config.CoverageSet{config.MetricMCDCMasking: true},
		Decisions:  []cover.DecisionMetadata{decision},
		Evaluations: []cover.DecisionEvaluation{
			completedEvaluation(301, 1, false, cover.ConditionFalse, cover.ConditionNotEvaluated),
			completedEvaluation(301, 2, true, cover.ConditionTrue, cover.ConditionTrue),
		},
		PackageStatuses:    map[string]string{"example.com/m/p": "passed"},
		ASTPackageStatuses: map[string]string{"example.com/m/p": "passed"},
	})
	var instrumentation backend.InstrumentationCoverage
	for _, metric := range built.Instrumentation.Metrics {
		if metric.Metric == string(config.MetricMCDCMasking) {
			instrumentation = metric.Coverage
		}
	}
	if instrumentation != (backend.InstrumentationCoverage{
		Discovered: 2, Supported: 2, Instrumented: 2, Percentage: 100,
	}) {
		t.Fatalf("repeated-occurrence instrumentation = %#v", instrumentation)
	}
	if built.Summary.MCDCMasking.Unknown != 0 || built.Summary.MCDCMasking.Total != 2 {
		t.Fatalf("repeated-occurrence summary = %#v", built.Summary.MCDCMasking)
	}
}

func TestJSONAndTextAreDeterministicAcrossInputOrder(t *testing.T) {
	t.Parallel()

	forward := weightedInput()
	backward := forward
	backward.Decisions = slices.Clone(forward.Decisions)
	slices.Reverse(backward.Decisions)
	backward.Evaluations = slices.Clone(forward.Evaluations)
	slices.Reverse(backward.Evaluations)
	backward.C0 = cloneReversedC0(forward.C0)

	forwardJSON, err := report.RenderJSON(forward)
	if err != nil {
		t.Fatalf("forward RenderJSON: %v", err)
	}
	backwardJSON, err := report.RenderJSON(backward)
	if err != nil {
		t.Fatalf("backward RenderJSON: %v", err)
	}
	if !bytes.Equal(forwardJSON, backwardJSON) {
		t.Fatalf("JSON depends on input order:\nforward:\n%s\nbackward:\n%s", forwardJSON, backwardJSON)
	}
	if got, want := report.RenderText(forward), report.RenderText(backward); got != want {
		t.Fatalf("text depends on input order:\nforward:\n%s\nbackward:\n%s", got, want)
	}
}

func weightedInput() report.Input {
	decisions := []cover.DecisionMetadata{
		{ID: 3, Package: "example.com/m/b", Function: "Loop", Kind: cover.DecisionFor, Location: cover.SourceLocation{File: "b.go", Start: cover.Position{Line: 8, Column: 2}, End: cover.Position{Line: 8, Column: 7}}, Expression: "i < 2"},
		{ID: 2, Package: "example.com/m/a", Function: "Check", Kind: cover.DecisionIf, Location: cover.SourceLocation{File: "a.go", Start: cover.Position{Line: 20, Column: 2}, End: cover.Position{Line: 20, Column: 8}}, Expression: "second"},
		{ID: 1, Package: "example.com/m/a", Function: "Check", Kind: cover.DecisionIf, Location: cover.SourceLocation{File: "a.go", Start: cover.Position{Line: 10, Column: 2}, End: cover.Position{Line: 10, Column: 7}}, Expression: "first"},
	}
	return report.Input{
		ModulePath: "example.com/m", Coverage: config.AllCoverage(), RunStatus: cover.RunPassed, Complete: true,
		Decisions: decisions,
		Evaluations: []cover.DecisionEvaluation{
			completedEvaluation(1, 1, true),
			completedEvaluation(2, 2, false),
			completedEvaluation(3, 3, true),
			completedEvaluation(3, 4, false),
		},
		C0: weightedC0(),
	}
}

func weightedC0() *c0.Report {
	return &c0.Report{
		Mode: c0.ModeSet, ModulePath: "example.com/m",
		Packages: []c0.PackageReport{
			{
				Path: "example.com/m/b", Evidence: true,
				Files: []c0.FileReport{{Path: "b.go", Evidence: true, Functions: []c0.FunctionReport{{
					Name: "Loop", Position: c0Range(7, 1, 12, 2), Evidence: true, Summary: c0.Summary{Functions: c0.Counts{Covered: 1, Total: 1}},
					Blocks: []c0.StatementBlock{c0Block(9, 1, 9, 6, 2, 1)},
				}}}},
			},
			{
				Path: "example.com/m/a", Evidence: true,
				Files: []c0.FileReport{{Path: "a.go", Evidence: true, Functions: []c0.FunctionReport{{
					Name: "Check", Position: c0Range(5, 1, 25, 2), Evidence: true, Summary: c0.Summary{Functions: c0.Counts{Covered: 1, Total: 1}},
					Blocks: []c0.StatementBlock{c0Block(6, 1, 6, 4, 1, 1), c0Block(7, 1, 7, 4, 1, 0)},
				}}}},
			},
		},
	}
}

func cloneReversedC0(value *c0.Report) *c0.Report {
	clone := *value
	clone.Packages = slices.Clone(value.Packages)
	slices.Reverse(clone.Packages)
	return &clone
}

func c0Block(startLine, startColumn, endLine, endColumn, statements int, count uint64) c0.StatementBlock {
	covered := 0
	coveredBlock := 0
	if count > 0 {
		covered = statements
		coveredBlock = 1
	}
	return c0.StatementBlock{
		Position: c0Range(startLine, startColumn, endLine, endColumn), Statements: statements, Count: count,
		Evidence: true,
		Summary:  c0.Summary{Statements: c0.Counts{Covered: covered, Total: statements}, Blocks: c0.Counts{Covered: coveredBlock, Total: 1}},
	}
}

func c0Range(startLine, startColumn, endLine, endColumn int) c0.SourceRange {
	return c0.SourceRange{Start: c0.Position{Line: startLine, Column: startColumn}, End: c0.Position{Line: endLine, Column: endColumn}}
}

func andDecision(id cover.DecisionID, packagePath, file, function string) cover.DecisionMetadata {
	decision := singleConditionDecision(id, packagePath, file, function)
	decision.Expression = "a && b"
	decision.Conditions = []cover.ConditionMetadata{
		{ID: cover.ConditionID(uint64(id)*10 + 1), Index: 0, Expression: "a", Location: location(file, 10)},
		{ID: cover.ConditionID(uint64(id)*10 + 2), Index: 1, Expression: "b", Location: location(file, 10)},
	}
	decision.ExpressionTree = cover.NewAndExpression(cover.NewConditionExpression(0), cover.NewConditionExpression(1))
	return decision
}

func singleConditionDecision(id cover.DecisionID, packagePath, file, function string) cover.DecisionMetadata {
	return cover.DecisionMetadata{
		ID: id, Package: packagePath, Function: function, Kind: cover.DecisionIf,
		Location: cover.SourceLocation{File: file, Start: cover.Position{Line: 10, Column: 2}, End: cover.Position{Line: 10, Column: 8}}, Expression: "a",
		Conditions: []cover.ConditionMetadata{{
			ID: cover.ConditionID(uint64(id)*10 + 1), Index: 0, Expression: "a", Location: location(file, 10),
		}},
		ExpressionTree: cover.NewConditionExpression(0),
	}
}

func completedEvaluation(id cover.DecisionID, evaluationID cover.EvaluationID, result bool, states ...cover.ConditionState) cover.DecisionEvaluation {
	return cover.DecisionEvaluation{
		DecisionID: id, EvaluationID: evaluationID, RunID: "run", PackagePath: decisionPackage(id),
		ProcessID: 42, TestID: "Test", Conditions: states, Result: result, Status: cover.EvaluationCompleted,
	}
}

func decisionPackage(id cover.DecisionID) string {
	switch id {
	case 1, 2:
		return "example.com/m/a"
	case 3:
		return "example.com/m/b"
	default:
		return "example.com/m/p"
	}
}

func location(file string, line int) cover.SourceLocation {
	return cover.SourceLocation{File: file, Start: cover.Position{Line: line, Column: 2}, End: cover.Position{Line: line, Column: 8}}
}

func packagePaths(value report.Report) []string {
	result := make([]string, 0, len(value.Packages))
	for _, packageReport := range value.Packages {
		result = append(result, packageReport.Path)
	}
	return result
}

func metrics(summary report.Summary) map[string]report.MetricSummary {
	return map[string]report.MetricSummary{
		"statement": summary.Statement, "function": summary.Function, "decision": summary.Decision,
		"switchClauseBody":     summary.SwitchClauseBody,
		"typeSwitchClauseBody": summary.TypeSwitchClauseBody, "selectClauseBody": summary.SelectClauseBody,
		"switchClauseSelection": summary.SwitchClauseSelection, "typeSwitchClauseSelection": summary.TypeSwitchClauseSelection,
		"condition":  summary.Condition,
		"mcdcUnique": summary.MCDCUnique, "mcdcMasking": summary.MCDCMasking,
	}
}

func assertMetric(
	t *testing.T,
	name string,
	got report.MetricSummary,
	enabled bool,
	covered, total int,
	percentage float64,
	unsupported, unknown, analysisIncomplete int, infeasible int,
) {
	t.Helper()
	want := report.MetricSummary{
		Enabled: enabled, Covered: covered, Total: total,
		Unsupported: unsupported, Unknown: unknown, Infeasible: infeasible,
		AnalysisIncomplete: analysisIncomplete,
	}
	if total > 0 {
		want.Percentage = &percentage
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %#v, want %#v", name, got, want)
	}
}
