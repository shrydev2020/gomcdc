package mcdc

import cover "github.com/shrydev2020/gomcdc/internal/coverage"

// enumeratePivotalCompletions enumerates all assignments compatible with the
// observed short-circuit path that make target condition pivotal.
func enumeratePivotalCompletions(expression *cover.BooleanExpression, evaluation cover.DecisionEvaluation, target uint16) []maskingCompletion {
	if int(target) >= len(evaluation.Conditions) || !evaluation.Conditions[target].IsEvaluated() {
		return nil
	}
	values := make([]bool, len(evaluation.Conditions))
	for index, state := range evaluation.Conditions {
		if value, evaluated := state.Bool(); evaluated {
			values[index] = value
		}
	}
	steps := make([]pivotalPathStep, 0)
	if !findPivotalPath(expression, target, &steps) {
		return nil
	}
	completions := make([]maskingCompletion, 0)
	seen := make(map[string]struct{})
	var visit func(int, []bool)
	visit = func(stepIndex int, current []bool) {
		if stepIndex == len(steps) {
			states := make([]cover.ConditionState, len(current))
			result := evaluateCompletion(expression, current, states)
			if result != evaluation.Result || !sameConditionStates(states, evaluation.Conditions) || !isPivotal(expression, current, target) {
				return
			}
			key := boolVectorKey(current)
			if _, exists := seen[key]; exists {
				return
			}
			seen[key] = struct{}{}
			completions = append(completions, maskingCompletion{values: current})
			return
		}
		step := steps[stepIndex]
		enumerateSubtreeResult(step.sibling, step.desired, evaluation.Conditions, current, func(next []bool) {
			visit(stepIndex+1, next)
		})
	}
	visit(0, values)
	return completions
}

func boolVectorKey(values []bool) string {
	key := make([]byte, len(values))
	for index, value := range values {
		if value {
			key[index] = 1
		}
	}
	return string(key)
}

func enumerateSubtreeResult(expression *cover.BooleanExpression, desired bool, observed []cover.ConditionState, values []bool, yield func([]bool)) {
	switch expression.Kind {
	case cover.BooleanExpressionCondition:
		next := append([]bool(nil), values...)
		if value, evaluated := observed[expression.ConditionIndex].Bool(); evaluated {
			if value != desired {
				return
			}
			next[expression.ConditionIndex] = value
		} else {
			next[expression.ConditionIndex] = desired
		}
		yield(next)
	case cover.BooleanExpressionConstant:
		if expression.Constant == desired {
			yield(append([]bool(nil), values...))
		}
	case cover.BooleanExpressionNot:
		enumerateSubtreeResult(expression.Left, !desired, observed, values, yield)
	case cover.BooleanExpressionAnd:
		if desired {
			enumerateSubtreeResult(expression.Left, true, observed, values, func(left []bool) {
				enumerateSubtreeResult(expression.Right, true, observed, left, yield)
			})
			return
		}
		enumerateSubtreeResult(expression.Left, false, observed, values, func(left []bool) {
			enumerateAny(expression.Right, observed, left, yield)
		})
		enumerateSubtreeResult(expression.Left, true, observed, values, func(left []bool) {
			enumerateSubtreeResult(expression.Right, false, observed, left, yield)
		})
	case cover.BooleanExpressionOr:
		if !desired {
			enumerateSubtreeResult(expression.Left, false, observed, values, func(left []bool) {
				enumerateSubtreeResult(expression.Right, false, observed, left, yield)
			})
			return
		}
		enumerateSubtreeResult(expression.Left, true, observed, values, func(left []bool) {
			enumerateAny(expression.Right, observed, left, yield)
		})
		enumerateSubtreeResult(expression.Left, false, observed, values, func(left []bool) {
			enumerateSubtreeResult(expression.Right, true, observed, left, yield)
		})
	default:
		panic("mcdc: validated expression contains an unsupported node")
	}
}

func enumerateAny(expression *cover.BooleanExpression, observed []cover.ConditionState, values []bool, yield func([]bool)) {
	switch expression.Kind {
	case cover.BooleanExpressionCondition:
		if value, evaluated := observed[expression.ConditionIndex].Bool(); evaluated {
			next := append([]bool(nil), values...)
			next[expression.ConditionIndex] = value
			yield(next)
			return
		}
		for _, value := range []bool{false, true} {
			next := append([]bool(nil), values...)
			next[expression.ConditionIndex] = value
			yield(next)
		}
	case cover.BooleanExpressionConstant:
		yield(append([]bool(nil), values...))
	case cover.BooleanExpressionNot:
		enumerateAny(expression.Left, observed, values, yield)
	case cover.BooleanExpressionAnd, cover.BooleanExpressionOr:
		enumerateAny(expression.Left, observed, values, func(left []bool) {
			enumerateAny(expression.Right, observed, left, yield)
		})
	default:
		panic("mcdc: validated expression contains an unsupported node")
	}
}
