// Package mcdc analyzes completed decision evaluation vectors without reading
// or mutating runtime state.
package mcdc

import (
	"fmt"
	"sort"

	cover "github.com/shrydev2020/gomcdc/internal/coverage"
)

// MCDCStrategy is the consumer-owned boundary for an MC/DC definition.
// Implementations must be pure and deterministic for equivalent inputs.
type MCDCStrategy interface {
	Analyze(metadata cover.DecisionMetadata, evaluations []cover.DecisionEvaluation) cover.MCDCResult
}

// UniqueCauseStrategy requires every non-target observed condition state to be
// identical in the witness pair. Not-evaluated is a distinct state.
type UniqueCauseStrategy struct{}

// MaskingStrategy uses the decision expression's Boolean difference. A target
// must be pivotal in both completed witness vectors; not-evaluated values are
// completed counterfactually without changing either recorded decision result.
type MaskingStrategy struct{}

var _ MCDCStrategy = UniqueCauseStrategy{}
var _ MCDCStrategy = MaskingStrategy{}

type analysisIssue struct {
	status cover.CoverageStatus
	reason string
}

type preparedEvaluations struct {
	evaluations []cover.DecisionEvaluation
	aborted     int
	invalid     int
}

// ValidateCompletedEvaluation verifies that one completed runtime vector is a
// structurally possible short-circuit evaluation of metadata and that its
// recorded decision result is truthful. It is the shared trust-boundary check
// used before Decision, Condition, or MC/DC evidence is accepted.
func ValidateCompletedEvaluation(metadata cover.DecisionMetadata, evaluation cover.DecisionEvaluation) error {
	if evaluation.Status != cover.EvaluationCompleted {
		return nil
	}
	if evaluation.DecisionID != metadata.ID {
		return fmt.Errorf("evaluation decision ID %d does not match metadata %d", evaluation.DecisionID, metadata.ID)
	}
	if len(evaluation.Conditions) != len(metadata.Conditions) {
		return fmt.Errorf("evaluation has %d conditions, want %d", len(evaluation.Conditions), len(metadata.Conditions))
	}
	for index, state := range evaluation.Conditions {
		if state != cover.ConditionNotEvaluated && state != cover.ConditionFalse && state != cover.ConditionTrue {
			return fmt.Errorf("condition %d has invalid state %d", index, state)
		}
	}
	if metadata.ExpressionTree == nil {
		// Future decision kinds may deliberately lack a supported expression
		// cover. They remain reportable as unsupported, not corrupt evidence.
		return nil
	}
	if issue := validateExpression(metadata.ExpressionTree, len(metadata.Conditions)); issue != nil {
		return fmt.Errorf("invalid decision expression metadata: %s", issue.reason)
	}
	result, err := evaluateObserved(metadata.ExpressionTree, evaluation.Conditions)
	if err != nil {
		return err
	}
	if result != evaluation.Result {
		return fmt.Errorf("recorded decision result %t, expression evaluates to %t", evaluation.Result, result)
	}
	return nil
}

// Analyze applies strict Unique-Cause MC/DC to completed evaluations.
func (UniqueCauseStrategy) Analyze(metadata cover.DecisionMetadata, evaluations []cover.DecisionEvaluation) cover.MCDCResult {
	count, indexes, issue := conditionLayout(metadata, evaluations)
	result := baseResult(metadata.ID, cover.CoverageMetricMCDCUnique, indexes)
	defer enrichResult(&result)
	if issue != nil {
		applyIssue(&result, issue)
		return result
	}

	prepared := prepareEvaluations(metadata.ID, count, evaluations)
	result.AbortedEvaluations = prepared.aborted
	result.InvalidEvaluations = prepared.invalid

	// When valid structure is available, structurally impossible short-circuit
	// vectors cannot become MC/DC evidence. Unique-Cause itself does not require
	// an expression tree, so malformed or absent optional structure is ignored.
	expressionStructureValid := false
	if metadata.ExpressionTree != nil {
		if treeIssue := validateExpression(metadata.ExpressionTree, count); treeIssue == nil {
			expressionStructureValid = true
			prepared.evaluations, result.InvalidEvaluations = filterObservedEvaluations(
				metadata.ExpressionTree,
				prepared.evaluations,
				result.InvalidEvaluations,
			)
		}
	}
	result.EvaluationsAnalyzed = len(prepared.evaluations)

	for conditionPosition, target := range indexes {
		conditionResult := &result.Conditions[conditionPosition]
		for first := 0; first < len(prepared.evaluations); first++ {
			for second := first + 1; second < len(prepared.evaluations); second++ {
				left := prepared.evaluations[first]
				right := prepared.evaluations[second]
				if uniqueCausePair(left, right, target) {
					conditionResult.Status = cover.CoverageCovered
					conditionResult.Witness = &cover.MCDCWitness{
						First:  cloneEvaluation(left),
						Second: cloneEvaluation(right),
					}
					break
				}
			}
			if conditionResult.Witness != nil {
				break
			}
		}
		if conditionResult.Witness == nil {
			if expressionStructureValid && !uniqueCauseStructurallyFeasible(metadata.ExpressionTree, target, count) {
				conditionResult.Status = cover.CoveragePossiblyInfeasible
				conditionResult.Reason = "short-circuit evaluation prevents an equal-state Unique-Cause pair for this condition"
			} else {
				conditionResult.Reason = "no pair changes only the evaluated target condition and the decision result"
			}
		}
	}

	finishResult(&result)
	return result
}

