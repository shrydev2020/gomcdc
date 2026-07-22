package mcdc

import (
	"unsafe"

	cover "github.com/shrydev2020/gomcdc/internal/coverage"
)

// maskingSearchBudget counts the operations that dominate the joint solver:
// candidate evaluation pairs, newly expanded dynamic-programming states, and
// the primary solver buffers. Each budget belongs to one condition obligation.
type maskingSearchBudget struct {
	limits          AnalysisBudget
	evaluationPairs uint64
	searchStates    uint64
	solverBytes     uint64
	exceededReason  string
}

func newMaskingSearchBudget(configured AnalysisBudget) *maskingSearchBudget {
	return &maskingSearchBudget{limits: EffectiveMaskingAnalysisBudget(configured)}
}

func (budget *maskingSearchBudget) consumeEvaluationPair() bool {
	if budget.evaluationPairs >= budget.limits.MaxEvaluationPairs {
		budget.exceed("evaluation-pair count")
		return false
	}
	budget.evaluationPairs++
	return true
}

func (budget *maskingSearchBudget) consumeSearchState() bool {
	if budget.searchStates >= budget.limits.MaxSearchStates {
		budget.exceed("search-state count")
		return false
	}
	budget.searchStates++
	return true
}

func (budget *maskingSearchBudget) requireSolverBytes(bytes uint64) bool {
	if bytes > budget.limits.MaxSolverBytes {
		budget.exceed("solver byte")
		return false
	}
	if bytes > budget.solverBytes {
		budget.solverBytes = bytes
	}
	return true
}

func (budget *maskingSearchBudget) exceed(resource string) {
	if budget.exceededReason == "" {
		budget.exceededReason = resource
	}
}

func (budget *maskingSearchBudget) reason() string {
	if budget.exceededReason == "" {
		return "masking MC/DC joint search did not finish"
	}
	return "masking MC/DC joint search exceeded configured " + budget.exceededReason + " limit"
}

func (budget *maskingSearchBudget) stats() maskingSearchStats {
	return maskingSearchStats{
		EvaluationPairs: budget.evaluationPairs,
		SearchStates:    budget.searchStates,
		SolverBytes:     budget.solverBytes,
	}
}

// A joint state describes the required result of one subtree in both
// completions and whether a change to that subtree can still propagate to the
// decision root in each completion. There are 2^4 possible states per node.
const jointStateCount = 16

// A candidate bucket uses one bit for the target value and one for the
// decision result. A D19 candidate must reverse both bits.
const maskingCandidateOppositeBits = 0b11

type jointNodeKind uint8

const (
	jointCondition jointNodeKind = iota
	jointConstant
	jointNot
	jointAnd
	jointOr
)

type jointNode struct {
	kind      jointNodeKind
	constant  bool
	condition uint16
	left      int
	right     int
}

const (
	jointMemoUnknown uint8 = iota
	jointMemoImpossible
	jointMemoPossible
)

type jointMemoEntry struct {
	status uint8
	choice uint8
}

type jointWorkspace struct {
	nodes  []jointNode
	memo   []jointMemoEntry
	first  []bool
	second []bool
}

func expressionNodeCount(expression *cover.BooleanExpression) int {
	if expression == nil {
		return 0
	}
	switch expression.Kind {
	case cover.BooleanExpressionNot:
		return 1 + expressionNodeCount(expression.Left)
	case cover.BooleanExpressionAnd, cover.BooleanExpressionOr:
		return 1 + expressionNodeCount(expression.Left) + expressionNodeCount(expression.Right)
	default:
		return 1
	}
}

// jointSolverBytes is the exact byte size of the primary backing arrays
// allocated by newJointWorkspace on the current architecture. It deliberately
// excludes validated input data, result witnesses, and goroutine stack space.
func jointSolverBytes(nodeCount, conditionCount int) uint64 {
	return uint64(nodeCount)*uint64(unsafe.Sizeof(jointNode{})) +
		uint64(nodeCount*jointStateCount)*uint64(unsafe.Sizeof(jointMemoEntry{})) +
		uint64(conditionCount*2)*uint64(unsafe.Sizeof(bool(false)))
}

func maskingSolverBytes(nodeCount, conditionCount, candidateIndexes int) uint64 {
	return jointSolverBytes(nodeCount, conditionCount) +
		uint64(candidateIndexes)*uint64(unsafe.Sizeof(int(0)))
}

func newJointWorkspace(expression *cover.BooleanExpression, nodeCount, conditionCount int) *jointWorkspace {
	workspace := &jointWorkspace{
		nodes:  make([]jointNode, 0, nodeCount),
		memo:   make([]jointMemoEntry, nodeCount*jointStateCount),
		first:  make([]bool, conditionCount),
		second: make([]bool, conditionCount),
	}
	workspace.appendExpression(expression)
	return workspace
}

