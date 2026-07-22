package mcdc

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	cover "github.com/shrydev2020/gomcdc/internal/coverage"
)

const testDecisionID cover.DecisionID = 41

var (
	notEvaluated   = cover.ConditionNotEvaluated
	conditionFalse = cover.ConditionFalse
	conditionTrue  = cover.ConditionTrue
)

func TestUniqueCauseStrategy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		metadata    cover.DecisionMetadata
		evaluations []cover.DecisionEvaluation
		want        []string
		wantAborted int
		wantInvalid int
	}{
		{
			name:     "one condition",
			metadata: decisionMetadata(condition(0)),
			evaluations: []cover.DecisionEvaluation{
				completed(1, []cover.ConditionState{conditionFalse}, false),
				completed(2, []cover.ConditionState{conditionTrue}, true),
			},
			want: []string{string(cover.CoverageCovered)},
		},
		{
			name:     "AND preserves not evaluated as a distinct value",
			metadata: decisionMetadata(and(condition(0), condition(1))),
			evaluations: []cover.DecisionEvaluation{
				completed(1, []cover.ConditionState{conditionFalse, notEvaluated}, false),
				completed(2, []cover.ConditionState{conditionTrue, conditionTrue}, true),
				completed(3, []cover.ConditionState{conditionTrue, conditionFalse}, false),
			},
			want: []string{string(cover.CoverageInfeasible), string(cover.CoverageCovered)},
		},
		{
			name:     "OR preserves not evaluated as a distinct value",
			metadata: decisionMetadata(or(condition(0), condition(1))),
			evaluations: []cover.DecisionEvaluation{
				completed(1, []cover.ConditionState{conditionTrue, notEvaluated}, true),
				completed(2, []cover.ConditionState{conditionFalse, conditionTrue}, true),
				completed(3, []cover.ConditionState{conditionFalse, conditionFalse}, false),
			},
			want: []string{string(cover.CoverageInfeasible), string(cover.CoverageCovered)},
		},
		{
			name: "nested a AND b OR c",
			metadata: decisionMetadata(and(
				condition(0),
				or(condition(1), condition(2)),
			)),
			evaluations: []cover.DecisionEvaluation{
				completed(1, []cover.ConditionState{conditionFalse, notEvaluated, notEvaluated}, false),
				completed(2, []cover.ConditionState{conditionTrue, conditionTrue, notEvaluated}, true),
				completed(3, []cover.ConditionState{conditionTrue, conditionFalse, conditionFalse}, false),
				completed(4, []cover.ConditionState{conditionTrue, conditionFalse, conditionTrue}, true),
			},
			want: []string{
				string(cover.CoverageInfeasible),
				string(cover.CoverageInfeasible),
				string(cover.CoverageCovered),
			},
		},
		{
			name: "same not evaluated minor state is allowed",
			metadata: cover.DecisionMetadata{
				ID: testDecisionID,
				Conditions: []cover.ConditionMetadata{
					{Index: 0},
					{Index: 1},
				},
			},
			evaluations: []cover.DecisionEvaluation{
				completed(1, []cover.ConditionState{conditionFalse, notEvaluated}, false),
				completed(2, []cover.ConditionState{conditionTrue, notEvaluated}, true),
			},
			want: []string{string(cover.CoverageCovered), string(cover.CoverageNotCovered)},
		},
		{
			name:     "aborted and invalid evaluations never establish coverage",
			metadata: decisionMetadata(condition(0)),
			evaluations: []cover.DecisionEvaluation{
				completed(1, []cover.ConditionState{conditionFalse}, false),
				aborted(2, []cover.ConditionState{conditionTrue}),
				{
					DecisionID: testDecisionID,
					Conditions: []cover.ConditionState{cover.ConditionState(99)},
					Status:     cover.EvaluationCompleted,
				},
				completedFor(99, 3, []cover.ConditionState{conditionTrue}, true),
			},
			want:        []string{string(cover.CoverageNotCovered)},
			wantAborted: 1,
			wantInvalid: 1,
		},
		{
			name:     "NOT uses the observed atomic value",
			metadata: decisionMetadata(not(condition(0))),
			evaluations: []cover.DecisionEvaluation{
				completed(1, []cover.ConditionState{conditionFalse}, true),
				completed(2, []cover.ConditionState{conditionTrue}, false),
			},
			want: []string{string(cover.CoverageCovered)},
		},
	}

	strategy := UniqueCauseStrategy{}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := strategy.Analyze(test.metadata, test.evaluations)
			assertConditionStatuses(t, got, test.want)
			if got.AbortedEvaluations != test.wantAborted {
				t.Fatalf("AbortedEvaluations = %d, want %d", got.AbortedEvaluations, test.wantAborted)
			}
			if got.InvalidEvaluations != test.wantInvalid {
				t.Fatalf("InvalidEvaluations = %d, want %d", got.InvalidEvaluations, test.wantInvalid)
			}
		})
	}
}

