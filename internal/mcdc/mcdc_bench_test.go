package mcdc

import (
	"fmt"
	"testing"

	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
)

type maskingAnalyzeBenchmarkCase struct {
	name                     string
	conditions               int
	expression               *cover.BooleanExpression
	evaluations              []cover.DecisionEvaluation
	strategy                 MCDCStrategy
	target                   uint16
	wantTargetOutcome        cover.CoverageOutcome
	wantTargetAnalysis       cover.AnalysisStatus
	minTargetEvaluationPairs uint64
}

func BenchmarkMaskingAnalyze(b *testing.B) {
	const conditionCount = 8
	balanced := benchmarkBalancedExpression(0, conditionCount)
	leftSkewed := benchmarkSkewedExpression(conditionCount, true)
	rightSkewed := benchmarkSkewedExpression(conditionCount, false)
	negated := cover.NewNotExpression(benchmarkBalancedExpression(0, conditionCount))

	full := func(expression *cover.BooleanExpression) []cover.DecisionEvaluation {
		return oracleEvaluations(expression, conditionCount)
	}
	nested := cover.NewOrExpression(
		cover.NewAndExpression(cover.NewConditionExpression(0), cover.NewConditionExpression(1)),
		cover.NewConditionExpression(2),
	)
	first := completed(1, []cover.ConditionState{conditionFalse, notEvaluated, conditionFalse}, false)
	badSecond := completed(2, []cover.ConditionState{conditionTrue, conditionFalse, conditionTrue}, true)
	goodSecond := completed(3, []cover.ConditionState{conditionTrue, conditionTrue, notEvaluated}, true)
	highUnobservedExpression, highUnobservedEvaluations := benchmarkHighUnobserved(16)
	cases := []maskingAnalyzeBenchmarkCase{
		{name: "balanced/full", conditions: conditionCount, expression: balanced, evaluations: full(balanced), strategy: MaskingStrategy{}},
		{name: "left-skewed/full", conditions: conditionCount, expression: leftSkewed, evaluations: full(leftSkewed), strategy: MaskingStrategy{}},
		{name: "right-skewed/full", conditions: conditionCount, expression: rightSkewed, evaluations: full(rightSkewed), strategy: MaskingStrategy{}},
		{name: "negated/full", conditions: conditionCount, expression: negated, evaluations: full(negated), strategy: MaskingStrategy{}},
		{name: "nested/late-witness", conditions: 3, expression: nested, evaluations: []cover.DecisionEvaluation{first, badSecond, goodSecond}, strategy: MaskingStrategy{}},
		{name: "nested/no-witness", conditions: 3, expression: nested, evaluations: []cover.DecisionEvaluation{first, badSecond}, strategy: MaskingStrategy{}},
		{name: "left-skewed/high-unobserved", conditions: 16, expression: highUnobservedExpression, evaluations: highUnobservedEvaluations, strategy: MaskingStrategy{}},
		{
			name:        "nested/resource-limit",
			conditions:  3,
			expression:  nested,
			evaluations: []cover.DecisionEvaluation{first, badSecond, goodSecond},
			strategy: MaskingStrategy{Budget: AnalysisBudget{
				MaxEvaluationPairs: 1,
			}},
		},
		{name: "unique-cause/full", conditions: conditionCount, expression: balanced, evaluations: full(balanced), strategy: UniqueCauseStrategy{}},
	}

	for _, test := range cases {
		b.Run(test.name, func(b *testing.B) {
			benchmarkMaskingAnalyzeCase(b, test)
		})
	}
}