func (workspace *jointWorkspace) appendExpression(expression *cover.BooleanExpression) int {
	index := len(workspace.nodes)
	workspace.nodes = append(workspace.nodes, jointNode{left: -1, right: -1})
	node := jointNode{left: -1, right: -1}
	switch expression.Kind {
	case cover.BooleanExpressionCondition:
		node.kind = jointCondition
		node.condition = expression.ConditionIndex
	case cover.BooleanExpressionConstant:
		node.kind = jointConstant
		node.constant = expression.Constant
	case cover.BooleanExpressionNot:
		node.kind = jointNot
		node.left = workspace.appendExpression(expression.Left)
	case cover.BooleanExpressionAnd:
		node.kind = jointAnd
		node.left = workspace.appendExpression(expression.Left)
		node.right = workspace.appendExpression(expression.Right)
	case cover.BooleanExpressionOr:
		node.kind = jointOr
		node.left = workspace.appendExpression(expression.Left)
		node.right = workspace.appendExpression(expression.Right)
	default:
		panic("mcdc: validated expression contains an unsupported node")
	}
	workspace.nodes[index] = node
	return index
}

func (workspace *jointWorkspace) solvePair(
	first cover.DecisionEvaluation,
	second cover.DecisionEvaluation,
	target uint16,
	budget *maskingSearchBudget,
) ([]bool, []bool, bool) {
	clear(workspace.memo)
	solver := jointSolver{
		workspace: workspace,
		first:     first.Conditions,
		second:    second.Conditions,
		target:    target,
		budget:    budget,
	}
	rootState := encodeJointState(first.Result, second.Result, true, true)
	if !solver.solve(0, rootState) {
		return nil, nil, false
	}
	workspace.reconstruct(0, rootState)
	return workspace.first, workspace.second, true
}

type jointSolver struct {
	workspace *jointWorkspace
	first     []cover.ConditionState
	second    []cover.ConditionState
	target    uint16
	budget    *maskingSearchBudget
}

func (solver *jointSolver) solve(nodeIndex int, state uint8) bool {
	memoIndex := nodeIndex*jointStateCount + int(state)
	entry := &solver.workspace.memo[memoIndex]
	switch entry.status {
	case jointMemoPossible:
		return true
	case jointMemoImpossible:
		return false
	}
	if !solver.budget.consumeSearchState() {
		return false
	}

	firstValue, secondValue, firstActive, secondActive := decodeJointState(state)
	node := solver.workspace.nodes[nodeIndex]
	possible := false
	switch node.kind {
	case jointCondition:
		possible = observedAllows(solver.first[node.condition], firstValue) &&
			observedAllows(solver.second[node.condition], secondValue)
		if possible && node.condition == solver.target {
			possible = firstValue != secondValue && firstActive && secondActive
		} else if possible && firstValue != secondValue {
			possible = !firstActive && !secondActive
		}
	case jointConstant:
		possible = firstValue == node.constant && secondValue == node.constant
	case jointNot:
		possible = solver.solve(node.left, encodeJointState(
			!firstValue, !secondValue, firstActive, secondActive,
		))
	case jointAnd, jointOr:
		possible = solver.solveBinary(node, state, entry)
	default:
		panic("mcdc: joint workspace contains an unsupported node")
	}

	if solver.budget.exceededReason != "" {
		return false
	}
	if possible {
		entry.status = jointMemoPossible
	} else {
		entry.status = jointMemoImpossible
	}
	return possible
}

func (solver *jointSolver) solveBinary(node jointNode, state uint8, entry *jointMemoEntry) bool {
	firstValue, secondValue, firstActive, secondActive := decodeJointState(state)
	firstChoices := jointChildValues(node.kind, firstValue)
	secondChoices := jointChildValues(node.kind, secondValue)
	for _, firstChoice := range firstChoices {
		for _, secondChoice := range secondChoices {
			if solver.budget.exceededReason != "" {
				return false
			}
			leftFirstActive, rightFirstActive := jointChildActivity(
				node.kind, firstActive, firstChoice[0], firstChoice[1],
			)
			leftSecondActive, rightSecondActive := jointChildActivity(
				node.kind, secondActive, secondChoice[0], secondChoice[1],
			)
			leftState := encodeJointState(
				firstChoice[0], secondChoice[0], leftFirstActive, leftSecondActive,
			)
			if !solver.solve(node.left, leftState) {
				continue
			}
			rightState := encodeJointState(
				firstChoice[1], secondChoice[1], rightFirstActive, rightSecondActive,
			)
			if !solver.solve(node.right, rightState) {
				continue
			}
			entry.choice = encodeJointChoice(firstChoice, secondChoice)
			return true
		}
	}
	return false
}