// Analyze applies Masking MC/DC using the Boolean expression tree. Merely
// allowing arbitrary non-target differences is insufficient: the target must
// be pivotal (its Boolean difference must be true) in both completed vectors.
func (MaskingStrategy) Analyze(metadata cover.DecisionMetadata, evaluations []cover.DecisionEvaluation) cover.MCDCResult {
	count, indexes, issue := conditionLayout(metadata, evaluations)
	result := baseResult(metadata.ID, cover.CoverageMetricMCDCMasking, indexes)
	defer enrichResult(&result)
	if issue != nil {
		applyIssue(&result, issue)
		return result
	}
	if metadata.ExpressionTree == nil {
		applyIssue(&result, &analysisIssue{
			status: cover.CoverageUnknown,
			reason: "masking MC/DC requires a boolean expression tree",
		})
		return result
	}
	if treeIssue := validateExpression(metadata.ExpressionTree, count); treeIssue != nil {
		applyIssue(&result, treeIssue)
		return result
	}
	if repeated := repeatedAtomicExpression(metadata.Conditions); repeated != "" {
		applyIssue(&result, &analysisIssue{
			status: cover.CoverageUnknown,
			reason: fmt.Sprintf("masking MC/DC cannot infer value coupling for repeated atomic expression %q", repeated),
		})
		return result
	}

	prepared := prepareEvaluations(metadata.ID, count, evaluations)
	result.AbortedEvaluations = prepared.aborted
	result.InvalidEvaluations = prepared.invalid
	prepared.evaluations, result.InvalidEvaluations = filterObservedEvaluations(
		metadata.ExpressionTree,
		prepared.evaluations,
		result.InvalidEvaluations,
	)
	result.EvaluationsAnalyzed = len(prepared.evaluations)

	for conditionPosition, target := range indexes {
		conditionResult := &result.Conditions[conditionPosition]
		for first := 0; first < len(prepared.evaluations); first++ {
			for second := first + 1; second < len(prepared.evaluations); second++ {
				left := prepared.evaluations[first]
				right := prepared.evaluations[second]
				if !candidatePair(left, right, target) {
					continue
				}
				witness, covered := maskingWitness(metadata.ExpressionTree, left, right, target)
				if covered {
					conditionResult.Status = cover.CoverageCovered
					conditionResult.Witness = witness
					break
				}
			}
			if conditionResult.Witness != nil {
				break
			}
		}
		if conditionResult.Witness == nil {
			if !structurallyPivotal(metadata.ExpressionTree, target, count) {
				conditionResult.Status = cover.CoveragePossiblyInfeasible
				conditionResult.Reason = "the boolean expression cannot make this condition pivotal"
			} else {
				conditionResult.Reason = "no pair makes the evaluated target pivotal in both completed vectors"
			}
		}
	}

	finishResult(&result)
	return result
}

func repeatedAtomicExpression(conditions []cover.ConditionMetadata) string {
	seen := make(map[string]struct{}, len(conditions))
	for _, condition := range conditions {
		expression := condition.Expression
		if expression == "" {
			continue
		}
		if _, exists := seen[expression]; exists {
			return expression
		}
		seen[expression] = struct{}{}
	}
	return ""
}

func uniqueCausePair(first, second cover.DecisionEvaluation, target uint16) bool {
	if !candidatePair(first, second, target) {
		return false
	}
	for index := range first.Conditions {
		if uint16(index) == target {
			continue
		}
		if first.Conditions[index] != second.Conditions[index] {
			return false
		}
	}
	return true
}