func TestUniqueCauseIndexedWitnessMatchesExhaustiveSearch(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		evaluations []cover.DecisionEvaluation
		target      uint16
	}{
		{
			name:        "lexicographically last pair is found after probe limit",
			evaluations: indexedUniqueCauseEvaluations(64, true, false),
		},
		{
			name:        "large set without a witness remains uncovered",
			evaluations: indexedUniqueCauseEvaluations(64, false, false),
		},
		{
			name:        "not-evaluated target is excluded from the index",
			evaluations: indexedUniqueCauseEvaluations(64, true, true),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, gotFound := uniqueCauseWitness(test.evaluations, test.target)
			wantFirst, wantSecond, wantFound := exhaustiveUniqueCausePair(test.evaluations, test.target)
			if gotFound != wantFound {
				t.Fatalf("indexed witness found = %t, exhaustive = %t", gotFound, wantFound)
			}
			if !wantFound {
				if got != nil {
					t.Fatalf("indexed witness = %#v, want nil", got)
				}
				return
			}
			if got == nil || got.First.EvaluationID != test.evaluations[wantFirst].EvaluationID ||
				got.Second.EvaluationID != test.evaluations[wantSecond].EvaluationID {
				t.Fatalf("indexed witness = %#v, want evaluation indexes (%d,%d)", got, wantFirst, wantSecond)
			}
		})
	}
}

func indexedUniqueCauseEvaluations(count int, withWitness, includeNotEvaluated bool) []cover.DecisionEvaluation {
	const conditionCount = 7
	evaluations := make([]cover.DecisionEvaluation, count)
	for index := range evaluations {
		states := make([]cover.ConditionState, conditionCount)
		states[0] = conditionFalse
		key := index
		if index >= count-2 {
			key = count - 1
		}
		for conditionIndex := 1; conditionIndex < conditionCount; conditionIndex++ {
			states[conditionIndex] = conditionFalse
			if key&(1<<(conditionIndex-1)) != 0 {
				states[conditionIndex] = conditionTrue
			}
		}
		evaluations[index] = completed(cover.EvaluationID(index+1), states, false)
	}
	if withWitness {
		last := &evaluations[len(evaluations)-1]
		last.Conditions[0] = conditionTrue
		last.Result = true
	}
	if includeNotEvaluated {
		evaluations[0].Conditions[0] = notEvaluated
	}
	return evaluations
}

func exhaustiveUniqueCausePair(evaluations []cover.DecisionEvaluation, target uint16) (int, int, bool) {
	for first := 0; first < len(evaluations); first++ {
		for second := first + 1; second < len(evaluations); second++ {
			if uniqueCausePairMatches(evaluations[first], evaluations[second], target) {
				return first, second, true
			}
		}
	}
	return 0, 0, false
}

