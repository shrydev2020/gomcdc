package mcdc

import (
	"testing"

	cover "github.com/shrydev2020/gomcdc/internal/coverage"
)

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

func BenchmarkMaskingCompletions(b *testing.B) {
	const conditionCount = 8
	conditions := make([]cover.ConditionMetadata, conditionCount)
	var expression *cover.BooleanExpression
	for index := range conditions {
		conditions[index] = cover.ConditionMetadata{Index: uint16(index), Expression: string(rune('a' + index))}
		leaf := cover.NewConditionExpression(uint16(index))
		if expression == nil {
			expression = leaf
		} else {
			expression = cover.NewAndExpression(expression, leaf)
		}
	}
	evaluations := make([]cover.DecisionEvaluation, 256)
	for vector := range evaluations {
		states := make([]cover.ConditionState, conditionCount)
		for index := range states {
			if vector&(1<<index) == 0 {
				states[index] = cover.ConditionFalse
			} else {
				states[index] = cover.ConditionTrue
			}
		}
		evaluations[vector] = cover.DecisionEvaluation{DecisionID: 1, Conditions: states, Result: vector == 255, Status: cover.EvaluationCompleted}
	}
	b.ReportAllocs()
	for index := 0; index < b.N; index++ {
		for target := uint16(0); target < conditionCount; target++ {
			_ = maskingCompletionsForTarget(expression, evaluations, target)
		}
	}
}
