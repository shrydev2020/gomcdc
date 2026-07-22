package report

import (
	"context"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shrydev2020/gomcdc/v2/internal/config"
	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
)

func TestParallelDecisionBuildMatchesSequentialReport(t *testing.T) {
	t.Parallel()
	input := parallelDecisionTestInput(12)
	input.MaxMaskingDecisionWorkers = 1
	sequential := Build(input)
	input.MaxMaskingDecisionWorkers = 4
	parallel := Build(input)
	if !reflect.DeepEqual(parallel, sequential) {
		t.Fatalf("parallel report differs from sequential report\nparallel: %#v\nsequential: %#v", parallel, sequential)
	}
}

func TestCanceledDecisionSchedulingRetainsEvidenceAndMarksMaskingIncomplete(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	input := parallelDecisionTestInput(1)
	input.Context = ctx
	input.MaxMaskingDecisionWorkers = 4
	built := Build(input)
	decision := built.Packages[0].Files[0].Functions[0].Decisions[0]
	if !decision.DecisionCoverage.True || !decision.DecisionCoverage.False || len(decision.Evaluations) != 3 {
		t.Fatalf("canceled Masking scheduling discarded accepted evidence: %#v", decision)
	}
	if decision.MCDCUnique.Status == string(cover.CoverageAnalysisIncomplete) {
		t.Fatalf("cancellation leaked from Masking into Unique-Cause analysis: %#v", decision.MCDCUnique)
	}
	if decision.MCDCMasking.Status != string(cover.CoverageAnalysisIncomplete) ||
		!strings.Contains(decision.MCDCMasking.Reason, "analysis canceled") {
		t.Fatalf("canceled Masking analysis = %#v", decision.MCDCMasking)
	}
}

func TestDecisionBuildPoolWaitsForWorkersBeforePropagatingPanic(t *testing.T) {
	var active atomic.Int64
	tasks := make([]decisionBuildTask, 32)
	for index := range tasks {
		tasks[index].metadata.ID = cover.DecisionID(index + 1)
	}
	build := func(_ context.Context, task decisionBuildTask) DecisionReport {
		active.Add(1)
		defer active.Add(-1)
		if task.metadata.ID == 1 {
			panic("decision-builder-panic")
		}
		time.Sleep(time.Millisecond)
		return DecisionReport{DecisionID: formatID(uint64(task.metadata.ID))}
	}
	recovered := func() (value any) {
		defer func() { value = recover() }()
		runDecisionBuildPool(t.Context(), tasks, 4, build)
		return nil
	}()
	if recovered != "decision-builder-panic" {
		t.Fatalf("recovered panic = %#v", recovered)
	}
	if got := active.Load(); got != 0 {
		t.Fatalf("%d decision workers remained active after panic", got)
	}
}

func TestCanceledDecisionBuildPoolLeavesNoWorkers(t *testing.T) {
	baseline := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	tasks := make([]decisionBuildTask, 64)
	var started, canceled atomic.Int64
	build := func(ctx context.Context, task decisionBuildTask) DecisionReport {
		if ctx.Err() != nil {
			canceled.Add(1)
			return DecisionReport{}
		}
		started.Add(1)
		return DecisionReport{}
	}
	results := runDecisionBuildPool(ctx, tasks, 4, build)
	if len(results) != len(tasks) || started.Load() != 0 || canceled.Load() != int64(len(tasks)) {
		t.Fatalf("canceled pool started %d analyses, finalized %d tasks, and returned %d results", started.Load(), canceled.Load(), len(results))
	}
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > baseline+1 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if current := runtime.NumGoroutine(); current > baseline+1 {
		t.Fatalf("decision worker goroutines leaked: baseline=%d current=%d", baseline, current)
	}
}