func TestMaskingStrategy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		metadata    cover.DecisionMetadata
		evaluations []cover.DecisionEvaluation
		want        []string
		wantOverall string
		wantInvalid int
	}{
		{
			name:     "one condition",
			metadata: decisionMetadata(condition(0)),
			evaluations: []cover.DecisionEvaluation{
				completed(1, []cover.ConditionState{conditionFalse}, false),
				completed(2, []cover.ConditionState{conditionTrue}, true),
			},
			want:        []string{string(cover.CoverageCovered)},
			wantOverall: string(cover.CoverageCovered),
		},
		{
			name:     "AND short circuit",
			metadata: decisionMetadata(and(condition(0), condition(1))),
			evaluations: []cover.DecisionEvaluation{
				completed(1, []cover.ConditionState{conditionFalse, notEvaluated}, false),
				completed(2, []cover.ConditionState{conditionTrue, conditionTrue}, true),
				completed(3, []cover.ConditionState{conditionTrue, conditionFalse}, false),
			},
			want:        []string{string(cover.CoverageCovered), string(cover.CoverageCovered)},
			wantOverall: string(cover.CoverageCovered),
		},
		{
			name:     "OR short circuit",
			metadata: decisionMetadata(or(condition(0), condition(1))),
			evaluations: []cover.DecisionEvaluation{
				completed(1, []cover.ConditionState{conditionTrue, notEvaluated}, true),
				completed(2, []cover.ConditionState{conditionFalse, conditionTrue}, true),
				completed(3, []cover.ConditionState{conditionFalse, conditionFalse}, false),
			},
			want:        []string{string(cover.CoverageCovered), string(cover.CoverageCovered)},
			wantOverall: string(cover.CoverageCovered),
		},
		{
			name: "nested a AND b OR c",
			metadata: decisionMetadata(and(
				condition(0),
				or(condition(1), condition(2)),
			)),
			evaluations: []cover.DecisionEvaluation{
				completed(1, []cover.ConditionState{conditionFalse, notEvaluated, notEvaluated}, false),
				completed(2, []cover.ConditionState{conditionTrue, conditionTrue, notEvaluated}, true),
				completed(3, []cover.ConditionState{conditionTrue, conditionFalse, conditionFalse}, false),
				completed(4, []cover.ConditionState{conditionTrue, conditionFalse, conditionTrue}, true),
			},
			want: []string{
				string(cover.CoverageCovered),
				string(cover.CoverageCovered),
				string(cover.CoverageCovered),
			},
			wantOverall: string(cover.CoverageCovered),
		},
		{
			name: "target must be pivotal in both vectors",
			metadata: decisionMetadata(or(
				and(condition(0), condition(1)),
				condition(2),
			)),
			evaluations: []cover.DecisionEvaluation{
				completed(1, []cover.ConditionState{conditionFalse, notEvaluated, conditionFalse}, false),
				completed(2, []cover.ConditionState{conditionTrue, conditionFalse, conditionTrue}, true),
			},
			want: []string{
				string(cover.CoverageNotCovered),
				string(cover.CoverageNotCovered),
				string(cover.CoverageCovered),
			},
			wantOverall: string(cover.CoverageNotCovered),
		},
		{
			name: "differing conditions may be collectively masked",
			metadata: decisionMetadata(or(
				and(condition(0), condition(1)),
				condition(2),
			)),
			evaluations: []cover.DecisionEvaluation{
				completed(1, []cover.ConditionState{conditionFalse, notEvaluated, conditionFalse}, false),
				completed(2, []cover.ConditionState{conditionTrue, conditionFalse, conditionTrue}, true),
			},
			want: []string{
				string(cover.CoverageNotCovered),
				string(cover.CoverageNotCovered),
				string(cover.CoverageCovered),
			},
			wantOverall: string(cover.CoverageNotCovered),
		},
		{
			name:     "aborted evaluations are ignored",
			metadata: decisionMetadata(condition(0)),
			evaluations: []cover.DecisionEvaluation{
				completed(1, []cover.ConditionState{conditionFalse}, false),
				aborted(2, []cover.ConditionState{conditionTrue}),
			},
			want:        []string{string(cover.CoverageNotCovered)},
			wantOverall: string(cover.CoverageNotCovered),
		},
		{
			name: "constant sibling makes target structurally non-pivotal",
			metadata: decisionMetadata(and(
				condition(0),
				cover.NewConstantExpression(false),
			)),
			evaluations: []cover.DecisionEvaluation{
				completed(1, []cover.ConditionState{conditionFalse}, false),
				completed(2, []cover.ConditionState{conditionTrue}, false),
			},
			want:        []string{string(cover.CoverageInfeasible)},
			wantOverall: string(cover.CoverageInfeasible),
		},
		{
			name: "structurally impossible short circuit vector is invalid",
			metadata: decisionMetadata(and(
				condition(0),
				condition(1),
			)),
			evaluations: []cover.DecisionEvaluation{
				completed(1, []cover.ConditionState{conditionFalse, conditionTrue}, false),
				completed(2, []cover.ConditionState{conditionTrue, conditionTrue}, true),
			},
			want:        []string{string(cover.CoverageNotCovered), string(cover.CoverageNotCovered)},
			wantOverall: string(cover.CoverageNotCovered),
			wantInvalid: 1,
		},
		{
			name:     "recorded result inconsistent with expression is invalid",
			metadata: decisionMetadata(condition(0)),
			evaluations: []cover.DecisionEvaluation{
				completed(1, []cover.ConditionState{conditionFalse}, true),
				completed(2, []cover.ConditionState{conditionTrue}, true),
			},
			want:        []string{string(cover.CoverageNotCovered)},
			wantOverall: string(cover.CoverageNotCovered),
			wantInvalid: 1,
		},
		{
			name: "missing expression structure is unknown",
			metadata: cover.DecisionMetadata{
				ID:         testDecisionID,
				Conditions: []cover.ConditionMetadata{{Index: 0}},
			},
			evaluations: []cover.DecisionEvaluation{
				completed(1, []cover.ConditionState{conditionFalse}, false),
				completed(2, []cover.ConditionState{conditionTrue}, true),
			},
			want:        []string{string(cover.CoverageAnalysisIncomplete)},
			wantOverall: string(cover.CoverageAnalysisIncomplete),
		},
		{
			name: "unsupported expression structure is explicit",
			metadata: cover.DecisionMetadata{
				ID:             testDecisionID,
				Conditions:     []cover.ConditionMetadata{{Index: 0}},
				ExpressionTree: &cover.BooleanExpression{Kind: "xor"},
			},
			want:        []string{string(cover.SupportUnsupported)},
			wantOverall: string(cover.SupportUnsupported),
		},
	}

	strategy := MaskingStrategy{}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := strategy.Analyze(test.metadata, test.evaluations)
			assertConditionStatuses(t, got, test.want)
			if status := mcdcResultStatus(got); status != test.wantOverall {
				t.Fatalf("Status = %q, want %q", status, test.wantOverall)
			}
			if got.InvalidEvaluations != test.wantInvalid {
				t.Fatalf("InvalidEvaluations = %d, want %d", got.InvalidEvaluations, test.wantInvalid)
			}
		})
	}
}