func candidatePair(first, second cover.DecisionEvaluation, target uint16) bool {
	if first.Result == second.Result {
		return false
	}
	firstTarget := first.Conditions[target]
	secondTarget := second.Conditions[target]
	return firstTarget.IsEvaluated() &&
		secondTarget.IsEvaluated() &&
		firstTarget != secondTarget
}

func maskingWitness(
	expression *cover.BooleanExpression,
	first cover.DecisionEvaluation,
	second cover.DecisionEvaluation,
	target uint16,
) (*cover.MCDCWitness, bool) {
	firstCompletion, firstPivotal := pivotalCompletion(expression, first, target)
	if !firstPivotal {
		return nil, false
	}
	secondCompletion, secondPivotal := pivotalCompletion(expression, second, target)
	if !secondPivotal {
		return nil, false
	}

	unobserved := make([]uint16, 0)
	masked := make([]uint16, 0)
	for index := range first.Conditions {
		conditionIndex := uint16(index)
		if conditionIndex == target {
			continue
		}
		if first.Conditions[index] == cover.ConditionNotEvaluated ||
			second.Conditions[index] == cover.ConditionNotEvaluated {
			unobserved = append(unobserved, conditionIndex)
		}
		if firstCompletion[index] != secondCompletion[index] {
			masked = append(masked, conditionIndex)
		}
	}

	return &cover.MCDCWitness{
		First:                cloneEvaluation(first),
		Second:               cloneEvaluation(second),
		FirstCompletion:      statesFromBools(firstCompletion),
		SecondCompletion:     statesFromBools(secondCompletion),
		UnobservedConditions: unobserved,
		MaskedConditions:     masked,
	}, true
}

// uniqueCauseStructurallyFeasible is a conservative proof over the expression
// tree, independent of observed tests. If a target lies in the left operand of
// a short-circuit operator, changing that operand's controlling value changes
// every condition in the right operand between evaluated and not-evaluated.
// Such states can never be identical under the strict Unique-Cause rule.
func uniqueCauseStructurallyFeasible(expression *cover.BooleanExpression, target uint16, conditionCount int) bool {
	if !structurallyPivotal(expression, target, conditionCount) {
		return false
	}
	_, compatible := uniqueCauseEvaluationCompatible(expression, target)
	return compatible
}

func uniqueCauseEvaluationCompatible(expression *cover.BooleanExpression, target uint16) (containsTarget, compatible bool) {
	switch expression.Kind {
	case cover.BooleanExpressionCondition:
		return expression.ConditionIndex == target, true
	case cover.BooleanExpressionConstant:
		return false, true
	case cover.BooleanExpressionNot:
		return uniqueCauseEvaluationCompatible(expression.Left, target)
	case cover.BooleanExpressionAnd, cover.BooleanExpressionOr:
		leftContains, leftCompatible := uniqueCauseEvaluationCompatible(expression.Left, target)
		if leftContains {
			return true, leftCompatible && !containsCondition(expression.Right)
		}
		rightContains, rightCompatible := uniqueCauseEvaluationCompatible(expression.Right, target)
		return rightContains, rightCompatible
	default:
		return false, false
	}
}

func containsCondition(expression *cover.BooleanExpression) bool {
	switch expression.Kind {
	case cover.BooleanExpressionCondition:
		return true
	case cover.BooleanExpressionConstant:
		return false
	case cover.BooleanExpressionNot:
		return containsCondition(expression.Left)
	case cover.BooleanExpressionAnd, cover.BooleanExpressionOr:
		return containsCondition(expression.Left) || containsCondition(expression.Right)
	default:
		return false
	}
}

// structurallyPivotal proves that some complete condition assignment can make
// target's Boolean difference true. It is linear in the expression size and
// therefore does not introduce an exponential enumeration cutoff.
func structurallyPivotal(expression *cover.BooleanExpression, target uint16, conditionCount int) bool {
	steps := make([]pivotalPathStep, 0)
	if !findPivotalPath(expression, target, &steps) {
		return false
	}
	states := make([]cover.ConditionState, conditionCount)
	feasible := make(map[*cover.BooleanExpression]subtreeFeasibility)
	computeFeasibility(expression, states, feasible)
	for _, step := range steps {
		if !canTakeValue(feasible[step.sibling], step.desired) {
			return false
		}
	}
	return true
}

type subtreeFeasibility struct {
	falseValue bool
	trueValue  bool
}

type pivotalPathStep struct {
	sibling *cover.BooleanExpression
	desired bool
}

