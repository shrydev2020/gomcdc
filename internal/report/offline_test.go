package report_test

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/backend"
	"github.com/shrydev2020/gomcdc/v2/internal/config"
	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
	"github.com/shrydev2020/gomcdc/v2/internal/mcdc"
	"github.com/shrydev2020/gomcdc/v2/internal/report"
)

func offlineDecisionInput(runID string, result bool) report.Input {
	state := cover.ConditionFalse
	if result {
		state = cover.ConditionTrue
	}
	evaluation := completedEvaluation(1, 1, result, state)
	evaluation.RunID = runID
	return report.Input{
		ModulePath: "example.com/m", ToolVersion: "test", Coverage: config.AllCoverage(),
		Decisions: []cover.DecisionMetadata{singleConditionDecision(1, "example.com/m/a", "a.go", "Check")}, Evaluations: []cover.DecisionEvaluation{evaluation},
		RunStatus: cover.RunPassed, FailureKind: cover.RunFailureNone, Complete: true, MeasurementMode: report.MeasurementSingleRun,
		PackageStatuses:  map[string]string{"example.com/m/a": "passed"},
		ProducerOutcomes: []report.ProducerOutcome{{Producer: report.ProducerASTRuntime, Integrity: report.ProducerIntegrityValid, Completeness: report.ProducerCompletenessComplete, Mapping: report.ProducerMappingComplete, Usability: report.ProducerUsabilityAccepted}},
	}
}

func TestOfflineAnalysisKeepsRejectedEvidenceOutOfWitnesses(t *testing.T) {
	t.Parallel()
	accepted := report.Build(offlineDecisionInput("accepted", true))
	input := offlineDecisionInput("rejected", false)
	input.ProducerOutcomes[0].Usability = report.ProducerUsabilityRejected
	input.ProducerOutcomes[0].Integrity = report.ProducerIntegrityInvalid
	input.RunStatus = cover.RunFailed
	input.Complete = false
	input.Results.Integrity = report.ResultFailed
	rejected := report.Build(input)
	before, _ := json.Marshal([]report.Report{accepted, rejected})
	merged, err := report.AnalyzeSnapshots(t.Context(), []report.AnalysisSnapshot{{ID: "a", Report: accepted}, {ID: "b", Report: rejected}}, config.AllCoverage(), mcdc.AnalysisBudget{})
	if err != nil {
		t.Fatal(err)
	}
	if merged.Summary.Decision.Covered != 1 || merged.Summary.MCDCMasking.Covered != 0 || merged.Run.Complete || merged.Run.Results.Integrity != report.ResultFailed {
		t.Fatalf("rejected observations became evidence: %+v", merged)
	}
	d := merged.Packages[0].Files[0].Functions[0].Decisions[0]
	if len(d.Evaluations) != 1 || d.Evaluations[0].RunID != "accepted" {
		t.Fatal("rejected evaluations leaked into derived evidence")
	}
	after, _ := json.Marshal([]report.Report{accepted, rejected})
	if string(before) != string(after) {
		t.Fatal("analysis changed input snapshots")
	}
	unknown, err := report.AnalyzeSnapshots(t.Context(), []report.AnalysisSnapshot{{ID: "b", Report: rejected}}, config.AllCoverage(), mcdc.AnalysisBudget{})
	if err != nil || unknown.Summary.MCDCMasking.Unknown != 1 {
		t.Fatalf("unknown evidence promoted by reanalysis: %+v %v", unknown.Summary, err)
	}
}