func TestMaskingWitnessExplainsShortCircuitCompletion(t *testing.T) {
	t.Parallel()

	metadata := decisionMetadata(and(condition(0), condition(1)))
	result := (MaskingStrategy{}).Analyze(metadata, []cover.DecisionEvaluation{
		completed(8, []cover.ConditionState{conditionTrue, conditionTrue}, true),
		completed(7, []cover.ConditionState{conditionFalse, notEvaluated}, false),
	})

	witness := result.Conditions[0].Witness
	if witness == nil {
		t.Fatal("condition 0 has no masking witness")
	}
	if want := []cover.ConditionState{conditionFalse, conditionTrue}; !reflect.DeepEqual(witness.FirstCompletion, want) {
		t.Fatalf("FirstCompletion = %v, want %v", witness.FirstCompletion, want)
	}
	if want := []cover.ConditionState{conditionTrue, conditionTrue}; !reflect.DeepEqual(witness.SecondCompletion, want) {
		t.Fatalf("SecondCompletion = %v, want %v", witness.SecondCompletion, want)
	}
	if want := []uint16{1}; !reflect.DeepEqual(witness.UnobservedConditions, want) {
		t.Fatalf("UnobservedConditions = %v, want %v", witness.UnobservedConditions, want)
	}
	if len(witness.MaskedConditions) != 0 {
		t.Fatalf("MaskedConditions = %v, want none after equal completion", witness.MaskedConditions)
	}
}

func TestMaskingTreatsRepeatedSourceTextAsSeparateOccurrences(t *testing.T) {
	t.Parallel()
	metadata := decisionMetadata(and(condition(0), condition(1)))
	metadata.Conditions[0].Expression = "a"
	metadata.Conditions[1].Expression = "a"
	result := (MaskingStrategy{}).Analyze(metadata, []cover.DecisionEvaluation{
		completed(1, []cover.ConditionState{conditionFalse, notEvaluated}, false),
		completed(2, []cover.ConditionState{conditionTrue, conditionTrue}, true),
	})
	if mcdcConditionStatus(result.Conditions[0]) != string(cover.CoverageCovered) || result.Conditions[0].Witness == nil {
		t.Fatalf("repeated-condition masking result = %#v", result)
	}
}

func TestMaskingWitnessRecordsVaryingMaskedConditions(t *testing.T) {
	t.Parallel()

	metadata := decisionMetadata(or(and(condition(0), condition(1)), condition(2)))
	result := (MaskingStrategy{}).Analyze(metadata, []cover.DecisionEvaluation{
		completed(1, []cover.ConditionState{conditionFalse, notEvaluated, conditionFalse}, false),
		completed(2, []cover.ConditionState{conditionTrue, conditionFalse, conditionTrue}, true),
	})
	witness := result.Conditions[2].Witness
	if witness == nil {
		t.Fatal("condition 2 has no masking witness")
	}
	if want := []uint16{1}; !reflect.DeepEqual(witness.UnobservedConditions, want) {
		t.Fatalf("UnobservedConditions = %v, want %v", witness.UnobservedConditions, want)
	}
	if want := []uint16{0}; !reflect.DeepEqual(witness.MaskedConditions, want) {
		t.Fatalf("MaskedConditions = %v, want %v", witness.MaskedConditions, want)
	}
}

func TestMaskingJointSolverValidatesD19(t *testing.T) {
	t.Parallel()
	metadata := decisionMetadata(and(condition(0), or(condition(1), condition(2))))
	first := completed(1, []cover.ConditionState{conditionFalse, notEvaluated, notEvaluated}, false)
	second := completed(2, []cover.ConditionState{conditionTrue, conditionTrue, notEvaluated}, true)
	result := (MaskingStrategy{}).Analyze(metadata, []cover.DecisionEvaluation{first, second})
	witness := result.Conditions[0].Witness
	if mcdcConditionStatus(result.Conditions[0]) != string(cover.CoverageCovered) || witness == nil {
		t.Fatalf("target status = %q, witness = %#v", mcdcConditionStatus(result.Conditions[0]), witness)
	}
	for index := range witness.FirstCompletion {
		if uint16(index) == 0 || witness.FirstCompletion[index] == witness.SecondCompletion[index] {
			continue
		}
		if !oracleMasked(metadata.ExpressionTree, conditionStatesToBools(witness.FirstCompletion), uint16(index)) || !oracleMasked(metadata.ExpressionTree, conditionStatesToBools(witness.SecondCompletion), uint16(index)) {
			t.Fatalf("witness violates D19 masking at condition %d: %#v", index, witness)
		}
	}
}