// pivotalCompletion solves a read-once expression in linear time. For the
// target to propagate to the root, each sibling on its path must be neutral:
// true beside AND and false beside OR. NOT needs no sibling constraint.
func pivotalCompletion(
	expression *cover.BooleanExpression,
	evaluation cover.DecisionEvaluation,
	target uint16,
) ([]bool, bool) {
	steps := make([]pivotalPathStep, 0)
	if !findPivotalPath(expression, target, &steps) {
		return nil, false
	}

	feasible := make(map[*cover.BooleanExpression]subtreeFeasibility)
	computeFeasibility(expression, evaluation.Conditions, feasible)
	values := make([]bool, len(evaluation.Conditions))
	for index, state := range evaluation.Conditions {
		if value, evaluated := state.Bool(); evaluated {
			values[index] = value
		}
	}
	targetValue, evaluated := evaluation.Conditions[target].Bool()
	if !evaluated {
		return nil, false
	}
	values[target] = targetValue

	for _, step := range steps {
		possibility := feasible[step.sibling]
		if !canTakeValue(possibility, step.desired) {
			return nil, false
		}
		assignSubtree(step.sibling, step.desired, feasible, values)
	}

	if evaluateFull(expression, values, -1, false) != evaluation.Result ||
		!isPivotal(expression, values, target) {
		return nil, false
	}
	return values, true
}

func findPivotalPath(
	expression *cover.BooleanExpression,
	target uint16,
	steps *[]pivotalPathStep,
) bool {
	switch expression.Kind {
	case cover.BooleanExpressionCondition:
		return expression.ConditionIndex == target
	case cover.BooleanExpressionConstant:
		return false
	case cover.BooleanExpressionNot:
		return findPivotalPath(expression.Left, target, steps)
	case cover.BooleanExpressionAnd:
		if findPivotalPath(expression.Left, target, steps) {
			*steps = append(*steps, pivotalPathStep{sibling: expression.Right, desired: true})
			return true
		}
		if findPivotalPath(expression.Right, target, steps) {
			*steps = append(*steps, pivotalPathStep{sibling: expression.Left, desired: true})
			return true
		}
		return false
	case cover.BooleanExpressionOr:
		if findPivotalPath(expression.Left, target, steps) {
			*steps = append(*steps, pivotalPathStep{sibling: expression.Right, desired: false})
			return true
		}
		if findPivotalPath(expression.Right, target, steps) {
			*steps = append(*steps, pivotalPathStep{sibling: expression.Left, desired: false})
			return true
		}
		return false
	default:
		panic("mcdc: validated expression contains an unsupported node")
	}
}

func computeFeasibility(
	expression *cover.BooleanExpression,
	states []cover.ConditionState,
	feasible map[*cover.BooleanExpression]subtreeFeasibility,
) subtreeFeasibility {
	var result subtreeFeasibility
	switch expression.Kind {
	case cover.BooleanExpressionCondition:
		switch states[expression.ConditionIndex] {
		case cover.ConditionNotEvaluated:
			result = subtreeFeasibility{falseValue: true, trueValue: true}
		case cover.ConditionFalse:
			result.falseValue = true
		case cover.ConditionTrue:
			result.trueValue = true
		}
	case cover.BooleanExpressionConstant:
		if expression.Constant {
			result.trueValue = true
		} else {
			result.falseValue = true
		}
	case cover.BooleanExpressionNot:
		child := computeFeasibility(expression.Left, states, feasible)
		result = subtreeFeasibility{falseValue: child.trueValue, trueValue: child.falseValue}
	case cover.BooleanExpressionAnd:
		left := computeFeasibility(expression.Left, states, feasible)
		right := computeFeasibility(expression.Right, states, feasible)
		result.trueValue = left.trueValue && right.trueValue
		result.falseValue = left.falseValue && (right.falseValue || right.trueValue) ||
			(left.falseValue || left.trueValue) && right.falseValue
	case cover.BooleanExpressionOr:
		left := computeFeasibility(expression.Left, states, feasible)
		right := computeFeasibility(expression.Right, states, feasible)
		result.falseValue = left.falseValue && right.falseValue
		result.trueValue = left.trueValue && (right.falseValue || right.trueValue) ||
			(left.falseValue || left.trueValue) && right.trueValue
	default:
		panic("mcdc: validated expression contains an unsupported node")
	}
	feasible[expression] = result
	return result
}