func BenchmarkMaskingAnalyzeHeavy(b *testing.B) {
	const (
		balancedConditions     = 32
		exactGuardedConditions = 24
		limitedGuardConditions = 48
		unobservedConditions   = 64
		limitedEvaluationPairs = 10_000
	)
	balanced := benchmarkBalancedExpression(0, balancedConditions)
	exactGuarded, exactNoWitness, exactCandidatePairs := benchmarkGuardedNoWitness(exactGuardedConditions)
	limitedGuarded, limitedNoWitness, limitedCandidatePairs := benchmarkGuardedNoWitness(limitedGuardConditions)
	if exactCandidatePairs == 0 || limitedCandidatePairs <= limitedEvaluationPairs {
		b.Fatalf("guarded candidate pairs = (%d, %d), want nonzero exact and more than %d limited",
			exactCandidatePairs, limitedCandidatePairs, limitedEvaluationPairs)
	}
	highUnobserved, highUnobservedEvaluations := benchmarkHighUnobserved(unobservedConditions)
	cases := []maskingAnalyzeBenchmarkCase{
		{
			name: "balanced/full-C32", conditions: balancedConditions,
			expression: balanced, evaluations: benchmarkObservedEvaluations(balanced, balancedConditions), strategy: MaskingStrategy{},
		},
		{
			name: "guarded/no-witness-C24", conditions: exactGuardedConditions,
			expression: exactGuarded, evaluations: exactNoWitness, strategy: MaskingStrategy{}, target: 0,
			wantTargetOutcome: cover.CoverageOutcomeNotCovered, wantTargetAnalysis: cover.AnalysisComplete,
			minTargetEvaluationPairs: uint64(exactCandidatePairs),
		},
		{
			name: "guarded/default-state-limit-C48", conditions: limitedGuardConditions,
			expression: limitedGuarded, evaluations: limitedNoWitness,
			strategy: MaskingStrategy{}, target: 0,
			wantTargetOutcome: cover.CoverageOutcomeUnknown, wantTargetAnalysis: cover.AnalysisIncomplete,
			minTargetEvaluationPairs: 1,
		},
		{
			name: "guarded/evaluation-pair-limit-C48", conditions: limitedGuardConditions,
			expression: limitedGuarded, evaluations: limitedNoWitness,
			strategy: MaskingStrategy{Budget: AnalysisBudget{MaxEvaluationPairs: limitedEvaluationPairs}}, target: 0,
			wantTargetOutcome: cover.CoverageOutcomeUnknown, wantTargetAnalysis: cover.AnalysisIncomplete,
			minTargetEvaluationPairs: limitedEvaluationPairs,
		},
		{
			name: "left-skewed/high-unobserved-C64", conditions: unobservedConditions,
			expression: highUnobserved, evaluations: highUnobservedEvaluations, strategy: MaskingStrategy{},
			wantTargetOutcome: cover.CoverageOutcomeCovered, wantTargetAnalysis: cover.AnalysisComplete,
			minTargetEvaluationPairs: 1,
		},
	}
	for _, test := range cases {
		b.Run(test.name, func(b *testing.B) {
			benchmarkMaskingAnalyzeCase(b, test)
		})
	}
}