func TestOfflineAnalysisRejectsChangedInventoriesAndInvalidVectors(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*report.Report)
	}{
		{"missing decision", func(r *report.Report) { r.Packages[0].Files[0].Functions[0].Decisions = nil }},
		{"changed expression", func(r *report.Report) { r.Packages[0].Files[0].Functions[0].Decisions[0].Expression = "!a" }},
		{"wrong condition layout", func(r *report.Report) { r.Packages[0].Files[0].Functions[0].Decisions[0].Conditions[0].Index = 1 }},
		{"impossible result", func(r *report.Report) { r.Packages[0].Files[0].Functions[0].Decisions[0].Evaluations[0].Result = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := report.Build(offlineDecisionInput("a", true))
			b := report.Build(offlineDecisionInput("b", false))
			test.mutate(&b)
			if _, err := report.AnalyzeSnapshots(t.Context(), []report.AnalysisSnapshot{{ID: "a", Report: a}, {ID: "b", Report: b}}, config.AllCoverage(), mcdc.AnalysisBudget{}); err == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
}

func TestOfflineUnionUsesPerObligationCoverageAndChecksCounterOverflow(t *testing.T) {
	t.Parallel()
	input := weightedInput()
	input.Decisions = nil
	input.Evaluations = nil
	a := report.Build(input)
	encoded, _ := json.Marshal(a)
	var b report.Report
	json.Unmarshal(encoded, &b)
	// A raw hit with unknown acceptance must not cover a known uncovered block.
	first := &a.Packages[0].Files[0].Functions[0].Statements[0]
	second := &b.Packages[0].Files[0].Functions[0].Statements[0]
	first.Count = 0
	first.Covered = false
	first.Metric.Covered = 0
	first.Metric.Percentage = new(float64(0))
	second.Count = 9
	second.Covered = true
	second.Metric = report.MetricSummary{Enabled: true, Unknown: second.Statements}
	merged, err := report.AnalyzeSnapshots(t.Context(), []report.AnalysisSnapshot{{ID: "a", Report: a}, {ID: "b", Report: b}}, input.Coverage, mcdc.AnalysisBudget{})
	if err != nil {
		t.Fatal(err)
	}
	block := merged.Packages[0].Files[0].Functions[0].Statements[0]
	if block.Count != 9 || block.Metric.Covered != 0 || block.Metric.Total != first.Metric.Total {
		t.Fatalf("raw counter became accepted evidence: %+v", block)
	}
	first.Count = math.MaxUint64
	second.Count = 0
	merged, err = report.AnalyzeSnapshots(t.Context(), []report.AnalysisSnapshot{{ID: "a", Report: a}, {ID: "b", Report: b}}, input.Coverage, mcdc.AnalysisBudget{})
	if err != nil || merged.Packages[0].Files[0].Functions[0].Statements[0].Count != math.MaxUint64 {
		t.Fatalf("exact uint64 counter limit: %v", err)
	}
	second.Count = 1
	if _, err := report.AnalyzeSnapshots(t.Context(), []report.AnalysisSnapshot{{ID: "a", Report: a}, {ID: "b", Report: b}}, input.Coverage, mcdc.AnalysisBudget{}); err == nil {
		t.Fatal("counter overflow accepted")
	}
}

func TestOfflineAnalysisSignedCounterBoundaries(t *testing.T) {
	t.Parallel()
	for _, counter := range []struct {
		name, diagnostic string
		field            func(*report.FunctionReport) *int
	}{
		{"clause body", "clause counter overflow", func(f *report.FunctionReport) *int { return &f.Clauses[0].BodyExecutions }},
		{"clause selection", "clause counter overflow", func(f *report.FunctionReport) *int { return &f.Clauses[0].DirectSelections }},
		{"skipped decision", "decision counter overflow", func(f *report.FunctionReport) *int { return &f.Decisions[0].NotEvaluated }},
	} {
		t.Run(counter.name, func(t *testing.T) {
			for _, test := range []struct {
				name        string
				left, right int
				want        int
				invalid     bool
			}{
				{"below limit", math.MaxInt - 2, 1, math.MaxInt - 1, false},
				{"exact limit", math.MaxInt - 1, 1, math.MaxInt, false},
				{"overflow", math.MaxInt, 1, 0, true},
				{"negative first", -1, 0, 0, true},
				{"negative second", 0, -1, 0, true},
			} {
				t.Run(test.name, func(t *testing.T) {
					var snapshots []report.AnalysisSnapshot
					for i, runID := range []string{"a", "b"} {
						input := offlineDecisionInput(runID, i == 0)
						input.Decisions[0].Kind = cover.DecisionSwitchCase
						input.Clauses = []cover.ClauseMetadata{{ID: 2, SwitchID: 3, Package: "example.com/m/a", Function: "Check", Kind: cover.ClauseExpressionSwitch, Role: cover.ClauseCase, Location: location("a.go", 20), Expressions: []string{"1"}}}
						r := report.Build(input)
						*counter.field(&r.Packages[0].Files[0].Functions[0]) = []int{test.left, test.right}[i]
						snapshots = append(snapshots, report.AnalysisSnapshot{ID: runID, Report: r})
					}
					for range 2 {
						before, err := json.Marshal(snapshots)
						if err != nil {
							t.Fatal(err)
						}
						merged, err := report.AnalyzeSnapshots(t.Context(), snapshots, config.AllCoverage(), mcdc.AnalysisBudget{})
						if test.invalid {
							if err == nil || !strings.Contains(err.Error(), counter.diagnostic) || merged.SchemaVersion != "" {
								t.Fatalf("invalid counter produced report or wrong error: %v", err)
							}
						} else {
							if err != nil {
								t.Fatal(err)
							}
							if got := *counter.field(&merged.Packages[0].Files[0].Functions[0]); got != test.want {
								t.Fatalf("counter = %d, want %d", got, test.want)
							}
						}
						after, err := json.Marshal(snapshots)
						if err != nil || string(before) != string(after) {
							t.Fatalf("analysis changed original counters: %v", err)
						}
						slices.Reverse(snapshots)
					}
				})
			}
		})
	}
}

func TestOfflineReanalysisChangesOnlyMCDCAndProvenance(t *testing.T) {
	t.Parallel()
	input := offlineDecisionInput("a", true)
	second := completedEvaluation(1, 2, false, cover.ConditionFalse)
	second.RunID = "a"
	input.Evaluations = append(input.Evaluations, second)
	input.MaskingAnalysisBudget = mcdc.AnalysisBudget{MaxSolverBytes: 1}
	first := report.Build(input)
	result, err := report.AnalyzeSnapshots(t.Context(), []report.AnalysisSnapshot{{ID: "a", Report: first}}, input.Coverage, mcdc.AnalysisBudget{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.MCDCMasking.AnalysisIncomplete != 0 || result.Summary.MCDCMasking.Covered != 1 {
		t.Fatalf("analysis did not finish: %+v", result.Summary)
	}
	a, b := first.Summary, result.Summary
	a.MCDCMasking = b.MCDCMasking
	if !reflect.DeepEqual(a, b) {
		t.Fatal("reanalysis changed another coverage metric")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := report.AnalyzeSnapshots(ctx, []report.AnalysisSnapshot{{ID: "a", Report: first}}, input.Coverage, mcdc.AnalysisBudget{}); err == nil {
		t.Fatal("canceled reanalysis succeeded")
	}
}

func TestOfflineAnalysisChecksEachCapabilityBoundary(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*report.Report)
	}{
		{"tool capabilities", func(r *report.Report) {
			r.Capabilities[backend.CapabilityMCDCMasking] = backend.CapabilityUnknown
		}},
		{"producer capabilities", func(r *report.Report) {
			r.Backends = append(r.Backends, backend.ProducerCapabilities{Backend: "different-producer"})
		}},
		{"instrumentation", func(r *report.Report) { r.Instrumentation = backend.InstrumentationReport{} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := report.Build(offlineDecisionInput("a", true))
			b := report.Build(offlineDecisionInput("b", false))
			snapshots := []report.AnalysisSnapshot{{ID: "a", Report: a}, {ID: "b", Report: b}}
			if _, err := report.AnalyzeSnapshots(t.Context(), snapshots, config.AllCoverage(), mcdc.AnalysisBudget{}); err != nil {
				t.Fatalf("matching capabilities: %v", err)
			}
			// Change only the named boundary, keeping the source inventory equal.
			test.mutate(&snapshots[1].Report)
			if _, err := report.AnalyzeSnapshots(t.Context(), snapshots, config.AllCoverage(), mcdc.AnalysisBudget{}); err == nil || !strings.Contains(err.Error(), "capabilities differ") {
				t.Fatalf("changed capability boundary: %v", err)
			}
		})
	}
}

func TestOfflineAnalysisPreservesFailuresInEitherInputOrder(t *testing.T) {
	t.Parallel()
	type outcome struct {
		status cover.RunStatus
		kind   cover.RunFailureKind
		test   report.ResultStatus
		pkg    string
	}
	passed := outcome{cover.RunPassed, cover.RunFailureNone, report.ResultPassed, "passed"}
	failed := outcome{cover.RunFailed, cover.RunFailureTest, report.ResultFailed, "failed"}
	buildFailed := outcome{cover.RunFailed, cover.RunFailureBuild, report.ResultNotRun, "build-failed"}
	timedOut := outcome{cover.RunTimeout, cover.RunFailureTimeout, report.ResultTimeout, "timeout"}
	for _, test := range []struct {
		name string
		a, b outcome
		want outcome
	}{
		{"success", passed, passed, passed},
		{"test failure", passed, failed, failed},
		{"repeated failure", failed, failed, failed},
		{"timeout", passed, timedOut, timedOut},
		{"mixed failures", failed, buildFailed, outcome{cover.RunFailed, cover.RunFailureMixed, report.ResultFailed, "build-failed"}},
		{"failure and timeout", failed, timedOut, outcome{cover.RunTimeout, cover.RunFailureMixed, report.ResultTimeout, "timeout"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var snapshots []report.AnalysisSnapshot
			for i, state := range []outcome{test.a, test.b} {
				input := offlineDecisionInput([]string{"a", "b"}[i], i == 0)
				input.RunStatus, input.FailureKind = state.status, state.kind
				input.Complete = state.status == cover.RunPassed
				input.PackageStatuses["example.com/m/a"] = state.pkg
				input.Results = report.RunResults{Test: state.test, Measurement: report.ResultPassed, Integrity: report.ResultPassed, Strict: report.ResultFailed, Threshold: report.ResultFailed}
				input.Errors = []report.ReportError{
					{Phase: "policy", Code: "strict-coverage-gap", Message: "original strict failure"},
					{Phase: "policy", Code: "coverage-threshold-failed", Message: "original threshold failure"},
				}
				if state.status != cover.RunPassed {
					input.Errors = append(input.Errors, report.ReportError{Phase: "test", Code: string(state.kind), Message: "original execution failure"})
				}
				snapshots = append(snapshots, report.AnalysisSnapshot{ID: []string{"a", "b"}[i], Report: report.Build(input)})
			}
			for range 2 {
				merged, err := report.AnalyzeSnapshots(t.Context(), snapshots, config.AllCoverage(), mcdc.AnalysisBudget{})
				if err != nil {
					t.Fatal(err)
				}
				if merged.Run.Status != test.want.status || merged.Run.FailureKind != test.want.kind || merged.Run.Results.Test != test.want.test || merged.Packages[0].Status != test.want.pkg {
					t.Fatalf("merged outcome = %+v, package = %s; want %+v", merged.Run, merged.Packages[0].Status, test.want)
				}
				if merged.Run.Complete != (test.want.status == cover.RunPassed) || merged.Run.Results.Strict != report.ResultNotRequested || merged.Run.Results.Threshold != report.ResultNotRequested {
					t.Fatalf("derived completion/policy results = %+v", merged.Run)
				}
				var wantErrors []report.ReportError
				for i, original := range snapshots {
					if merged.ContributingRuns[i].Run != original.Report.Run || !reflect.DeepEqual(merged.ContributingRuns[i].Errors, original.Report.Errors) {
						t.Fatal("original execution or policy outcome changed")
					}
					for _, failure := range original.Report.Errors {
						if failure.Phase == "test" && !slices.Contains(wantErrors, failure) {
							wantErrors = append(wantErrors, failure)
						}
					}
				}
				if !slices.Equal(merged.Errors, wantErrors) {
					t.Fatalf("aggregate errors = %+v, want execution errors %+v", merged.Errors, wantErrors)
				}
				slices.Reverse(snapshots)
			}
		})
	}
}

func TestOfflineAnalysisDeduplicatesEvaluationsAndRejectsConflicts(t *testing.T) {
	t.Parallel()
	a := report.Build(offlineDecisionInput("same-run", true))
	b := report.Build(offlineDecisionInput("same-run", true))
	snapshots := []report.AnalysisSnapshot{{ID: "a", Report: a}, {ID: "b", Report: b}}
	merged, err := report.AnalyzeSnapshots(t.Context(), snapshots, config.AllCoverage(), mcdc.AnalysisBudget{})
	if err != nil {
		t.Fatal(err)
	}
	d := merged.Packages[0].Files[0].Functions[0].Decisions[0]
	if len(d.Evaluations) != 1 || merged.Summary.Decision.Covered != 1 || merged.Summary.MCDCMasking.Covered != 0 {
		t.Fatalf("duplicate observation changed coverage: %+v", d)
	}
	// Each vector is valid alone, but the same physical evaluation cannot have
	// two different outcomes. Treating it as two observations would invent a witness.
	snapshots[1].Report = report.Build(offlineDecisionInput("same-run", false))
	if _, err := report.AnalyzeSnapshots(t.Context(), snapshots, config.AllCoverage(), mcdc.AnalysisBudget{}); err == nil || !strings.Contains(err.Error(), "conflicting evaluation identity") {
		t.Fatalf("conflicting evaluation accepted: %v", err)
	}
}