func assignSubtree(
	expression *cover.BooleanExpression,
	desired bool,
	feasible map[*cover.BooleanExpression]subtreeFeasibility,
	values []bool,
) {
	switch expression.Kind {
	case cover.BooleanExpressionCondition:
		values[expression.ConditionIndex] = desired
	case cover.BooleanExpressionConstant:
		return
	case cover.BooleanExpressionNot:
		assignSubtree(expression.Left, !desired, feasible, values)
	case cover.BooleanExpressionAnd:
		if desired {
			assignSubtree(expression.Left, true, feasible, values)
			assignSubtree(expression.Right, true, feasible, values)
			return
		}
		left, right := chooseChildValues(expression.Left, expression.Right, feasible, [][2]bool{
			{false, false},
			{false, true},
			{true, false},
		})
		assignSubtree(expression.Left, left, feasible, values)
		assignSubtree(expression.Right, right, feasible, values)
	case cover.BooleanExpressionOr:
		if !desired {
			assignSubtree(expression.Left, false, feasible, values)
			assignSubtree(expression.Right, false, feasible, values)
			return
		}
		left, right := chooseChildValues(expression.Left, expression.Right, feasible, [][2]bool{
			{false, true},
			{true, false},
			{true, true},
		})
		assignSubtree(expression.Left, left, feasible, values)
		assignSubtree(expression.Right, right, feasible, values)
	default:
		panic("mcdc: validated expression contains an unsupported node")
	}
}

func chooseChildValues(
	left *cover.BooleanExpression,
	right *cover.BooleanExpression,
	feasible map[*cover.BooleanExpression]subtreeFeasibility,
	candidates [][2]bool,
) (bool, bool) {
	for _, candidate := range candidates {
		if canTakeValue(feasible[left], candidate[0]) && canTakeValue(feasible[right], candidate[1]) {
			return candidate[0], candidate[1]
		}
	}
	panic("mcdc: assignSubtree called for an infeasible result")
}

func canTakeValue(feasible subtreeFeasibility, value bool) bool {
	if value {
		return feasible.trueValue
	}
	return feasible.falseValue
}

func isPivotal(expression *cover.BooleanExpression, values []bool, target uint16) bool {
	actual := evaluateFull(expression, values, -1, false)
	flipped := evaluateFull(expression, values, int(target), !values[target])
	return actual != flipped
}

// evaluateFull evaluates a complete vector. A non-negative overrideIndex
// replaces one leaf value without mutating the caller's vector.
func evaluateFull(expression *cover.BooleanExpression, values []bool, overrideIndex int, override bool) bool {
	switch expression.Kind {
	case cover.BooleanExpressionCondition:
		if int(expression.ConditionIndex) == overrideIndex {
			return override
		}
		return values[expression.ConditionIndex]
	case cover.BooleanExpressionConstant:
		return expression.Constant
	case cover.BooleanExpressionNot:
		return !evaluateFull(expression.Left, values, overrideIndex, override)
	case cover.BooleanExpressionAnd:
		return evaluateFull(expression.Left, values, overrideIndex, override) &&
			evaluateFull(expression.Right, values, overrideIndex, override)
	case cover.BooleanExpressionOr:
		return evaluateFull(expression.Left, values, overrideIndex, override) ||
			evaluateFull(expression.Right, values, overrideIndex, override)
	default:
		panic("mcdc: validated expression contains an unsupported node")
	}
}

func filterObservedEvaluations(
	expression *cover.BooleanExpression,
	evaluations []cover.DecisionEvaluation,
	invalid int,
) ([]cover.DecisionEvaluation, int) {
	filtered := make([]cover.DecisionEvaluation, 0, len(evaluations))
	for _, evaluation := range evaluations {
		result, err := evaluateObserved(expression, evaluation.Conditions)
		if err != nil || result != evaluation.Result {
			invalid++
			continue
		}
		filtered = append(filtered, evaluation)
	}
	return filtered, invalid
}

// evaluateObserved replays the expression's short-circuit control flow. A
// reached condition must be observed, and every skipped subtree must contain
// only not-evaluated condition states.
func evaluateObserved(expression *cover.BooleanExpression, states []cover.ConditionState) (bool, error) {
	switch expression.Kind {
	case cover.BooleanExpressionCondition:
		value, evaluated := states[expression.ConditionIndex].Bool()
		if !evaluated {
			return false, fmt.Errorf("condition %d was reached but not evaluated", expression.ConditionIndex)
		}
		return value, nil
	case cover.BooleanExpressionConstant:
		return expression.Constant, nil
	case cover.BooleanExpressionNot:
		value, err := evaluateObserved(expression.Left, states)
		return !value, err
	case cover.BooleanExpressionAnd:
		left, err := evaluateObserved(expression.Left, states)
		if err != nil {
			return false, err
		}
		if !left {
			if err := requireSkipped(expression.Right, states); err != nil {
				return false, err
			}
			return false, nil
		}
		return evaluateObserved(expression.Right, states)
	case cover.BooleanExpressionOr:
		left, err := evaluateObserved(expression.Left, states)
		if err != nil {
			return false, err
		}
		if left {
			if err := requireSkipped(expression.Right, states); err != nil {
				return false, err
			}
			return true, nil
		}
		return evaluateObserved(expression.Right, states)
	default:
		return false, fmt.Errorf("unsupported boolean expression kind %q", expression.Kind)
	}
}