func TestMaskingEvaluationPairLimitIsAnalysisIncomplete(t *testing.T) {
	t.Parallel()
	metadata := decisionMetadata(or(and(condition(0), condition(1)), condition(2)))
	evaluations := []cover.DecisionEvaluation{
		completed(1, []cover.ConditionState{conditionFalse, notEvaluated, conditionFalse}, false),
		completed(2, []cover.ConditionState{conditionTrue, conditionFalse, conditionTrue}, true),
		completed(3, []cover.ConditionState{conditionTrue, conditionTrue, notEvaluated}, true),
	}
	result := (MaskingStrategy{Budget: AnalysisBudget{MaxEvaluationPairs: 1}}).Analyze(metadata, evaluations)
	condition := result.Conditions[0]
	if status := mcdcConditionStatus(condition); status != string(cover.CoverageAnalysisIncomplete) {
		t.Fatalf("target status = %q, want analysis-incomplete: %#v", status, condition)
	}
	if !strings.Contains(condition.Reason, "evaluation-pair count") {
		t.Fatalf("target reason = %q, want evaluation-pair limit", condition.Reason)
	}
	exact := (MaskingStrategy{Budget: AnalysisBudget{MaxEvaluationPairs: 2}}).Analyze(metadata, evaluations)
	if status := mcdcConditionStatus(exact.Conditions[0]); status != string(cover.CoverageCovered) {
		t.Fatalf("target status = %q, want covered at exact pair limit: %#v", status, exact.Conditions[0])
	}
}

func TestEffectiveMaskingAnalysisBudgetFillsOnlyZeroFields(t *testing.T) {
	t.Parallel()
	defaults := DefaultMaskingAnalysisBudget()
	effective := EffectiveMaskingAnalysisBudget(AnalysisBudget{MaxSearchStates: 17})
	if effective != (AnalysisBudget{
		MaxEvaluationPairs: defaults.MaxEvaluationPairs,
		MaxSearchStates:    17,
		MaxSolverBytes:     defaults.MaxSolverBytes,
	}) {
		t.Fatalf("effective Masking analysis budget = %#v", effective)
	}
}

func TestMaskingSearchStateLimitIsAnalysisIncomplete(t *testing.T) {
	t.Parallel()
	metadata := decisionMetadata(and(condition(0), condition(1)))
	evaluations := []cover.DecisionEvaluation{
		completed(1, []cover.ConditionState{conditionFalse, notEvaluated}, false),
		completed(2, []cover.ConditionState{conditionTrue, conditionTrue}, true),
	}
	var exactStates uint64
	(MaskingStrategy{}).analyze(metadata, evaluations, func(target uint16, stats maskingSearchStats) {
		if target == 0 {
			exactStates = stats.SearchStates
		}
	})
	if exactStates < 2 {
		t.Fatalf("default search states = %d, want at least 2", exactStates)
	}
	result := (MaskingStrategy{Budget: AnalysisBudget{MaxSearchStates: exactStates - 1}}).Analyze(metadata, evaluations)
	condition := result.Conditions[0]
	if status := mcdcConditionStatus(condition); status != string(cover.CoverageAnalysisIncomplete) {
		t.Fatalf("target status = %q, want analysis-incomplete: %#v", status, condition)
	}
	if !strings.Contains(condition.Reason, "search-state count") {
		t.Fatalf("target reason = %q, want search-state limit", condition.Reason)
	}
	exact := (MaskingStrategy{Budget: AnalysisBudget{MaxSearchStates: exactStates}}).Analyze(metadata, evaluations)
	if status := mcdcConditionStatus(exact.Conditions[0]); status != string(cover.CoverageCovered) {
		t.Fatalf("target status = %q, want covered at exact state limit: %#v", status, exact.Conditions[0])
	}
}

func TestMaskingSolverByteLimitIsAnalysisIncomplete(t *testing.T) {
	t.Parallel()
	metadata := decisionMetadata(and(condition(0), condition(1)))
	evaluations := []cover.DecisionEvaluation{
		completed(1, []cover.ConditionState{conditionFalse, notEvaluated}, false),
		completed(2, []cover.ConditionState{conditionTrue, conditionTrue}, true),
	}
	required := maskingSolverBytes(expressionNodeCount(metadata.ExpressionTree), len(metadata.Conditions), 2)
	result := (MaskingStrategy{Budget: AnalysisBudget{MaxSolverBytes: required - 1}}).Analyze(metadata, evaluations)
	condition := result.Conditions[0]
	if status := mcdcConditionStatus(condition); status != string(cover.CoverageAnalysisIncomplete) {
		t.Fatalf("target status = %q, want analysis-incomplete: %#v", status, condition)
	}
	if !strings.Contains(condition.Reason, "solver byte") {
		t.Fatalf("target reason = %q, want solver byte limit", condition.Reason)
	}
	exact := (MaskingStrategy{Budget: AnalysisBudget{MaxSolverBytes: required}}).Analyze(metadata, evaluations)
	if status := mcdcConditionStatus(exact.Conditions[0]); status != string(cover.CoverageCovered) {
		t.Fatalf("target status = %q, want covered at exact solver-byte limit: %#v", status, exact.Conditions[0])
	}
}