func observedAllows(state cover.ConditionState, value bool) bool {
	observed, evaluated := state.Bool()
	return !evaluated || observed == value
}

func encodeJointState(firstValue, secondValue, firstActive, secondActive bool) uint8 {
	var state uint8
	if firstValue {
		state |= 1
	}
	if secondValue {
		state |= 2
	}
	if firstActive {
		state |= 4
	}
	if secondActive {
		state |= 8
	}
	return state
}

func decodeJointState(state uint8) (firstValue, secondValue, firstActive, secondActive bool) {
	return state&1 != 0, state&2 != 0, state&4 != 0, state&8 != 0
}

func jointChildValues(kind jointNodeKind, desired bool) [][2]bool {
	switch kind {
	case jointAnd:
		if desired {
			return [][2]bool{{true, true}}
		}
		return [][2]bool{{false, false}, {false, true}, {true, false}}
	case jointOr:
		if desired {
			return [][2]bool{{false, true}, {true, false}, {true, true}}
		}
		return [][2]bool{{false, false}}
	default:
		panic("mcdc: child values requested for a non-binary node")
	}
}

func jointChildActivity(kind jointNodeKind, active, leftValue, rightValue bool) (left, right bool) {
	switch kind {
	case jointAnd:
		return active && rightValue, active && leftValue
	case jointOr:
		return active && !rightValue, active && !leftValue
	default:
		panic("mcdc: child activity requested for a non-binary node")
	}
}

func encodeJointChoice(first, second [2]bool) uint8 {
	return encodeJointState(first[0], first[1], second[0], second[1])
}

func decodeJointChoice(choice uint8) (first, second [2]bool) {
	first[0], first[1], second[0], second[1] = decodeJointState(choice)
	return first, second
}

func (workspace *jointWorkspace) reconstruct(nodeIndex int, state uint8) {
	firstValue, secondValue, firstActive, secondActive := decodeJointState(state)
	node := workspace.nodes[nodeIndex]
	switch node.kind {
	case jointCondition:
		workspace.first[node.condition] = firstValue
		workspace.second[node.condition] = secondValue
	case jointConstant:
		return
	case jointNot:
		workspace.reconstruct(node.left, encodeJointState(
			!firstValue, !secondValue, firstActive, secondActive,
		))
	case jointAnd, jointOr:
		entry := workspace.memo[nodeIndex*jointStateCount+int(state)]
		firstChoice, secondChoice := decodeJointChoice(entry.choice)
		leftFirstActive, rightFirstActive := jointChildActivity(
			node.kind, firstActive, firstChoice[0], firstChoice[1],
		)
		leftSecondActive, rightSecondActive := jointChildActivity(
			node.kind, secondActive, secondChoice[0], secondChoice[1],
		)
		workspace.reconstruct(node.left, encodeJointState(
			firstChoice[0], secondChoice[0], leftFirstActive, leftSecondActive,
		))
		workspace.reconstruct(node.right, encodeJointState(
			firstChoice[1], secondChoice[1], rightFirstActive, rightSecondActive,
		))
	default:
		panic("mcdc: joint workspace contains an unsupported node")
	}
}

func maskingCandidateBucketCounts(
	evaluations []cover.DecisionEvaluation,
	target uint16,
) (counts [4]int, candidateIndexes int, hasCandidatePair bool) {
	for _, evaluation := range evaluations {
		bucket, candidate := maskingCandidateBucket(evaluation, target)
		if candidate {
			counts[bucket]++
			candidateIndexes++
		}
	}
	hasCandidatePair = counts[0] > 0 && counts[3] > 0 || counts[1] > 0 && counts[2] > 0
	return counts, candidateIndexes, hasCandidatePair
}

func maskingCandidateBuckets(
	evaluations []cover.DecisionEvaluation,
	target uint16,
	counts [4]int,
) [4][]int {
	var buckets [4][]int
	for index := range buckets {
		buckets[index] = make([]int, 0, counts[index])
	}
	for index, evaluation := range evaluations {
		bucket, candidate := maskingCandidateBucket(evaluation, target)
		if candidate {
			buckets[bucket] = append(buckets[bucket], index)
		}
	}
	return buckets
}

func maskingCandidateBucket(evaluation cover.DecisionEvaluation, target uint16) (int, bool) {
	value, evaluated := evaluation.Conditions[target].Bool()
	if !evaluated {
		return 0, false
	}
	bucket := 0
	if value {
		bucket |= 1
	}
	if evaluation.Result {
		bucket |= 2
	}
	return bucket, true
}

func maskingWitness(
	first cover.DecisionEvaluation,
	second cover.DecisionEvaluation,
	target uint16,
	firstCompletion []bool,
	secondCompletion []bool,
) *cover.MCDCWitness {
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
	}
}