func requireSkipped(expression *cover.BooleanExpression, states []cover.ConditionState) error {
	switch expression.Kind {
	case cover.BooleanExpressionCondition:
		if states[expression.ConditionIndex] != cover.ConditionNotEvaluated {
			return fmt.Errorf("condition %d was observed in a skipped subtree", expression.ConditionIndex)
		}
		return nil
	case cover.BooleanExpressionConstant:
		return nil
	case cover.BooleanExpressionNot:
		return requireSkipped(expression.Left, states)
	case cover.BooleanExpressionAnd, cover.BooleanExpressionOr:
		if err := requireSkipped(expression.Left, states); err != nil {
			return err
		}
		return requireSkipped(expression.Right, states)
	default:
		return fmt.Errorf("unsupported boolean expression kind %q", expression.Kind)
	}
}

func conditionLayout(
	metadata cover.DecisionMetadata,
	evaluations []cover.DecisionEvaluation,
) (int, []uint16, *analysisIssue) {
	if len(metadata.Conditions) > 0 {
		count := len(metadata.Conditions)
		seen := make([]bool, count)
		for _, condition := range metadata.Conditions {
			if int(condition.Index) >= count || seen[condition.Index] {
				return count, indexesForCount(count), &analysisIssue{
					status: cover.CoverageUnknown,
					reason: "condition metadata indexes must be unique and contiguous from zero",
				}
			}
			seen[condition.Index] = true
		}
		return count, indexesForCount(count), nil
	}

	if metadata.ExpressionTree != nil {
		indexes, issue := expressionIndexes(metadata.ExpressionTree)
		if issue != nil {
			return len(indexes), indexes, issue
		}
		if len(indexes) == 0 {
			return 0, nil, &analysisIssue{status: cover.CoverageUnknown, reason: "decision has no atomic conditions"}
		}
		for position, index := range indexes {
			if int(index) != position {
				return len(indexes), indexes, &analysisIssue{
					status: cover.CoverageUnknown,
					reason: "expression condition indexes must be contiguous from zero",
				}
			}
		}
		return len(indexes), indexes, nil
	}

	count := -1
	for _, evaluation := range evaluations {
		if evaluation.DecisionID != metadata.ID || len(evaluation.Conditions) == 0 {
			continue
		}
		if count == -1 {
			count = len(evaluation.Conditions)
			continue
		}
		if len(evaluation.Conditions) != count {
			return count, indexesForCount(count), &analysisIssue{
				status: cover.CoverageUnknown,
				reason: "condition metadata is absent and evaluation vector lengths disagree",
			}
		}
	}
	if count <= 0 {
		return 0, nil, &analysisIssue{status: cover.CoverageUnknown, reason: "condition metadata is absent"}
	}
	return count, indexesForCount(count), nil
}

func indexesForCount(count int) []uint16 {
	indexes := make([]uint16, count)
	for index := range indexes {
		indexes[index] = uint16(index)
	}
	return indexes
}

func expressionIndexes(expression *cover.BooleanExpression) ([]uint16, *analysisIssue) {
	seen := make(map[uint16]struct{})
	visiting := make(map[*cover.BooleanExpression]bool)
	if issue := inspectExpression(expression, seen, visiting); issue != nil {
		indexes := make([]uint16, 0, len(seen))
		for index := range seen {
			indexes = append(indexes, index)
		}
		sort.Slice(indexes, func(i, j int) bool { return indexes[i] < indexes[j] })
		return indexes, issue
	}
	indexes := make([]uint16, 0, len(seen))
	for index := range seen {
		indexes = append(indexes, index)
	}
	sort.Slice(indexes, func(i, j int) bool { return indexes[i] < indexes[j] })
	return indexes, nil
}