func oracleMasked(expression *cover.BooleanExpression, values []bool, target uint16) bool {
	return oracleEvaluate(expression, values, -1, false) == oracleEvaluate(expression, values, int(target), !values[target])
}

func conditionStatesToBools(states []cover.ConditionState) []bool {
	values := make([]bool, len(states))
	for index, state := range states {
		values[index], _ = state.Bool()
	}
	return values
}

func oracleEvaluate(expression *cover.BooleanExpression, values []bool, override int, replacement bool) bool {
	switch expression.Kind {
	case cover.BooleanExpressionCondition:
		if int(expression.ConditionIndex) == override {
			return replacement
		}
		return values[expression.ConditionIndex]
	case cover.BooleanExpressionConstant:
		return expression.Constant
	case cover.BooleanExpressionNot:
		return !oracleEvaluate(expression.Left, values, override, replacement)
	case cover.BooleanExpressionAnd:
		return oracleEvaluate(expression.Left, values, override, replacement) && oracleEvaluate(expression.Right, values, override, replacement)
	case cover.BooleanExpressionOr:
		return oracleEvaluate(expression.Left, values, override, replacement) || oracleEvaluate(expression.Right, values, override, replacement)
	default:
		return false
	}
}

func TestStrategiesAggregateDuplicatesAndChooseDeterministicWitnesses(t *testing.T) {
	t.Parallel()

	metadata := decisionMetadata(condition(0))
	evaluations := []cover.DecisionEvaluation{
		completed(9, []cover.ConditionState{conditionFalse}, false),
		completed(8, []cover.ConditionState{conditionTrue}, true),
		completed(1, []cover.ConditionState{conditionFalse}, false),
		completed(2, []cover.ConditionState{conditionTrue}, true),
	}
	original := make([]cover.DecisionEvaluation, len(evaluations))
	for index, evaluation := range evaluations {
		original[index] = evaluation
		original[index].Conditions = append([]cover.ConditionState(nil), evaluation.Conditions...)
	}
	reversed := slices.Clone(evaluations)
	slices.Reverse(reversed)

	strategies := []MCDCStrategy{UniqueCauseStrategy{}, MaskingStrategy{}}
	for _, strategy := range strategies {
		forward := strategy.Analyze(metadata, evaluations)
		backward := strategy.Analyze(metadata, reversed)
		if !reflect.DeepEqual(forward, backward) {
			t.Fatalf("%T result depends on input order:\nforward: %#v\nbackward: %#v", strategy, forward, backward)
		}
		if forward.EvaluationsAnalyzed != 2 {
			t.Fatalf("%T EvaluationsAnalyzed = %d, want 2 aggregated vectors", strategy, forward.EvaluationsAnalyzed)
		}
		witness := forward.Conditions[0].Witness
		if witness == nil {
			t.Fatalf("%T did not produce a witness", strategy)
		}
		if witness.First.EvaluationID != 1 || witness.Second.EvaluationID != 2 {
			t.Fatalf("%T witness IDs = (%d, %d), want (1, 2)",
				strategy,
				witness.First.EvaluationID,
				witness.Second.EvaluationID,
			)
		}
		if witness.First.TestID != cover.UnknownTestID || witness.Second.TestID != cover.UnknownTestID {
			t.Fatalf("%T did not normalize missing test IDs: %#v", strategy, witness)
		}
	}
	if !reflect.DeepEqual(evaluations, original) {
		t.Fatalf("Analyze mutated its input:\ngot:  %#v\nwant: %#v", evaluations, original)
	}
}

