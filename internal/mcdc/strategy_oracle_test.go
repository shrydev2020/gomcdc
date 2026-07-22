package mcdc

import (
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"testing"

	cover "github.com/shrydev2020/gomcdc/internal/coverage"
)

const oracleDecisionID cover.DecisionID = 9001

const randomizedOracleSeed int64 = 0x4d43444320260001

// TestStrategiesAgainstGeneratedSemanticOracle checks the public strategy
// boundary against a deliberately slow, independent truth-table oracle. The
// generated read-once trees vary association, AND/OR, and nested negation;
// their observed vectors are produced with Go short-circuit semantics. Sparse
// suites preserve the distinction between not-covered and infeasible.
func TestStrategiesAgainstGeneratedSemanticOracle(t *testing.T) {
	for conditionCount := 1; conditionCount <= 3; conditionCount++ {
		expressions := oracleExpressions(0, conditionCount)
		for expressionIndex, expression := range expressions {
			allEvaluations := oracleEvaluations(expression, conditionCount)
			scenarios := []struct {
				name        string
				evaluations []cover.DecisionEvaluation
			}{
				{name: "full", evaluations: allEvaluations},
				{name: "alternating", evaluations: oracleAlternating(allEvaluations)},
				{name: "singleton", evaluations: append([]cover.DecisionEvaluation(nil), allEvaluations[:1]...)},
			}

			for _, scenario := range scenarios {
				name := fmt.Sprintf("conditions=%d/expression=%03d/%s", conditionCount, expressionIndex, scenario.name)
				t.Run(name, func(t *testing.T) {
					metadata := oracleMetadata(expression, conditionCount)
					strategies := []struct {
						name     string
						strategy MCDCStrategy
						masking  bool
					}{
						{name: "unique-cause", strategy: UniqueCauseStrategy{}},
						{name: "masking", strategy: MaskingStrategy{}, masking: true},
					}
					for _, strategy := range strategies {
						t.Run(strategy.name, func(t *testing.T) {
							result := strategy.strategy.Analyze(metadata, scenario.evaluations)
							oracleAssertStrategyResult(t, expression, conditionCount, scenario.evaluations, result, strategy.masking)

							reversed := append([]cover.DecisionEvaluation(nil), scenario.evaluations...)
							slices.Reverse(reversed)
							reversedResult := strategy.strategy.Analyze(metadata, reversed)
							if !reflect.DeepEqual(reversedResult, result) {
								t.Fatalf("analysis depends on evaluation order for %s\nforward: %#v\nreverse: %#v", oracleExpressionString(expression), result, reversedResult)
							}
						})
					}
				})
			}
		}
	}
}

// TestStrategiesAgainstRandomizedSemanticOracle extends the exhaustive small
// oracle with bounded 4-6 condition cases. A fixed seed keeps CI deterministic
// while random association, leaf order, operators, negation, and evidence
// subsets cover shapes that are too numerous to enumerate exhaustively.
func TestStrategiesAgainstRandomizedSemanticOracle(t *testing.T) {
	random := rand.New(rand.NewSource(randomizedOracleSeed))
	for caseIndex := 0; caseIndex < 12; caseIndex++ {
		conditionCount := 4 + caseIndex%3
		indexes := random.Perm(conditionCount)
		expression := oracleRandomExpression(random, indexes)
		evaluations := oracleEvaluations(expression, conditionCount)
		scenario := "full"
		if caseIndex%2 == 1 {
			scenario = "random-subset"
			evaluations = oracleRandomSubset(random, evaluations)
		}
		name := fmt.Sprintf(
			"seed=%x/case=%02d/conditions=%d/%s",
			randomizedOracleSeed,
			caseIndex,
			conditionCount,
			scenario,
		)
		t.Run(name, func(t *testing.T) {
			metadata := oracleMetadata(expression, conditionCount)
			strategies := []struct {
				name     string
				strategy MCDCStrategy
				masking  bool
			}{
				{name: "unique-cause", strategy: UniqueCauseStrategy{}},
				{name: "masking", strategy: MaskingStrategy{}, masking: true},
			}
			for _, strategy := range strategies {
				t.Run(strategy.name, func(t *testing.T) {
					result := strategy.strategy.Analyze(metadata, evaluations)
					oracleAssertStrategyResult(t, expression, conditionCount, evaluations, result, strategy.masking)

					reversed := append([]cover.DecisionEvaluation(nil), evaluations...)
					slices.Reverse(reversed)
					reversedResult := strategy.strategy.Analyze(metadata, reversed)
					if !reflect.DeepEqual(reversedResult, result) {
						t.Fatalf(
							"seed=%x case=%d expression=%s depends on evaluation order\nforward: %#v\nreverse: %#v",
							randomizedOracleSeed,
							caseIndex,
							oracleExpressionString(expression),
							result,
							reversedResult,
						)
					}
				})
			}
		})
	}
}