func TestDecisionBuildPoolRetainsCompletedResultsAfterCancellation(t *testing.T) {
	tasks := make([]decisionBuildTask, 8)
	for index := range tasks {
		tasks[index].metadata.ID = cover.DecisionID(index + 1)
	}
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan cover.DecisionID, 2)
	release := make(chan struct{})
	build := func(ctx context.Context, task decisionBuildTask) DecisionReport {
		if ctx.Err() != nil {
			return DecisionReport{DecisionID: "canceled"}
		}
		started <- task.metadata.ID
		<-release
		return DecisionReport{DecisionID: "completed"}
	}
	done := make(chan []DecisionReport, 1)
	go func() {
		done <- runDecisionBuildPool(ctx, tasks, 2, build)
	}()
	<-started
	<-started
	cancel()
	close(release)
	results := <-done
	completed, canceledCount := 0, 0
	for _, result := range results {
		switch result.DecisionID {
		case "completed":
			completed++
		case "canceled":
			canceledCount++
		default:
			t.Fatalf("unexpected partial result %#v", result)
		}
	}
	if completed != 2 || canceledCount != len(tasks)-2 {
		t.Fatalf("partial results: completed=%d canceled=%d", completed, canceledCount)
	}
}

func TestMaskingDecisionWorkerCountIsHardCapped(t *testing.T) {
	t.Parallel()
	if got := effectiveMaskingDecisionWorkers(1000, 1000); got != maxMaskingDecisionWorkers {
		t.Fatalf("worker count = %d, want hard cap %d", got, maxMaskingDecisionWorkers)
	}
	if got := effectiveMaskingDecisionWorkers(3, 2); got != 2 {
		t.Fatalf("worker count = %d, want decision count 2", got)
	}
}

func TestDecisionParallelismRequiresMaskingMetric(t *testing.T) {
	t.Parallel()
	input := parallelDecisionTestInput(8)
	input.Coverage = config.CoverageSet{config.MetricDecision: true}
	input.MaxMaskingDecisionWorkers = maxMaskingDecisionWorkers
	if got := newBuildContext(input).maskingDecisionWorkers; got != 1 {
		t.Fatalf("non-Masking decision workers = %d, want 1", got)
	}
}

func parallelDecisionTestInput(decisionCount int) Input {
	const packagePath = "example.test/parallel/p"
	decisions := make([]cover.DecisionMetadata, 0, decisionCount)
	evaluations := make([]cover.DecisionEvaluation, 0, decisionCount*3)
	var evaluationID cover.EvaluationID
	for index := 0; index < decisionCount; index++ {
		decisionID := cover.DecisionID(index + 1)
		decisions = append(decisions, cover.DecisionMetadata{
			ID: decisionID, Package: packagePath, Function: "Check", Kind: cover.DecisionIf,
			Location: cover.SourceLocation{File: "p.go", StartOffset: index * 10, EndOffset: index*10 + 4,
				Start: cover.Position{Line: index + 1, Column: 1}, End: cover.Position{Line: index + 1, Column: 5}},
			Conditions: []cover.ConditionMetadata{
				{ID: cover.ConditionID(index*2 + 1), Index: 0},
				{ID: cover.ConditionID(index*2 + 2), Index: 1},
			},
			ExpressionTree: cover.NewAndExpression(cover.NewConditionExpression(0), cover.NewConditionExpression(1)),
		})
		for _, evaluation := range []struct {
			states []cover.ConditionState
			result bool
		}{
			{states: []cover.ConditionState{cover.ConditionFalse, cover.ConditionNotEvaluated}},
			{states: []cover.ConditionState{cover.ConditionTrue, cover.ConditionTrue}, result: true},
			{states: []cover.ConditionState{cover.ConditionTrue, cover.ConditionFalse}},
		} {
			evaluationID++
			evaluations = append(evaluations, cover.DecisionEvaluation{
				DecisionID: decisionID, EvaluationID: evaluationID, Status: cover.EvaluationCompleted,
				RunID: "parallel-test", PackagePath: packagePath, ProcessID: 1,
				Conditions: evaluation.states, Result: evaluation.result,
			})
		}
	}
	return Input{
		Coverage: config.CoverageSet{
			config.MetricDecision: true, config.MetricCondition: true,
			config.MetricMCDCUnique: true, config.MetricMCDCMasking: true,
		},
		Decisions: decisions, Evaluations: evaluations, RunStatus: cover.RunPassed, Complete: true,
		PackageStatuses: map[string]string{packagePath: "passed"},
	}
}
