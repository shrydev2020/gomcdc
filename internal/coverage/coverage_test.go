package coverage

import "testing"

func TestConditionStatePreservesNotEvaluated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		state         ConditionState
		wantValue     bool
		wantEvaluated bool
		wantString    string
	}{
		{ConditionNotEvaluated, false, false, "not evaluated"},
		{ConditionFalse, false, true, "false"},
		{ConditionTrue, true, true, "true"},
	}
	for _, test := range tests {
		value, evaluated := test.state.Bool()
		if value != test.wantValue || evaluated != test.wantEvaluated {
			t.Errorf("%v.Bool() = (%t, %t), want (%t, %t)",
				test.state,
				value,
				evaluated,
				test.wantValue,
				test.wantEvaluated,
			)
		}
		if got := test.state.String(); got != test.wantString {
			t.Errorf("state.String() = %q, want %q", got, test.wantString)
		}
	}
}

func TestEvaluationIdentityIncludesProcessProvenance(t *testing.T) {
	t.Parallel()

	evaluation := DecisionEvaluation{
		EvaluationID: 9,
		RunID:        "run-1",
		PackagePath:  "example.com/module/pkg",
		ProcessID:    1234,
	}
	want := EvaluationIdentity{
		EvaluationID: 9,
		RunID:        "run-1",
		PackagePath:  "example.com/module/pkg",
		ProcessID:    1234,
	}
	if got := evaluation.Identity(); got != want {
		t.Fatalf("Identity() = %#v, want %#v", got, want)
	}
}

func TestCoverageCountPercentage(t *testing.T) {
	t.Parallel()

	if got := (CoverageCount{}).Percentage(); got != 0 {
		t.Fatalf("empty Percentage() = %v, want 0", got)
	}
	if got := (CoverageCount{Covered: 3, Total: 4}).Percentage(); got != 75 {
		t.Fatalf("Percentage() = %v, want 75", got)
	}
}

func TestClauseSelectionAndBodyExecutionAreDistinct(t *testing.T) {
	t.Parallel()

	selected := ClauseObservation{ClauseID: 7, Event: ClauseDirectSelection}
	body := ClauseObservation{ClauseID: 7, Event: ClauseBodyExecution}
	if selected == body {
		t.Fatal("direct selection and fallthrough body execution were conflated")
	}
}