func benchmarkMaskingAnalyzeCase(b *testing.B, test maskingAnalyzeBenchmarkCase) {
	b.Helper()
	metadata := oracleMetadata(test.expression, test.conditions)
	if len(test.evaluations) > 0 {
		metadata.ID = test.evaluations[0].DecisionID
	}
	var result cover.MCDCResult
	var evaluationPairs, searchStates, solverBytes uint64
	var targetStats maskingSearchStats
	if strategy, ok := test.strategy.(MaskingStrategy); ok {
		result = strategy.analyze(metadata, test.evaluations, func(target uint16, stats maskingSearchStats) {
			evaluationPairs += stats.EvaluationPairs
			searchStates += stats.SearchStates
			if stats.SolverBytes > solverBytes {
				solverBytes = stats.SolverBytes
			}
			if target == test.target {
				targetStats = stats
			}
		})
	} else {
		result = test.strategy.Analyze(metadata, test.evaluations)
	}
	if result.InvalidEvaluations != 0 || result.EvaluationsAnalyzed != len(test.evaluations) {
		b.Fatalf("prepared evaluations = %d invalid=%d, want %d valid unique vectors",
			result.EvaluationsAnalyzed, result.InvalidEvaluations, len(test.evaluations))
	}
	if test.wantTargetOutcome != "" {
		target := result.Conditions[test.target]
		if target.Outcome != test.wantTargetOutcome || target.Analysis != test.wantTargetAnalysis {
			b.Fatalf("target %d = (%s, %s), want (%s, %s)",
				test.target, target.Outcome, target.Analysis, test.wantTargetOutcome, test.wantTargetAnalysis)
		}
	}
	if targetStats.EvaluationPairs < test.minTargetEvaluationPairs {
		b.Fatalf("target %d evaluation pairs = %d, want at least %d",
			test.target, targetStats.EvaluationPairs, test.minTargetEvaluationPairs)
	}
	covered, incomplete := 0, 0
	for _, condition := range result.Conditions {
		if condition.Outcome == cover.CoverageOutcomeCovered {
			covered++
		}
		if condition.Analysis == cover.AnalysisIncomplete {
			incomplete++
		}
	}
	unobserved := 0
	for _, evaluation := range test.evaluations {
		for _, state := range evaluation.Conditions {
			if state == cover.ConditionNotEvaluated {
				unobserved++
			}
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		result = test.strategy.Analyze(metadata, test.evaluations)
	}
	b.ReportMetric(float64(test.conditions), "conditions")
	b.ReportMetric(float64(len(test.evaluations)), "evaluations")
	b.ReportMetric(float64(unobserved)/float64(len(test.evaluations)), "unobserved/evaluation")
	b.ReportMetric(float64(covered), "covered")
	b.ReportMetric(float64(incomplete), "analysis-incomplete")
	b.ReportMetric(float64(evaluationPairs), "evaluation-pairs")
	b.ReportMetric(float64(searchStates), "search-states")
	b.ReportMetric(float64(targetStats.EvaluationPairs), "target-evaluation-pairs")
	b.ReportMetric(float64(targetStats.SearchStates), "target-search-states")
	b.ReportMetric(float64(solverBytes), "solver-bytes")
	_ = result
}

func benchmarkGuardedNoWitness(conditionCount int) (*cover.BooleanExpression, []cover.DecisionEvaluation, int) {
	guardCount := (conditionCount - 1) / 2
	guard := benchmarkBalancedExpression(1, guardCount)
	tail := benchmarkBalancedExpression(1+guardCount, conditionCount-1-guardCount)
	expression := cover.NewOrExpression(
		cover.NewAndExpression(cover.NewConditionExpression(0), guard),
		tail,
	)
	all := benchmarkObservedEvaluations(expression, conditionCount)
	firstCount, secondCount := 0, 0
	evaluations := make([]cover.DecisionEvaluation, 0, len(all))
	for _, evaluation := range all {
		target, evaluated := evaluation.Conditions[0].Bool()
		if !evaluated {
			continue
		}
		if !target && !evaluation.Result {
			evaluations = append(evaluations, evaluation)
			firstCount++
			continue
		}
		if !target || !evaluation.Result {
			continue
		}
		guardResult, err := evaluateObserved(guard, evaluation.Conditions)
		if err == nil && !guardResult {
			evaluations = append(evaluations, evaluation)
			secondCount++
		}
	}
	return expression, evaluations, firstCount * secondCount
}

type benchmarkEvaluationTrace struct {
	conditions []cover.ConditionState
	result     bool
}

// benchmarkObservedEvaluations enumerates distinct short-circuit traces
// directly from the read-once tree. Unlike a truth-table generator, its setup
// cost does not grow as 2^C, so benchmarks can exercise large expressions.
func benchmarkObservedEvaluations(
	expression *cover.BooleanExpression,
	conditionCount int,
) []cover.DecisionEvaluation {
	traces := benchmarkEvaluationTraces(expression, conditionCount)
	evaluations := make([]cover.DecisionEvaluation, len(traces))
	for index, trace := range traces {
		evaluations[index] = cover.DecisionEvaluation{
			DecisionID:   oracleDecisionID,
			EvaluationID: cover.EvaluationID(index + 1),
			TestID:       "benchmark-trace",
			Conditions:   trace.conditions,
			Result:       trace.result,
			Status:       cover.EvaluationCompleted,
		}
	}
	return evaluations
}

func benchmarkEvaluationTraces(
	expression *cover.BooleanExpression,
	conditionCount int,
) []benchmarkEvaluationTrace {
	switch expression.Kind {
	case cover.BooleanExpressionCondition:
		falseConditions := make([]cover.ConditionState, conditionCount)
		falseConditions[expression.ConditionIndex] = cover.ConditionFalse
		trueConditions := make([]cover.ConditionState, conditionCount)
		trueConditions[expression.ConditionIndex] = cover.ConditionTrue
		return []benchmarkEvaluationTrace{
			{conditions: falseConditions, result: false},
			{conditions: trueConditions, result: true},
		}
	case cover.BooleanExpressionConstant:
		return []benchmarkEvaluationTrace{{
			conditions: make([]cover.ConditionState, conditionCount),
			result:     expression.Constant,
		}}
	case cover.BooleanExpressionNot:
		traces := benchmarkEvaluationTraces(expression.Left, conditionCount)
		for index := range traces {
			traces[index].result = !traces[index].result
		}
		return traces
	case cover.BooleanExpressionAnd, cover.BooleanExpressionOr:
		leftTraces := benchmarkEvaluationTraces(expression.Left, conditionCount)
		rightTraces := benchmarkEvaluationTraces(expression.Right, conditionCount)
		traces := make([]benchmarkEvaluationTrace, 0, len(leftTraces)+len(rightTraces))
		for _, left := range leftTraces {
			shortCircuits := expression.Kind == cover.BooleanExpressionAnd && !left.result ||
				expression.Kind == cover.BooleanExpressionOr && left.result
			if shortCircuits {
				traces = append(traces, left)
				continue
			}
			for _, right := range rightTraces {
				conditions := append([]cover.ConditionState(nil), left.conditions...)
				for index, state := range right.conditions {
					if state != cover.ConditionNotEvaluated {
						conditions[index] = state
					}
				}
				traces = append(traces, benchmarkEvaluationTrace{
					conditions: conditions,
					result:     right.result,
				})
			}
		}
		return traces
	default:
		panic("benchmark trace generator received unsupported expression")
	}
}

func benchmarkHighUnobserved(conditionCount int) (*cover.BooleanExpression, []cover.DecisionEvaluation) {
	sibling := cover.NewConditionExpression(1)
	for index := 2; index < conditionCount; index++ {
		sibling = cover.NewOrExpression(sibling, cover.NewConditionExpression(uint16(index)))
	}
	expression := cover.NewAndExpression(cover.NewConditionExpression(0), sibling)
	first := make([]cover.ConditionState, conditionCount)
	first[0] = cover.ConditionFalse
	second := make([]cover.ConditionState, conditionCount)
	second[0] = cover.ConditionTrue
	second[1] = cover.ConditionTrue
	return expression, []cover.DecisionEvaluation{
		completed(1, first, false),
		completed(2, second, true),
	}
}

func benchmarkBalancedExpression(start, count int) *cover.BooleanExpression {
	if count == 1 {
		return cover.NewConditionExpression(uint16(start))
	}
	leftCount := count / 2
	left := benchmarkBalancedExpression(start, leftCount)
	right := benchmarkBalancedExpression(start+leftCount, count-leftCount)
	if start%2 == 0 {
		return cover.NewAndExpression(left, right)
	}
	return cover.NewOrExpression(left, right)
}

func benchmarkSkewedExpression(count int, left bool) *cover.BooleanExpression {
	if count < 1 {
		panic(fmt.Sprintf("condition count must be positive: %d", count))
	}
	expression := cover.NewConditionExpression(0)
	for index := 1; index < count; index++ {
		leaf := cover.NewConditionExpression(uint16(index))
		if left {
			expression = cover.NewAndExpression(expression, leaf)
		} else {
			expression = cover.NewAndExpression(leaf, expression)
		}
	}
	return expression
}

func BenchmarkUniqueCauseWitness(b *testing.B) {
	const conditionCount = 8
	evaluations := make([]cover.DecisionEvaluation, 0, 256)
	for vector := 0; vector < 256; vector++ {
		states := make([]cover.ConditionState, conditionCount)
		for index := range states {
			if vector&(1<<index) == 0 {
				states[index] = cover.ConditionFalse
			} else {
				states[index] = cover.ConditionTrue
			}
		}
		result := vector%3 == 0
		evaluations = append(evaluations, cover.DecisionEvaluation{
			DecisionID: 1, EvaluationID: cover.EvaluationID(vector + 1), Conditions: states, Result: result, Status: cover.EvaluationCompleted,
		})
	}
	b.Run("hybrid", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for target := uint16(0); target < conditionCount; target++ {
				_, _ = uniqueCauseWitness(evaluations, target)
			}
		}
	})
	b.Run("nested-pair-baseline", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for target := uint16(0); target < conditionCount; target++ {
				_, _ = naiveUniqueCauseWitness(evaluations, target)
			}
		}
	})
	b.Run("indexed-no-witness", func(b *testing.B) {
		noWitness := append([]cover.DecisionEvaluation(nil), evaluations...)
		for index := range noWitness {
			noWitness[index].Result = false
		}
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for target := uint16(0); target < conditionCount; target++ {
				_, _ = uniqueCauseWitness(noWitness, target)
			}
		}
	})
	b.Run("nested-pair-no-witness-baseline", func(b *testing.B) {
		noWitness := append([]cover.DecisionEvaluation(nil), evaluations...)
		for index := range noWitness {
			noWitness[index].Result = false
		}
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for target := uint16(0); target < conditionCount; target++ {
				_, _ = naiveUniqueCauseWitness(noWitness, target)
			}
		}
	})
}

func naiveUniqueCauseWitness(evaluations []cover.DecisionEvaluation, target uint16) (int, int) {
	for first := 0; first < len(evaluations); first++ {
		for second := first + 1; second < len(evaluations); second++ {
			left, right := evaluations[first], evaluations[second]
			if left.Result == right.Result || !left.Conditions[target].IsEvaluated() || !right.Conditions[target].IsEvaluated() || left.Conditions[target] == right.Conditions[target] {
				continue
			}
			match := true
			for index := range left.Conditions {
				if uint16(index) != target && left.Conditions[index] != right.Conditions[index] {
					match = false
					break
				}
			}
			if match {
				return first, second
			}
		}
	}
	return -1, -1
}