func TestMaskingLargeReadOnceExpressionHasNoCompletionLimit(t *testing.T) {
	t.Parallel()

	const conditionCount = 64
	expression := condition(0)
	for index := 1; index < conditionCount; index++ {
		expression = or(expression, condition(uint16(index)))
	}
	metadata := decisionMetadata(expression)
	first := make([]cover.ConditionState, conditionCount)
	first[0] = conditionTrue
	for index := 1; index < conditionCount; index++ {
		first[index] = notEvaluated
	}
	second := make([]cover.ConditionState, conditionCount)
	for index := range second {
		second[index] = conditionFalse
	}

	result := (MaskingStrategy{}).Analyze(metadata, []cover.DecisionEvaluation{
		completed(1, first, true),
		completed(2, second, false),
	})
	if status := mcdcConditionStatus(result.Conditions[0]); status != string(cover.CoverageCovered) {
		t.Fatalf("condition 0 status = %q, want covered", status)
	}
	witness := result.Conditions[0].Witness
	if witness == nil {
		t.Fatal("large expression has no masking witness")
	}
	for index := 1; index < conditionCount; index++ {
		if witness.FirstCompletion[index] != conditionFalse ||
			witness.SecondCompletion[index] != conditionFalse {
			t.Fatalf("condition %d completion = (%v, %v), want (false, false)",
				index,
				witness.FirstCompletion[index],
				witness.SecondCompletion[index],
			)
		}
	}
}

func TestLinearPivotalSolverMatchesExhaustiveSearch(t *testing.T) {
	t.Parallel()

	expressions := []*cover.BooleanExpression{
		condition(0),
		not(condition(0)),
		and(condition(0), condition(1)),
		or(condition(0), condition(1)),
		and(condition(0), or(condition(1), condition(2))),
		or(and(condition(0), condition(1)), condition(2)),
		and(not(or(condition(0), condition(1))), condition(2)),
	}
	for expressionIndex, expression := range expressions {
		metadata := decisionMetadata(expression)
		states := make([]cover.ConditionState, len(metadata.Conditions))
		forEachStateVector(states, func(vector []cover.ConditionState) {
			for target := range vector {
				if !vector[target].IsEvaluated() {
					continue
				}
				for _, result := range []bool{false, true} {
					evaluation := cover.DecisionEvaluation{
						DecisionID: testDecisionID,
						Conditions: append([]cover.ConditionState(nil), vector...),
						Result:     result,
						Status:     cover.EvaluationCompleted,
					}
					_, got := pivotalCompletion(expression, evaluation, uint16(target))
					want := exhaustivePivotalCompletionExists(expression, evaluation, uint16(target))
					if got != want {
						t.Fatalf("expression %d vector %v target %d result %t: solver = %t, exhaustive = %t",
							expressionIndex,
							vector,
							target,
							result,
							got,
							want,
						)
					}
				}
			}
		})
	}
}

func forEachStateVector(states []cover.ConditionState, visit func([]cover.ConditionState)) {
	var enumerate func(int)
	enumerate = func(index int) {
		if index == len(states) {
			visit(states)
			return
		}
		for _, state := range []cover.ConditionState{notEvaluated, conditionFalse, conditionTrue} {
			states[index] = state
			enumerate(index + 1)
		}
	}
	enumerate(0)
}

func exhaustivePivotalCompletionExists(
	expression *cover.BooleanExpression,
	evaluation cover.DecisionEvaluation,
	target uint16,
) bool {
	values := make([]bool, len(evaluation.Conditions))
	unknown := make([]int, 0)
	for index, state := range evaluation.Conditions {
		if value, evaluated := state.Bool(); evaluated {
			values[index] = value
		} else {
			unknown = append(unknown, index)
		}
	}
	var enumerate func(int) bool
	enumerate = func(position int) bool {
		if position == len(unknown) {
			return evaluateFull(expression, values, -1, false) == evaluation.Result &&
				isPivotal(expression, values, target)
		}
		index := unknown[position]
		values[index] = false
		if enumerate(position + 1) {
			return true
		}
		values[index] = true
		return enumerate(position + 1)
	}
	return enumerate(0)
}

func TestMalformedMetadataIsNotSilentlyUncovered(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		metadata cover.DecisionMetadata
		want     string
	}{
		{
			name: "duplicate metadata index",
			metadata: cover.DecisionMetadata{
				ID: testDecisionID,
				Conditions: []cover.ConditionMetadata{
					{Index: 0},
					{Index: 0},
				},
			},
			want: string(cover.CoverageAnalysisIncomplete),
		},
		{
			name: "duplicate expression leaf",
			metadata: cover.DecisionMetadata{
				ID:             testDecisionID,
				Conditions:     []cover.ConditionMetadata{{Index: 0}},
				ExpressionTree: and(condition(0), condition(0)),
			},
			want: string(cover.CoverageAnalysisIncomplete),
		},
		{
			name: "cyclic expression",
			metadata: func() cover.DecisionMetadata {
				cycle := &cover.BooleanExpression{Kind: cover.BooleanExpressionNot}
				cycle.Left = cycle
				return cover.DecisionMetadata{
					ID:             testDecisionID,
					Conditions:     []cover.ConditionMetadata{{Index: 0}},
					ExpressionTree: cycle,
				}
			}(),
			want: string(cover.CoverageAnalysisIncomplete),
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result := (MaskingStrategy{}).Analyze(test.metadata, nil)
			if status := mcdcResultStatus(result); status != test.want {
				t.Fatalf("Status = %q, want %q (%s)", status, test.want, result.Reason)
			}
		})
	}
}

