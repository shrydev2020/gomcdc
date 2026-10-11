package evidence

import (
	"context"
	"errors"
	"testing"

	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
	"github.com/shrydev2020/gomcdc/v2/internal/runtimecov"
)

func TestCanceledAcceptancePreservesProducerRejections(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, issues := AcceptRuntimeByProducer(ctx, nil, nil, runtimecov.RecordedEvidence{}, "", nil)
	if !errors.Is(issues.ASTError(), context.Canceled) || !errors.Is(issues.CompilerError(), context.Canceled) {
		t.Fatal("cancellation did not reject both producers")
	}
	if _, err := conditionlessSwitchDecisionOrder(ctx, []cover.ClauseMetadata{{}}); !errors.Is(err, context.Canceled) {
		t.Fatal("decision indexing ignored cancellation", err)
	}
}
