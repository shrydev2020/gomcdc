package analysis

import (
	"testing"

	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
)

func TestBuildContextIndexesEvidenceDeterministically(t *testing.T) {
	const decisionID = cover.DecisionID(7)
	ctx := newBuildContext(Input{
		Decisions: []cover.DecisionMetadata{{ID: decisionID, Package: "example.test/p"}},
		Evaluations: []cover.DecisionEvaluation{
			{DecisionID: decisionID, EvaluationID: 2, Status: cover.EvaluationCompleted},
			{DecisionID: decisionID, EvaluationID: 1, Status: cover.EvaluationCompleted},
		},
	})
	evaluations := ctx.evaluationsByDecision[decisionID]
	if len(evaluations) != 2 {
		t.Fatalf("evaluation count = %d, want 2", len(evaluations))
	}
	if evaluations[0].EvaluationID != 1 || evaluations[1].EvaluationID != 2 {
		t.Fatalf("evaluation order = %#v, want IDs 1, 2", evaluations)
	}
	if !ctx.astPackageEvidence["example.test/p"] {
		t.Fatal("completed evaluation did not mark package evidence")
	}
}

func TestMissingGeneratedProbeIsUnknownDespiteDeclaredCapability(t *testing.T) {
	input := parallelDecisionTestInput(1)
	input.Probes = cover.Probes{}
	built := Analyze(input)
	if built.Instrumentation.Total.Unknown == 0 || built.Summary.Decision.Unknown == 0 || built.Summary.MCDCMasking.Covered != 0 {
		t.Fatalf("missing probe treated as measured: %+v", built.Summary)
	}
	input.Probes.Decisions = []cover.DecisionID{input.Decisions[0].ID}
	measured := Analyze(input)
	if measured.Summary.Decision.Unknown != 0 || measured.Summary.Decision.Covered != 2 {
		t.Fatalf("generated probe not used: %+v", measured.Summary.Decision)
	}
}
