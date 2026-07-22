package report

import (
	"context"
	"runtime"
	"sync"

	"github.com/shrydev2020/gomcdc/v2/internal/config"
	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
	"github.com/shrydev2020/gomcdc/v2/internal/mcdc"
)

// Four concurrent decisions cap the multiplier applied to each independent
// condition-obligation solver budget. A larger GOMAXPROCS does not silently
// turn the per-obligation solver-byte limit into unbounded process memory.
const maxMaskingDecisionWorkers = 4

type decisionBuildTask struct {
	metadata     cover.DecisionMetadata
	evaluations  []cover.DecisionEvaluation
	notEvaluated int
	state        entityState
}

type decisionBuildFunc func(decisionBuildTask) DecisionReport

func effectiveMaskingDecisionWorkers(configured, decisionCount int) int {
	if decisionCount <= 1 {
		return 1
	}
	workers := configured
	if workers == 0 {
		workers = runtime.GOMAXPROCS(0)
	} else if workers < 0 {
		workers = 1
	}
	if workers < 1 {
		workers = 1
	}
	if workers > maxMaskingDecisionWorkers {
		workers = maxMaskingDecisionWorkers
	}
	if workers > decisionCount {
		workers = decisionCount
	}
	return workers
}

func buildDecisionReports(
	ctx context.Context,
	tasks []decisionBuildTask,
	coverage config.CoverageSet,
	maskingBudget mcdc.AnalysisBudget,
	workers int,
) []DecisionReport {
	build := func(task decisionBuildTask) DecisionReport {
		return buildDecisionReport(
			task.metadata,
			task.evaluations,
			task.notEvaluated,
			task.state,
			coverage,
			maskingBudget,
			false,
		)
	}
	canceled := func(task decisionBuildTask) DecisionReport {
		return buildDecisionReport(
			task.metadata,
			task.evaluations,
			task.notEvaluated,
			task.state,
			coverage,
			maskingBudget,
			true,
		)
	}
	return runDecisionBuildPool(ctx, tasks, workers, build, canceled)
}

// runDecisionBuildPool writes each result to its sorted task index. Workers
// never mutate report builders. A worker panic cancels sibling scheduling,
// waits for every started worker, then re-panics on the caller goroutine.
func runDecisionBuildPool(
	ctx context.Context,
	tasks []decisionBuildTask,
	workers int,
	build decisionBuildFunc,
	canceled decisionBuildFunc,
) []DecisionReport {
	if ctx == nil {
		ctx = context.Background()
	}
	results := make([]DecisionReport, len(tasks))
	completed := make([]bool, len(tasks))
	if len(tasks) == 0 {
		return results
	}
	if workers <= 1 {
		for index, task := range tasks {
			if ctx.Err() != nil {
				results[index] = canceled(task)
				continue
			}
			results[index] = build(task)
		}
		return results
	}

	poolContext, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int, len(tasks))
	for index := range tasks {
		jobs <- index
	}
	close(jobs)
	panicValues := make(chan any, 1)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			defer func() {
				if value := recover(); value != nil {
					select {
					case panicValues <- value:
					default:
					}
					cancel()
				}
			}()
			for {
				if poolContext.Err() != nil {
					return
				}
				select {
				case <-poolContext.Done():
					return
				case index, ok := <-jobs:
					if !ok {
						return
					}
					if poolContext.Err() != nil {
						return
					}
					results[index] = build(tasks[index])
					completed[index] = true
				}
			}
		}()
	}
	wait.Wait()
	select {
	case value := <-panicValues:
		panic(value)
	default:
	}
	for index, done := range completed {
		if !done {
			results[index] = canceled(tasks[index])
		}
	}
	return results
}

func canceledMaskingResult(metadata cover.DecisionMetadata) cover.MCDCResult {
	const reason = "Masking MC/DC analysis was canceled before this decision started"
	conditions := make([]cover.MCDCConditionResult, 0, len(metadata.Conditions))
	for _, condition := range metadata.Conditions {
		conditions = append(conditions, cover.MCDCConditionResult{
			ConditionIndex: condition.Index,
			Outcome:        cover.CoverageOutcomeUnknown,
			Support:        cover.SupportSupported,
			Analysis:       cover.AnalysisIncomplete,
			Reason:         reason,
		})
	}
	return cover.MCDCResult{
		DecisionID: metadata.ID,
		Metric:     cover.CoverageMetricMCDCMasking,
		Outcome:    cover.CoverageOutcomeUnknown,
		Support:    cover.SupportSupported,
		Analysis:   cover.AnalysisIncomplete,
		Conditions: conditions,
		Reason:     reason,
	}
}