func inspectExpression(
	expression *cover.BooleanExpression,
	seen map[uint16]struct{},
	visiting map[*cover.BooleanExpression]bool,
) *analysisIssue {
	if expression == nil {
		return &analysisIssue{status: cover.CoverageUnknown, reason: "boolean expression contains a nil operand"}
	}
	if visiting[expression] {
		return &analysisIssue{status: cover.CoverageUnknown, reason: "boolean expression contains a cycle"}
	}
	visiting[expression] = true
	defer delete(visiting, expression)

	switch expression.Kind {
	case cover.BooleanExpressionCondition:
		if expression.Left != nil || expression.Right != nil {
			return &analysisIssue{status: cover.CoverageUnknown, reason: "condition expression must be a leaf"}
		}
		if _, duplicate := seen[expression.ConditionIndex]; duplicate {
			return &analysisIssue{status: cover.CoverageUnknown, reason: "each atomic occurrence must have a unique condition index"}
		}
		seen[expression.ConditionIndex] = struct{}{}
		return nil
	case cover.BooleanExpressionConstant:
		if expression.Left != nil || expression.Right != nil {
			return &analysisIssue{status: cover.CoverageUnknown, reason: "constant expression must be a leaf"}
		}
		return nil
	case cover.BooleanExpressionNot:
		if expression.Left == nil || expression.Right != nil {
			return &analysisIssue{status: cover.CoverageUnknown, reason: "not expression must have exactly one operand"}
		}
		return inspectExpression(expression.Left, seen, visiting)
	case cover.BooleanExpressionAnd, cover.BooleanExpressionOr:
		if expression.Left == nil || expression.Right == nil {
			return &analysisIssue{status: cover.CoverageUnknown, reason: "and/or expression must have two operands"}
		}
		if issue := inspectExpression(expression.Left, seen, visiting); issue != nil {
			return issue
		}
		return inspectExpression(expression.Right, seen, visiting)
	default:
		return &analysisIssue{
			status: cover.CoverageUnsupported,
			reason: fmt.Sprintf("unsupported boolean expression kind %q", expression.Kind),
		}
	}
}

func validateExpression(expression *cover.BooleanExpression, count int) *analysisIssue {
	indexes, issue := expressionIndexes(expression)
	if issue != nil {
		return issue
	}
	if len(indexes) != count {
		return &analysisIssue{
			status: cover.CoverageUnknown,
			reason: "boolean expression and condition metadata have different condition counts",
		}
	}
	for position, index := range indexes {
		if int(index) != position {
			return &analysisIssue{
				status: cover.CoverageUnknown,
				reason: "boolean expression condition indexes must be contiguous from zero",
			}
		}
	}
	return nil
}

func prepareEvaluations(
	decisionID cover.DecisionID,
	conditionCount int,
	evaluations []cover.DecisionEvaluation,
) preparedEvaluations {
	byVector := make(map[string]cover.DecisionEvaluation)
	aborted := 0
	invalid := 0
	for _, evaluation := range evaluations {
		if evaluation.DecisionID != decisionID {
			continue
		}
		switch evaluation.Status {
		case cover.EvaluationAborted:
			aborted++
			continue
		case cover.EvaluationCompleted:
		default:
			invalid++
			continue
		}
		if len(evaluation.Conditions) != conditionCount || !validStates(evaluation.Conditions) {
			invalid++
			continue
		}
		evaluation = cloneEvaluation(evaluation)
		key := evaluationKey(evaluation)
		if existing, exists := byVector[key]; !exists || lessEvaluationIdentity(evaluation, existing) {
			byVector[key] = evaluation
		}
	}

	result := make([]cover.DecisionEvaluation, 0, len(byVector))
	for _, evaluation := range byVector {
		result = append(result, evaluation)
	}
	sort.Slice(result, func(i, j int) bool { return lessEvaluation(result[i], result[j]) })
	return preparedEvaluations{evaluations: result, aborted: aborted, invalid: invalid}
}

func validStates(states []cover.ConditionState) bool {
	for _, state := range states {
		if state != cover.ConditionNotEvaluated &&
			state != cover.ConditionFalse &&
			state != cover.ConditionTrue {
			return false
		}
	}
	return true
}

func evaluationKey(evaluation cover.DecisionEvaluation) string {
	key := make([]byte, len(evaluation.Conditions)+1)
	for index, state := range evaluation.Conditions {
		key[index] = byte(state)
	}
	if evaluation.Result {
		key[len(key)-1] = 1
	}
	return string(key)
}

func lessEvaluation(left, right cover.DecisionEvaluation) bool {
	for index := range left.Conditions {
		if left.Conditions[index] != right.Conditions[index] {
			return left.Conditions[index] < right.Conditions[index]
		}
	}
	if left.Result != right.Result {
		return !left.Result
	}
	return lessEvaluationIdentity(left, right)
}

