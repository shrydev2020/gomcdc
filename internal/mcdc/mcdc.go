// Package mcdc analyzes completed decision evaluation vectors without reading
// or mutating runtime state.
package mcdc

import (
	"fmt"
	"sort"

	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
)

// MCDCStrategy is the consumer-owned boundary for an MC/DC definition.
// Implementations must be pure and deterministic for equivalent inputs.
type MCDCStrategy interface {
	Analyze(metadata cover.DecisionMetadata, evaluations []cover.DecisionEvaluation) cover.MCDCResult
}

// UniqueCauseStrategy requires every non-target observed condition state to be
// identical in the witness pair. Not-evaluated is a distinct state.
type UniqueCauseStrategy struct{}

// AnalysisBudget bounds one Masking MC/DC condition-obligation search. It
// counts candidate observed evaluation pairs, newly expanded joint-DP states,
// and bytes in the solver's primary backing arrays. Zero values select the
// corresponding default so MaskingStrategy{} is safe for callers that do not
// need custom limits.
type AnalysisBudget struct {
	MaxEvaluationPairs uint64
	MaxSearchStates    uint64
	MaxSolverBytes     uint64
}

const (
	defaultMaxEvaluationPairs = uint64(1_000_000)
	defaultMaxSearchStates    = uint64(4_000_000)
	defaultMaxSolverBytes     = uint64(64 * 1024 * 1024)
)

// DefaultMaskingAnalysisBudget returns the deterministic resource limits used
// by a zero-value MaskingStrategy.
func DefaultMaskingAnalysisBudget() AnalysisBudget {
	return AnalysisBudget{
		MaxEvaluationPairs: defaultMaxEvaluationPairs,
		MaxSearchStates:    defaultMaxSearchStates,
		MaxSolverBytes:     defaultMaxSolverBytes,
	}
}

// EffectiveMaskingAnalysisBudget fills each zero-valued limit with the
// deterministic default used by MaskingStrategy. The returned value is safe
// to persist in a report as the effective per-condition-obligation budget.
func EffectiveMaskingAnalysisBudget(configured AnalysisBudget) AnalysisBudget {
	defaults := DefaultMaskingAnalysisBudget()
	if configured.MaxEvaluationPairs == 0 {
		configured.MaxEvaluationPairs = defaults.MaxEvaluationPairs
	}
	if configured.MaxSearchStates == 0 {
		configured.MaxSearchStates = defaults.MaxSearchStates
	}
	if configured.MaxSolverBytes == 0 {
		configured.MaxSolverBytes = defaults.MaxSolverBytes
	}
	return configured
}

// MaskingStrategy uses the decision expression's Boolean difference. A target
// must be pivotal in both completed witness vectors; not-evaluated values are
// completed counterfactually without changing either recorded decision result.
type MaskingStrategy struct {
	Budget AnalysisBudget
}

var _ MCDCStrategy = UniqueCauseStrategy{}
var _ MCDCStrategy = MaskingStrategy{}

type analysisIssue struct {
	outcome  cover.CoverageOutcome
	support  cover.SupportStatus
	analysis cover.AnalysisStatus
	reason   string
}

func incompleteAnalysis(reason string) *analysisIssue {
	return &analysisIssue{
		outcome: cover.CoverageOutcomeUnknown, support: cover.SupportSupported,
		analysis: cover.AnalysisIncomplete, reason: reason,
	}
}

