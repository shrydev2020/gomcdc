package mcdc

import cover "github.com/shrydev2020/gomcdc/internal/coverage"

type completionResult struct {
	Values     []maskingCompletion
	Exhaustive bool
}

type maskingSearchBudget struct {
	limits         AnalysisBudget
	completions    uint64
	witnessPairs   uint64
	booleanChecks  uint64
	exceededReason string
}

func newMaskingSearchBudget(configured AnalysisBudget) *maskingSearchBudget {
	limits := configured
	defaults := DefaultMaskingAnalysisBudget()
	if limits.MaxCompletions == 0 {
		limits.MaxCompletions = defaults.MaxCompletions
	}
	if limits.MaxWitnessPairs == 0 {
		limits.MaxWitnessPairs = defaults.MaxWitnessPairs
	}
	if limits.MaxBooleanChecks == 0 {
		limits.MaxBooleanChecks = defaults.MaxBooleanChecks
	}
	return &maskingSearchBudget{limits: limits}
}

func (budget *maskingSearchBudget) consumeCompletion() bool {
	if budget.completions >= budget.limits.MaxCompletions {
		budget.exceed("completion count")
		return false
	}
	budget.completions++
	return true
}

func (budget *maskingSearchBudget) consumeWitnessPair() bool {
	if budget.witnessPairs >= budget.limits.MaxWitnessPairs {
		budget.exceed("witness-pair count")
		return false
	}
	budget.witnessPairs++
	return true
}

func (budget *maskingSearchBudget) consumeBooleanChecks(count uint64) bool {
	if count > budget.limits.MaxBooleanChecks-budget.booleanChecks {
		budget.exceed("Boolean-check count")
		return false
	}
	budget.booleanChecks += count
	return true
}

func (budget *maskingSearchBudget) exceed(resource string) {
	if budget.exceededReason == "" {
		budget.exceededReason = resource
	}
}

func (budget *maskingSearchBudget) reason() string {
	if budget.exceededReason == "" {
		return "masking MC/DC completion search did not finish"
	}
	return "masking MC/DC completion search exceeded configured " + budget.exceededReason + " limit"
}

// enumeratePivotalCompletionsBounded enumerates assignments compatible with
// the observed short-circuit path that make target condition pivotal.
func enumeratePivotalCompletionsBounded(expression *cover.BooleanExpression, evaluation cover.DecisionEvaluation, target uint16, budget *maskingSearchBudget) completionResult {
	if int(target) >= len(evaluation.Conditions) || !evaluation.Conditions[target].IsEvaluated() {
		return completionResult{Exhaustive: true}
	}
	values := make([]bool, len(evaluation.Conditions))
	for index, state := range evaluation.Conditions {
		if value, evaluated := state.Bool(); evaluated {
			values[index] = value
		}
	}
	steps := make([]pivotalPathStep, 0)
	if !findPivotalPath(expression, target, &steps) {
		return completionResult{Exhaustive: true}
	}
	completions := make([]maskingCompletion, 0)
	seen := make(map[string]struct{})
	var visit func(int, []bool) bool
	visit = func(stepIndex int, current []bool) bool {
		if stepIndex == len(steps) {
			if !budget.consumeBooleanChecks(3) {
				return false
			}
			states := make([]cover.ConditionState, len(current))
			result := evaluateCompletion(expression, current, states)
			if result != evaluation.Result || !sameConditionStates(states, evaluation.Conditions) || !isPivotal(expression, current, target) {
				return true
			}
			key := boolVectorKey(current)
			if _, exists := seen[key]; exists {
				return true
			}
			if !budget.consumeCompletion() {
				return false
			}
			seen[key] = struct{}{}
			completions = append(completions, maskingCompletion{values: current})
			return true
		}
		step := steps[stepIndex]
		return enumerateSubtreeResult(step.sibling, step.desired, evaluation.Conditions, current, func(next []bool) bool {
			return visit(stepIndex+1, next)
		})
	}
	exhaustive := visit(0, values)
	return completionResult{Values: completions, Exhaustive: exhaustive}
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

func enumerateSubtreeResult(expression *cover.BooleanExpression, desired bool, observed []cover.ConditionState, values []bool, yield func([]bool) bool) bool {
	switch expression.Kind {
	case cover.BooleanExpressionCondition:
		next := append([]bool(nil), values...)
		if value, evaluated := observed[expression.ConditionIndex].Bool(); evaluated {
			if value != desired {
				return true
			}
			next[expression.ConditionIndex] = value
		} else {
			next[expression.ConditionIndex] = desired
		}
		return yield(next)
	case cover.BooleanExpressionConstant:
		if expression.Constant == desired {
			return yield(append([]bool(nil), values...))
		}
		return true
	case cover.BooleanExpressionNot:
		return enumerateSubtreeResult(expression.Left, !desired, observed, values, yield)
	case cover.BooleanExpressionAnd:
		if desired {
			return enumerateSubtreeResult(expression.Left, true, observed, values, func(left []bool) bool {
				return enumerateSubtreeResult(expression.Right, true, observed, left, yield)
			})
		}
		if !enumerateSubtreeResult(expression.Left, false, observed, values, func(left []bool) bool {
			return enumerateAny(expression.Right, observed, left, yield)
		}) {
			return false
		}
		return enumerateSubtreeResult(expression.Left, true, observed, values, func(left []bool) bool {
			return enumerateSubtreeResult(expression.Right, false, observed, left, yield)
		})
	case cover.BooleanExpressionOr:
		if !desired {
			return enumerateSubtreeResult(expression.Left, false, observed, values, func(left []bool) bool {
				return enumerateSubtreeResult(expression.Right, false, observed, left, yield)
			})
		}
		if !enumerateSubtreeResult(expression.Left, true, observed, values, func(left []bool) bool {
			return enumerateAny(expression.Right, observed, left, yield)
		}) {
			return false
		}
		return enumerateSubtreeResult(expression.Left, false, observed, values, func(left []bool) bool {
			return enumerateSubtreeResult(expression.Right, true, observed, left, yield)
		})
	default:
		panic("mcdc: validated expression contains an unsupported node")
	}
}

func enumerateAny(expression *cover.BooleanExpression, observed []cover.ConditionState, values []bool, yield func([]bool) bool) bool {
	switch expression.Kind {
	case cover.BooleanExpressionCondition:
		if value, evaluated := observed[expression.ConditionIndex].Bool(); evaluated {
			next := append([]bool(nil), values...)
			next[expression.ConditionIndex] = value
			return yield(next)
		}
		for _, value := range []bool{false, true} {
			next := append([]bool(nil), values...)
			next[expression.ConditionIndex] = value
			if !yield(next) {
				return false
			}
		}
		return true
	case cover.BooleanExpressionConstant:
		return yield(append([]bool(nil), values...))
	case cover.BooleanExpressionNot:
		return enumerateAny(expression.Left, observed, values, yield)
	case cover.BooleanExpressionAnd, cover.BooleanExpressionOr:
		return enumerateAny(expression.Left, observed, values, func(left []bool) bool {
			return enumerateAny(expression.Right, observed, left, yield)
		})
	default:
		panic("mcdc: validated expression contains an unsupported node")
	}
}