func oracleRandomExpression(random *rand.Rand, indexes []int) *cover.BooleanExpression {
	if len(indexes) == 1 {
		expression := cover.NewConditionExpression(uint16(indexes[0]))
		if random.Intn(3) == 0 {
			return cover.NewNotExpression(expression)
		}
		return expression
	}
	split := 1 + random.Intn(len(indexes)-1)
	kind := cover.BooleanExpressionAnd
	if random.Intn(2) == 1 {
		kind = cover.BooleanExpressionOr
	}
	expression := &cover.BooleanExpression{
		Kind:  kind,
		Left:  oracleRandomExpression(random, indexes[:split]),
		Right: oracleRandomExpression(random, indexes[split:]),
	}
	if random.Intn(3) == 0 {
		return cover.NewNotExpression(expression)
	}
	return expression
}

func oracleRandomSubset(random *rand.Rand, evaluations []cover.DecisionEvaluation) []cover.DecisionEvaluation {
	result := make([]cover.DecisionEvaluation, 0, len(evaluations))
	for _, evaluation := range evaluations {
		if random.Intn(3) != 0 {
			result = append(result, evaluation)
		}
	}
	if len(result) == 0 && len(evaluations) > 0 {
		result = append(result, evaluations[0])
	}
	return result
}

func oracleAssertStrategyResult(
	t *testing.T,
	expression *cover.BooleanExpression,
	conditionCount int,
	evaluations []cover.DecisionEvaluation,
	result cover.MCDCResult,
	masking bool,
) {
	t.Helper()
	if result.DecisionID != oracleDecisionID {
		t.Errorf("decision ID = %d, want %d", result.DecisionID, oracleDecisionID)
	}
	if len(result.Conditions) != conditionCount {
		t.Fatalf("condition results = %d, want %d", len(result.Conditions), conditionCount)
	}
	for target := 0; target < conditionCount; target++ {
		conditionResult := result.Conditions[target]
		if conditionResult.ConditionIndex != uint16(target) {
			t.Errorf("condition position %d has index %d", target, conditionResult.ConditionIndex)
		}
		if conditionResult.Support != cover.SupportSupported {
			t.Errorf("condition %d support = %q, want supported", target, conditionResult.Support)
		}

		covered := false
		structurallyFeasible := false
		if masking {
			covered = oracleMaskingCovered(expression, evaluations, target)
			structurallyFeasible = oracleStructurallyPivotal(expression, conditionCount, target)
		} else {
			covered = oracleUniqueCovered(evaluations, target)
			structurallyFeasible = oracleUniqueCovered(oracleEvaluations(expression, conditionCount), target)
		}

		wantOutcome := cover.CoverageOutcomeNotCovered
		wantAnalysis := cover.AnalysisComplete
		if covered {
			wantOutcome = cover.CoverageOutcomeCovered
		} else if !structurallyFeasible {
			wantAnalysis = cover.AnalysisInfeasible
		}
		if conditionResult.Outcome != wantOutcome || conditionResult.Analysis != wantAnalysis {
			t.Errorf(
				"condition %d for %s = (%s, %s), want (%s, %s); evaluations=%#v",
				target,
				oracleExpressionString(expression),
				conditionResult.Outcome,
				conditionResult.Analysis,
				wantOutcome,
				wantAnalysis,
				evaluations,
			)
			continue
		}
		if covered && conditionResult.Witness == nil {
			t.Errorf("covered condition %d has no witness", target)
			continue
		}
		if !covered && conditionResult.Witness != nil {
			t.Errorf("uncovered condition %d has witness %#v", target, conditionResult.Witness)
			continue
		}
		if conditionResult.Witness != nil {
			if !oracleContainsEvaluation(evaluations, conditionResult.Witness.First) ||
				!oracleContainsEvaluation(evaluations, conditionResult.Witness.Second) {
				t.Errorf("condition %d witness was not drawn from observed evidence: %#v", target, conditionResult.Witness)
				continue
			}
			valid := oracleUniquePair(conditionResult.Witness.First, conditionResult.Witness.Second, target)
			if masking {
				valid = oracleMaskingWitness(expression, conditionResult.Witness, target)
			}
			if !valid {
				t.Errorf("condition %d has invalid semantic witness %#v", target, conditionResult.Witness)
			}
		}
	}
}