func lessEvaluationIdentity(left, right cover.DecisionEvaluation) bool {
	if left.RunID != right.RunID {
		return left.RunID < right.RunID
	}
	if left.PackagePath != right.PackagePath {
		return left.PackagePath < right.PackagePath
	}
	if left.ProcessID != right.ProcessID {
		return left.ProcessID < right.ProcessID
	}
	if left.EvaluationID != right.EvaluationID {
		return left.EvaluationID < right.EvaluationID
	}
	return normalizedTestID(left.TestID) < normalizedTestID(right.TestID)
}

func cloneEvaluation(evaluation cover.DecisionEvaluation) cover.DecisionEvaluation {
	evaluation.Conditions = append([]cover.ConditionState(nil), evaluation.Conditions...)
	evaluation.TestID = normalizedTestID(evaluation.TestID)
	return evaluation
}

func normalizedTestID(testID string) string {
	if testID == "" {
		return cover.UnknownTestID
	}
	return testID
}

func statesFromBools(values []bool) []cover.ConditionState {
	states := make([]cover.ConditionState, len(values))
	for index, value := range values {
		if value {
			states[index] = cover.ConditionTrue
		} else {
			states[index] = cover.ConditionFalse
		}
	}
	return states
}

func baseResult(decisionID cover.DecisionID, metric cover.CoverageMetric, indexes []uint16) cover.MCDCResult {
	conditions := make([]cover.MCDCConditionResult, len(indexes))
	for position, index := range indexes {
		conditions[position] = cover.MCDCConditionResult{
			ConditionIndex: index,
			Status:         cover.CoverageNotCovered,
		}
	}
	return cover.MCDCResult{
		DecisionID: decisionID,
		Metric:     metric,
		Status:     cover.CoverageNotCovered,
		Conditions: conditions,
	}
}

func applyIssue(result *cover.MCDCResult, issue *analysisIssue) {
	result.Status = issue.status
	result.Reason = issue.reason
	for index := range result.Conditions {
		result.Conditions[index].Status = issue.status
		result.Conditions[index].Reason = issue.reason
	}
}

func finishResult(result *cover.MCDCResult) {
	if len(result.Conditions) == 0 {
		result.Status = cover.CoverageUnknown
		if result.Reason == "" {
			result.Reason = "decision has no atomic conditions"
		}
		return
	}
	allCovered := true
	hasUnsupported := false
	hasNotCovered := false
	hasPossiblyInfeasible := false
	for _, condition := range result.Conditions {
		allCovered = allCovered && condition.Status == cover.CoverageCovered
		hasUnsupported = hasUnsupported || condition.Status == cover.CoverageUnsupported
		hasNotCovered = hasNotCovered || condition.Status == cover.CoverageNotCovered
		hasPossiblyInfeasible = hasPossiblyInfeasible || condition.Status == cover.CoveragePossiblyInfeasible
	}
	switch {
	case allCovered:
		result.Status = cover.CoverageCovered
	case hasUnsupported:
		result.Status = cover.CoverageUnsupported
	case hasNotCovered:
		result.Status = cover.CoverageNotCovered
	case hasPossiblyInfeasible:
		result.Status = cover.CoveragePossiblyInfeasible
		if result.Reason == "" {
			result.Reason = "one or more MC/DC obligations are structurally infeasible under the selected strategy"
		}
	default:
		result.Status = cover.CoverageNotCovered
	}
}

func enrichResult(result *cover.MCDCResult) {
	result.Outcome, result.Support, result.Analysis = decomposeStatus(result.Status)
	for index := range result.Conditions {
		result.Conditions[index].Outcome, result.Conditions[index].Support, result.Conditions[index].Analysis =
			decomposeStatus(result.Conditions[index].Status)
	}
}

func decomposeStatus(status cover.CoverageStatus) (cover.CoverageOutcome, cover.SupportStatus, cover.AnalysisStatus) {
	switch status {
	case cover.CoverageCovered:
		return cover.CoverageOutcomeCovered, cover.SupportSupported, cover.AnalysisComplete
	case cover.CoverageNotCovered:
		return cover.CoverageOutcomeNotCovered, cover.SupportSupported, cover.AnalysisComplete
	case cover.CoverageUnsupported:
		return cover.CoverageOutcomeUnknown, cover.SupportUnsupported, cover.AnalysisComplete
	case cover.CoveragePossiblyInfeasible:
		return cover.CoverageOutcomeNotCovered, cover.SupportSupported, cover.AnalysisInfeasible
	default:
		return cover.CoverageOutcomeUnknown, cover.SupportUnknown, cover.AnalysisIncomplete
	}
}