func unsupportedAnalysis(reason string) *analysisIssue {
	return &analysisIssue{
		outcome: cover.CoverageOutcomeUnknown, support: cover.SupportUnsupported,
		analysis: cover.AnalysisComplete, reason: reason,
	}
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
		if witness, covered := uniqueCauseWitness(prepared.evaluations, target); covered {
			conditionResult.Outcome = cover.CoverageOutcomeCovered
			conditionResult.Witness = witness
		}
		if conditionResult.Witness == nil {
			if expressionStructureValid && !uniqueCauseStructurallyFeasible(metadata.ExpressionTree, target, count) {
				conditionResult.Analysis = cover.AnalysisInfeasible
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
func (strategy MaskingStrategy) Analyze(metadata cover.DecisionMetadata, evaluations []cover.DecisionEvaluation) cover.MCDCResult {
	return strategy.analyze(metadata, evaluations, nil)
}

type maskingSearchStats struct {
	EvaluationPairs uint64
	SearchStates    uint64
	SolverBytes     uint64
}

func (strategy MaskingStrategy) analyze(
	metadata cover.DecisionMetadata,
	evaluations []cover.DecisionEvaluation,
	observe func(target uint16, stats maskingSearchStats),
) cover.MCDCResult {
	count, indexes, issue := conditionLayout(metadata, evaluations)
	result := baseResult(metadata.ID, cover.CoverageMetricMCDCMasking, indexes)
	if issue != nil {
		applyIssue(&result, issue)
		return result
	}
	if metadata.ExpressionTree == nil {
		applyIssue(&result, incompleteAnalysis("masking MC/DC requires a boolean expression tree"))
		return result
	}
	if treeIssue := validateExpression(metadata.ExpressionTree, count); treeIssue != nil {
		applyIssue(&result, treeIssue)
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
	nodeCount := expressionNodeCount(metadata.ExpressionTree)
	var workspace *jointWorkspace

	for conditionPosition, target := range indexes {
		conditionResult := &result.Conditions[conditionPosition]
		if !structurallyPivotal(metadata.ExpressionTree, target, count) {
			conditionResult.Analysis = cover.AnalysisInfeasible
			conditionResult.Reason = "the boolean expression cannot make this condition pivotal"
			if observe != nil {
				observe(target, maskingSearchStats{})
			}
			continue
		}

		search := newMaskingSearchBudget(strategy.Budget)
		bucketCounts, candidateIndexes, hasCandidatePair := maskingCandidateBucketCounts(
			prepared.evaluations, target,
		)
		if !hasCandidatePair {
			conditionResult.Reason = "no pair makes the evaluated target pivotal in both completed vectors"
			if observe != nil {
				observe(target, search.stats())
			}
			continue
		}
		solverBytes := maskingSolverBytes(nodeCount, count, candidateIndexes)
		if search.requireSolverBytes(solverBytes) {
			if workspace == nil {
				workspace = newJointWorkspace(metadata.ExpressionTree, nodeCount, count)
			}
		} else {
			conditionResult.Outcome = cover.CoverageOutcomeUnknown
			conditionResult.Analysis = cover.AnalysisIncomplete
			conditionResult.Reason = search.reason()
			if observe != nil {
				observe(target, search.stats())
			}
			continue
		}
		buckets := maskingCandidateBuckets(prepared.evaluations, target, bucketCounts)
	searchPairs:
		for first, left := range prepared.evaluations {
			bucket, candidate := maskingCandidateBucket(left, target)
			if !candidate {
				continue
			}
			seconds := buckets[bucket^maskingCandidateOppositeBits]
			for position := sort.SearchInts(seconds, first+1); position < len(seconds); position++ {
				second := seconds[position]
				if !search.consumeEvaluationPair() {
					break searchPairs
				}
				right := prepared.evaluations[second]
				firstCompletion, secondCompletion, covered := workspace.solvePair(left, right, target, search)
				if search.exceededReason != "" {
					break searchPairs
				}
				if covered {
					conditionResult.Outcome = cover.CoverageOutcomeCovered
					conditionResult.Witness = maskingWitness(
						left, right, target, firstCompletion, secondCompletion,
					)
					break searchPairs
				}
			}
		}
		if conditionResult.Witness == nil {
			if search.exceededReason != "" {
				conditionResult.Outcome = cover.CoverageOutcomeUnknown
				conditionResult.Analysis = cover.AnalysisIncomplete
				conditionResult.Reason = search.reason()
			} else {
				conditionResult.Reason = "no pair makes the evaluated target pivotal in both completed vectors"
			}
		}
		if observe != nil {
			observe(target, search.stats())
		}
	}

	finishResult(&result)
	return result
}

// uniqueCauseWitness indexes evaluations by every non-target condition state.
// The index avoids rescanning all evaluation pairs for each target while still
// selecting the same lexicographically-first pair as the former nested loop.
func uniqueCauseWitness(evaluations []cover.DecisionEvaluation, target uint16) (*cover.MCDCWitness, bool) {
	// Most real decisions have only a few distinct vectors and commonly find a
	// witness immediately. Keep that hot path allocation-free; the index below
	// handles large or witness-free sets without an O(E²) scan.
	const probeLimit = 1024
	fullScanLimit := probeLimit
	if len(evaluations) <= 32 {
		fullScanLimit = len(evaluations) * len(evaluations)
	}
	probes := 0
	for first := 0; first < len(evaluations); first++ {
		for second := first + 1; second < len(evaluations); second++ {
			if uniqueCausePairMatches(evaluations[first], evaluations[second], target) {
				return &cover.MCDCWitness{
					First:  cloneEvaluation(evaluations[first]),
					Second: cloneEvaluation(evaluations[second]),
				}, true
			}
			probes++
			if probes == fullScanLimit {
				first = len(evaluations)
				break
			}
		}
	}
	if len(evaluations) <= 32 {
		return nil, false
	}

	type group struct {
		indexes [2][2][]int // [decision result][target value]
	}
	groups := make(map[string]*group, len(evaluations))
	for index, evaluation := range evaluations {
		if int(target) >= len(evaluation.Conditions) || !evaluation.Conditions[target].IsEvaluated() {
			continue
		}
		key := nonTargetVectorKey(evaluation.Conditions, target)
		entry := groups[key]
		if entry == nil {
			entry = &group{}
			groups[key] = entry
		}
		resultIndex := 0
		if evaluation.Result {
			resultIndex = 1
		}
		targetIndex := 0
		if evaluation.Conditions[target] == cover.ConditionTrue {
			targetIndex = 1
		}
		entry.indexes[resultIndex][targetIndex] = append(entry.indexes[resultIndex][targetIndex], index)
	}

	bestFirst, bestSecond := len(evaluations), len(evaluations)
	for _, entry := range groups {
		for firstResult := 0; firstResult < 2; firstResult++ {
			secondResult := 1 - firstResult
			for firstTarget := 0; firstTarget < 2; firstTarget++ {
				secondTarget := 1 - firstTarget
				secondIndexes := entry.indexes[secondResult][secondTarget]
				for _, first := range entry.indexes[firstResult][firstTarget] {
					position := sort.SearchInts(secondIndexes, first+1)
					if position == len(secondIndexes) {
						continue
					}
					second := secondIndexes[position]
					if first < bestFirst || (first == bestFirst && second < bestSecond) {
						bestFirst, bestSecond = first, second
					}
				}
			}
		}
	}
	if bestFirst == len(evaluations) {
		return nil, false
	}
	return &cover.MCDCWitness{
		First:  cloneEvaluation(evaluations[bestFirst]),
		Second: cloneEvaluation(evaluations[bestSecond]),
	}, true
}

func uniqueCausePairMatches(first, second cover.DecisionEvaluation, target uint16) bool {
	if first.Result == second.Result || !first.Conditions[target].IsEvaluated() || !second.Conditions[target].IsEvaluated() || first.Conditions[target] == second.Conditions[target] {
		return false
	}
	for index := range first.Conditions {
		if uint16(index) != target && first.Conditions[index] != second.Conditions[index] {
			return false
		}
	}
	return true
}

func nonTargetVectorKey(states []cover.ConditionState, target uint16) string {
	key := make([]byte, 0, len(states)-1)
	for index, state := range states {
		if uint16(index) == target {
			continue
		}
		key = append(key, byte(state))
	}
	return string(key)
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
				return count, indexesForCount(count), incompleteAnalysis(
					"condition metadata indexes must be unique and contiguous from zero",
				)
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
			return 0, nil, incompleteAnalysis("decision has no atomic conditions")
		}
		for position, index := range indexes {
			if int(index) != position {
				return len(indexes), indexes, incompleteAnalysis(
					"expression condition indexes must be contiguous from zero",
				)
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
			return count, indexesForCount(count), incompleteAnalysis(
				"condition metadata is absent and evaluation vector lengths disagree",
			)
		}
	}
	if count <= 0 {
		return 0, nil, incompleteAnalysis("condition metadata is absent")
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
		return incompleteAnalysis("boolean expression contains a nil operand")
	}
	if visiting[expression] {
		return incompleteAnalysis("boolean expression contains a cycle")
	}
	visiting[expression] = true
	defer delete(visiting, expression)

	switch expression.Kind {
	case cover.BooleanExpressionCondition:
		if expression.Left != nil || expression.Right != nil {
			return incompleteAnalysis("condition expression must be a leaf")
		}
		if _, duplicate := seen[expression.ConditionIndex]; duplicate {
			return incompleteAnalysis("each atomic occurrence must have a unique condition index")
		}
		seen[expression.ConditionIndex] = struct{}{}
		return nil
	case cover.BooleanExpressionConstant:
		if expression.Left != nil || expression.Right != nil {
			return incompleteAnalysis("constant expression must be a leaf")
		}
		return nil
	case cover.BooleanExpressionNot:
		if expression.Left == nil || expression.Right != nil {
			return incompleteAnalysis("not expression must have exactly one operand")
		}
		return inspectExpression(expression.Left, seen, visiting)
	case cover.BooleanExpressionAnd, cover.BooleanExpressionOr:
		if expression.Left == nil || expression.Right == nil {
			return incompleteAnalysis("and/or expression must have two operands")
		}
		if issue := inspectExpression(expression.Left, seen, visiting); issue != nil {
			return issue
		}
		return inspectExpression(expression.Right, seen, visiting)
	default:
		return unsupportedAnalysis(fmt.Sprintf("unsupported boolean expression kind %q", expression.Kind))
	}
}

func validateExpression(expression *cover.BooleanExpression, count int) *analysisIssue {
	indexes, issue := expressionIndexes(expression)
	if issue != nil {
		return issue
	}
	if len(indexes) != count {
		return incompleteAnalysis("boolean expression and condition metadata have different condition counts")
	}
	for position, index := range indexes {
		if int(index) != position {
			return incompleteAnalysis("boolean expression condition indexes must be contiguous from zero")
		}
	}
	return nil
}

func prepareEvaluations(
	decisionID cover.DecisionID,
	conditionCount int,
	evaluations []cover.DecisionEvaluation,
) preparedEvaluations {
	prepared := make([]cover.DecisionEvaluation, 0, len(evaluations))
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
		// The analysis never mutates condition slices. Copy the value so TestID
		// can be normalized without cloning every vector; selected witnesses are
		// deep-cloned at the result boundary.
		evaluation.TestID = normalizedTestID(evaluation.TestID)
		prepared = append(prepared, evaluation)
	}

	sort.Slice(prepared, func(i, j int) bool { return lessEvaluation(prepared[i], prepared[j]) })
	unique := 0
	for index := range prepared {
		if unique > 0 && sameEvaluationVector(prepared[unique-1], prepared[index]) {
			continue
		}
		prepared[unique] = prepared[index]
		unique++
	}
	clear(prepared[unique:])
	return preparedEvaluations{evaluations: prepared[:unique], aborted: aborted, invalid: invalid}
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

func sameEvaluationVector(left, right cover.DecisionEvaluation) bool {
	if left.Result != right.Result || len(left.Conditions) != len(right.Conditions) {
		return false
	}
	for index := range left.Conditions {
		if left.Conditions[index] != right.Conditions[index] {
			return false
		}
	}
	return true
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
			Outcome:        cover.CoverageOutcomeNotCovered,
			Support:        cover.SupportSupported,
			Analysis:       cover.AnalysisComplete,
		}
	}
	return cover.MCDCResult{
		DecisionID: decisionID, Metric: metric,
		Outcome: cover.CoverageOutcomeNotCovered, Support: cover.SupportSupported,
		Analysis: cover.AnalysisComplete, Conditions: conditions,
	}
}

func applyIssue(result *cover.MCDCResult, issue *analysisIssue) {
	result.Outcome = issue.outcome
	result.Support = issue.support
	result.Analysis = issue.analysis
	result.Reason = issue.reason
	for index := range result.Conditions {
		result.Conditions[index].Outcome = issue.outcome
		result.Conditions[index].Support = issue.support
		result.Conditions[index].Analysis = issue.analysis
		result.Conditions[index].Reason = issue.reason
	}
}

func finishResult(result *cover.MCDCResult) {
	if len(result.Conditions) == 0 {
		result.Outcome = cover.CoverageOutcomeUnknown
		result.Support = cover.SupportSupported
		result.Analysis = cover.AnalysisIncomplete
		if result.Reason == "" {
			result.Reason = "decision has no atomic conditions"
		}
		return
	}
	allCovered := true
	hasUnsupported := false
	hasUnknownSupport := false
	hasAnalysisIncomplete := false
	hasNotCovered := false
	hasInfeasible := false
	for _, condition := range result.Conditions {
		allCovered = allCovered && condition.Outcome == cover.CoverageOutcomeCovered &&
			condition.Support == cover.SupportSupported && condition.Analysis == cover.AnalysisComplete
		hasUnsupported = hasUnsupported || condition.Support == cover.SupportUnsupported
		hasUnknownSupport = hasUnknownSupport || condition.Support == cover.SupportUnknown
		hasAnalysisIncomplete = hasAnalysisIncomplete || condition.Analysis == cover.AnalysisIncomplete
		hasNotCovered = hasNotCovered || condition.Outcome == cover.CoverageOutcomeNotCovered &&
			condition.Analysis == cover.AnalysisComplete
		hasInfeasible = hasInfeasible || condition.Analysis == cover.AnalysisInfeasible
	}
	switch {
	case allCovered:
		result.Outcome = cover.CoverageOutcomeCovered
		result.Support = cover.SupportSupported
		result.Analysis = cover.AnalysisComplete
	case hasUnsupported:
		result.Outcome = cover.CoverageOutcomeUnknown
		result.Support = cover.SupportUnsupported
		result.Analysis = cover.AnalysisComplete
	case hasUnknownSupport:
		result.Outcome = cover.CoverageOutcomeUnknown
		result.Support = cover.SupportUnknown
		result.Analysis = cover.AnalysisIncomplete
	case hasAnalysisIncomplete:
		result.Outcome = cover.CoverageOutcomeUnknown
		result.Support = cover.SupportSupported
		result.Analysis = cover.AnalysisIncomplete
	case hasNotCovered:
		result.Outcome = cover.CoverageOutcomeNotCovered
		result.Support = cover.SupportSupported
		result.Analysis = cover.AnalysisComplete
	case hasInfeasible:
		result.Outcome = cover.CoverageOutcomeNotCovered
		result.Support = cover.SupportSupported
		result.Analysis = cover.AnalysisInfeasible
		if result.Reason == "" {
			result.Reason = "one or more MC/DC obligations are structurally infeasible under the selected strategy"
		}
	default:
		result.Outcome = cover.CoverageOutcomeUnknown
		result.Support = cover.SupportUnknown
		result.Analysis = cover.AnalysisIncomplete
	}
}