func oracleContainsEvaluation(evaluations []cover.DecisionEvaluation, candidate cover.DecisionEvaluation) bool {
	for _, evaluation := range evaluations {
		if reflect.DeepEqual(evaluation, candidate) {
			return true
		}
	}
	return false
}

func oracleExpressions(start, count int) []*cover.BooleanExpression {
	if count == 1 {
		leaf := cover.NewConditionExpression(uint16(start))
		return []*cover.BooleanExpression{leaf, cover.NewNotExpression(cover.NewConditionExpression(uint16(start)))}
	}
	var expressions []*cover.BooleanExpression
	for leftCount := 1; leftCount < count; leftCount++ {
		leftExpressions := oracleExpressions(start, leftCount)
		rightExpressions := oracleExpressions(start+leftCount, count-leftCount)
		for _, left := range leftExpressions {
			for _, right := range rightExpressions {
				for _, kind := range []cover.BooleanExpressionKind{cover.BooleanExpressionAnd, cover.BooleanExpressionOr} {
					combined := &cover.BooleanExpression{Kind: kind, Left: oracleCloneExpression(left), Right: oracleCloneExpression(right)}
					expressions = append(expressions, combined, cover.NewNotExpression(oracleCloneExpression(combined)))
				}
			}
		}
	}
	return expressions
}

func oracleCloneExpression(expression *cover.BooleanExpression) *cover.BooleanExpression {
	if expression == nil {
		return nil
	}
	return &cover.BooleanExpression{
		Kind:           expression.Kind,
		ConditionIndex: expression.ConditionIndex,
		Constant:       expression.Constant,
		Left:           oracleCloneExpression(expression.Left),
		Right:          oracleCloneExpression(expression.Right),
	}
}

func oracleMetadata(expression *cover.BooleanExpression, conditionCount int) cover.DecisionMetadata {
	conditions := make([]cover.ConditionMetadata, conditionCount)
	for index := range conditions {
		conditions[index] = cover.ConditionMetadata{ID: cover.ConditionID(index + 1), Index: uint16(index)}
	}
	return cover.DecisionMetadata{
		ID:             oracleDecisionID,
		Conditions:     conditions,
		ExpressionTree: oracleCloneExpression(expression),
	}
}

func oracleEvaluations(expression *cover.BooleanExpression, conditionCount int) []cover.DecisionEvaluation {
	seen := make(map[string]struct{})
	var evaluations []cover.DecisionEvaluation
	for bits := 0; bits < 1<<conditionCount; bits++ {
		assignment := make([]bool, conditionCount)
		for index := range assignment {
			assignment[index] = bits&(1<<index) != 0
		}
		states := make([]cover.ConditionState, conditionCount)
		result := oracleObserve(expression, assignment, states)
		key := fmt.Sprint(states, result)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		evaluations = append(evaluations, cover.DecisionEvaluation{
			DecisionID:   oracleDecisionID,
			EvaluationID: cover.EvaluationID(len(evaluations) + 1),
			TestID:       "generated-oracle",
			Conditions:   states,
			Result:       result,
			Status:       cover.EvaluationCompleted,
		})
	}
	return evaluations
}