func TestValidateCompletedEvaluationRejectsImpossibleOrFalseEvidence(t *testing.T) {
	t.Parallel()
	metadata := decisionMetadata(and(condition(0), condition(1)))
	tests := []struct {
		name       string
		evaluation cover.DecisionEvaluation
		wantError  bool
	}{
		{
			name:       "valid short circuit",
			evaluation: completed(1, []cover.ConditionState{conditionFalse, notEvaluated}, false),
		},
		{
			name:       "evaluated skipped condition",
			evaluation: completed(2, []cover.ConditionState{conditionFalse, conditionTrue}, false),
			wantError:  true,
		},
		{
			name:       "false recorded result",
			evaluation: completed(3, []cover.ConditionState{conditionTrue, conditionTrue}, false),
			wantError:  true,
		},
		{
			name:       "aborted is not completed evidence",
			evaluation: aborted(4, []cover.ConditionState{conditionFalse, conditionTrue}),
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateCompletedEvaluation(metadata, test.evaluation)
			if (err != nil) != test.wantError {
				t.Fatalf("ValidateCompletedEvaluation() error = %v, wantError=%t", err, test.wantError)
			}
		})
	}
}

func decisionMetadata(expression *cover.BooleanExpression) cover.DecisionMetadata {
	indexes, issue := expressionIndexes(expression)
	if issue != nil {
		panic(issue.reason)
	}
	conditions := make([]cover.ConditionMetadata, len(indexes))
	for index := range conditions {
		conditions[index] = cover.ConditionMetadata{Index: uint16(index)}
	}
	return cover.DecisionMetadata{
		ID:             testDecisionID,
		Conditions:     conditions,
		ExpressionTree: expression,
	}
}

func condition(index uint16) *cover.BooleanExpression {
	return cover.NewConditionExpression(index)
}

func not(operand *cover.BooleanExpression) *cover.BooleanExpression {
	return cover.NewNotExpression(operand)
}

func and(left, right *cover.BooleanExpression) *cover.BooleanExpression {
	return cover.NewAndExpression(left, right)
}

func or(left, right *cover.BooleanExpression) *cover.BooleanExpression {
	return cover.NewOrExpression(left, right)
}

func completed(
	evaluationID cover.EvaluationID,
	states []cover.ConditionState,
	result bool,
) cover.DecisionEvaluation {
	return completedFor(testDecisionID, evaluationID, states, result)
}

func completedFor(
	decisionID cover.DecisionID,
	evaluationID cover.EvaluationID,
	states []cover.ConditionState,
	result bool,
) cover.DecisionEvaluation {
	return cover.DecisionEvaluation{
		DecisionID:   decisionID,
		EvaluationID: evaluationID,
		Conditions:   states,
		Result:       result,
		Status:       cover.EvaluationCompleted,
	}
}

func aborted(evaluationID cover.EvaluationID, states []cover.ConditionState) cover.DecisionEvaluation {
	return cover.DecisionEvaluation{
		DecisionID:   testDecisionID,
		EvaluationID: evaluationID,
		Conditions:   states,
		Status:       cover.EvaluationAborted,
	}
}

func assertConditionStatuses(t *testing.T, result cover.MCDCResult, want []string) {
	t.Helper()
	if len(result.Conditions) != len(want) {
		t.Fatalf("got %d condition results, want %d: %#v", len(result.Conditions), len(want), result)
	}
	for index, wantStatus := range want {
		status := mcdcConditionStatus(result.Conditions[index])
		if status != wantStatus {
			t.Errorf("condition %d status = %q, want %q (reason: %s)",
				index,
				status,
				wantStatus,
				result.Conditions[index].Reason,
			)
		}
	}
}

func mcdcResultStatus(result cover.MCDCResult) string {
	return mcdcStatus(result.Outcome, result.Support, result.Analysis)
}

func mcdcConditionStatus(result cover.MCDCConditionResult) string {
	return mcdcStatus(result.Outcome, result.Support, result.Analysis)
}

func mcdcStatus(outcome cover.CoverageOutcome, support cover.SupportStatus, analysis cover.AnalysisStatus) string {
	switch {
	case support == cover.SupportUnsupported:
		return string(cover.SupportUnsupported)
	case support == cover.SupportUnknown:
		return string(cover.SupportUnknown)
	case analysis == cover.AnalysisIncomplete:
		return string(cover.CoverageAnalysisIncomplete)
	case analysis == cover.AnalysisInfeasible:
		return string(cover.CoverageInfeasible)
	case outcome == cover.CoverageOutcomeCovered:
		return string(cover.CoverageCovered)
	case outcome == cover.CoverageOutcomeNotCovered:
		return string(cover.CoverageNotCovered)
	default:
		return string(cover.SupportUnknown)
	}
}