func oracleAlternating(evaluations []cover.DecisionEvaluation) []cover.DecisionEvaluation {
	result := make([]cover.DecisionEvaluation, 0, (len(evaluations)+1)/2)
	for index, evaluation := range evaluations {
		if index%2 == 0 {
			result = append(result, evaluation)
		}
	}
	return result
}

func oracleObserve(expression *cover.BooleanExpression, assignment []bool, states []cover.ConditionState) bool {
	switch expression.Kind {
	case cover.BooleanExpressionCondition:
		value := assignment[expression.ConditionIndex]
		if value {
			states[expression.ConditionIndex] = cover.ConditionTrue
		} else {
			states[expression.ConditionIndex] = cover.ConditionFalse
		}
		return value
	case cover.BooleanExpressionConstant:
		return expression.Constant
	case cover.BooleanExpressionNot:
		return !oracleObserve(expression.Left, assignment, states)
	case cover.BooleanExpressionAnd:
		if !oracleObserve(expression.Left, assignment, states) {
			return false
		}
		return oracleObserve(expression.Right, assignment, states)
	case cover.BooleanExpressionOr:
		if oracleObserve(expression.Left, assignment, states) {
			return true
		}
		return oracleObserve(expression.Right, assignment, states)
	default:
		panic("generated oracle contains unsupported expression")
	}
}

func oracleFull(expression *cover.BooleanExpression, assignment []bool) bool {
	switch expression.Kind {
	case cover.BooleanExpressionCondition:
		return assignment[expression.ConditionIndex]
	case cover.BooleanExpressionConstant:
		return expression.Constant
	case cover.BooleanExpressionNot:
		return !oracleFull(expression.Left, assignment)
	case cover.BooleanExpressionAnd:
		left := oracleFull(expression.Left, assignment)
		right := oracleFull(expression.Right, assignment)
		return left && right
	case cover.BooleanExpressionOr:
		left := oracleFull(expression.Left, assignment)
		right := oracleFull(expression.Right, assignment)
		return left || right
	default:
		panic("generated oracle contains unsupported expression")
	}
}

func oracleUniqueCovered(evaluations []cover.DecisionEvaluation, target int) bool {
	for first := 0; first < len(evaluations); first++ {
		for second := first + 1; second < len(evaluations); second++ {
			if oracleUniquePair(evaluations[first], evaluations[second], target) {
				return true
			}
		}
	}
	return false
}

func oracleUniquePair(first, second cover.DecisionEvaluation, target int) bool {
	if first.Result == second.Result ||
		!first.Conditions[target].IsEvaluated() ||
		!second.Conditions[target].IsEvaluated() ||
		first.Conditions[target] == second.Conditions[target] {
		return false
	}
	for index := range first.Conditions {
		if index != target && first.Conditions[index] != second.Conditions[index] {
			return false
		}
	}
	return true
}

func oracleMaskingCovered(expression *cover.BooleanExpression, evaluations []cover.DecisionEvaluation, target int) bool {
	for first := 0; first < len(evaluations); first++ {
		for second := first + 1; second < len(evaluations); second++ {
			if oracleMaskingPair(expression, evaluations[first], evaluations[second], target) {
				return true
			}
		}
	}
	return false
}

func oracleMaskingPair(expression *cover.BooleanExpression, first, second cover.DecisionEvaluation, target int) bool {
	if first.Result == second.Result ||
		!first.Conditions[target].IsEvaluated() ||
		!second.Conditions[target].IsEvaluated() ||
		first.Conditions[target] == second.Conditions[target] {
		return false
	}
	for _, firstCompletion := range oraclePivotalCompletions(expression, first, target) {
		for _, secondCompletion := range oraclePivotalCompletions(expression, second, target) {
			if oracleMaskingCompletionsCompatible(expression, firstCompletion, secondCompletion, target) {
				return true
			}
		}
	}
	return false
}

func oraclePivotalCompletions(expression *cover.BooleanExpression, evaluation cover.DecisionEvaluation, target int) [][]bool {
	conditionCount := len(evaluation.Conditions)
	var completions [][]bool
	for bits := 0; bits < 1<<conditionCount; bits++ {
		assignment := make([]bool, conditionCount)
		compatible := true
		for index, state := range evaluation.Conditions {
			assignment[index] = bits&(1<<index) != 0
			if value, evaluated := state.Bool(); evaluated && value != assignment[index] {
				compatible = false
				break
			}
		}
		if compatible && oracleFull(expression, assignment) == evaluation.Result && oraclePivotalAt(expression, assignment, target) {
			completions = append(completions, assignment)
		}
	}
	return completions
}

func oraclePivotalAt(expression *cover.BooleanExpression, assignment []bool, target int) bool {
	flipped := append([]bool(nil), assignment...)
	flipped[target] = !flipped[target]
	return oracleFull(expression, assignment) != oracleFull(expression, flipped)
}

func oracleStructurallyPivotal(expression *cover.BooleanExpression, conditionCount, target int) bool {
	for bits := 0; bits < 1<<conditionCount; bits++ {
		assignment := make([]bool, conditionCount)
		for index := range assignment {
			assignment[index] = bits&(1<<index) != 0
		}
		if oraclePivotalAt(expression, assignment, target) {
			return true
		}
	}
	return false
}

func oracleMaskingCompletionsCompatible(expression *cover.BooleanExpression, first, second []bool, target int) bool {
	for index := range first {
		if index == target || first[index] == second[index] {
			continue
		}
		if oraclePivotalAt(expression, first, index) || oraclePivotalAt(expression, second, index) {
			return false
		}
	}
	return true
}

func oracleMaskingWitness(expression *cover.BooleanExpression, witness *cover.MCDCWitness, target int) bool {
	if witness == nil || !oracleMaskingPair(expression, witness.First, witness.Second, target) {
		return false
	}
	first, firstOK := oracleCompletionBools(witness.FirstCompletion)
	second, secondOK := oracleCompletionBools(witness.SecondCompletion)
	if !firstOK || !secondOK ||
		!oracleCompletionMatches(expression, witness.First, first, target) ||
		!oracleCompletionMatches(expression, witness.Second, second, target) {
		return false
	}
	return oracleMaskingCompletionsCompatible(expression, first, second, target)
}

func oracleCompletionBools(states []cover.ConditionState) ([]bool, bool) {
	values := make([]bool, len(states))
	for index, state := range states {
		value, evaluated := state.Bool()
		if !evaluated {
			return nil, false
		}
		values[index] = value
	}
	return values, true
}

func oracleCompletionMatches(expression *cover.BooleanExpression, evaluation cover.DecisionEvaluation, completion []bool, target int) bool {
	if len(completion) != len(evaluation.Conditions) || oracleFull(expression, completion) != evaluation.Result || !oraclePivotalAt(expression, completion, target) {
		return false
	}
	for index, state := range evaluation.Conditions {
		if value, evaluated := state.Bool(); evaluated && value != completion[index] {
			return false
		}
	}
	return true
}

func oracleExpressionString(expression *cover.BooleanExpression) string {
	switch expression.Kind {
	case cover.BooleanExpressionCondition:
		return fmt.Sprintf("c%d", expression.ConditionIndex)
	case cover.BooleanExpressionConstant:
		return fmt.Sprintf("%t", expression.Constant)
	case cover.BooleanExpressionNot:
		return "!(" + oracleExpressionString(expression.Left) + ")"
	case cover.BooleanExpressionAnd:
		return "(" + oracleExpressionString(expression.Left) + " && " + oracleExpressionString(expression.Right) + ")"
	case cover.BooleanExpressionOr:
		return "(" + oracleExpressionString(expression.Left) + " || " + oracleExpressionString(expression.Right) + ")"
	default:
		return "<unsupported>"
	}
}
