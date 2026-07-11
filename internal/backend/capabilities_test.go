package backend_test

import (
	"testing"

	"github.com/shrydev2020/gomcdc/internal/backend"
	cover "github.com/shrydev2020/gomcdc/internal/coverage"
)

func TestASTBackendAdvertisesSemanticBoundary(t *testing.T) {
	t.Parallel()

	capabilities := (backend.ASTBackend{}).Capabilities()
	for _, capability := range []backend.Capability{
		backend.CapabilityIfDecision,
		backend.CapabilityConditionlessSwitchDecision,
		backend.CapabilitySwitchClauseBody,
		backend.CapabilityTypeSwitchClauseBody,
		backend.CapabilitySelectClauseBody,
		backend.CapabilityMCDCUnique,
		backend.CapabilityMCDCMasking,
	} {
		if got := capabilities.Status(capability); got != backend.CapabilitySupported {
			t.Errorf("%s = %q, want supported", capability, got)
		}
	}
	for _, capability := range []backend.Capability{
		backend.CapabilityStatementCoverage,
		backend.CapabilityFunctionCoverage,
		backend.CapabilityExpressionSwitchMatchedExpression,
		backend.CapabilityTypeSwitchMatchedTypeAlternative,
		backend.CapabilityDirectCaseSelection,
		backend.CapabilityFallthroughEdge,
		backend.CapabilityCFGEdge,
		backend.CapabilityImplicitBranch,
	} {
		if got := capabilities.Status(capability); got != backend.CapabilityUnsupportedByBackend {
			t.Errorf("%s = %q, want unsupported-by-backend", capability, got)
		}
	}
	if got := capabilities.Status("future"); got != backend.CapabilityUnknown {
		t.Fatalf("unadvertised capability = %q, want unknown", got)
	}
}

func TestOrchestratedBackendKeepsProducerResponsibilitiesVisible(t *testing.T) {
	t.Parallel()

	standard := (backend.StandardCoverBackend{}).Capabilities()
	if standard.Status(backend.CapabilityStatementCoverage) != backend.CapabilitySupported || standard.Status(backend.CapabilityIfDecision) != backend.CapabilityUnknown {
		t.Fatalf("standard-cover capabilities = %#v", standard)
	}
	aggregate := (backend.OrchestratedBackend{}).Capabilities()
	if aggregate.Status(backend.CapabilityStatementCoverage) != backend.CapabilitySupported || aggregate.Status(backend.CapabilityIfDecision) != backend.CapabilitySupported || aggregate.Status(backend.CapabilityDirectCaseSelection) != backend.CapabilityUnsupportedByBackend {
		t.Fatalf("orchestrated capabilities = %#v", aggregate)
	}
	producers := backend.V1Producers()
	if len(producers) != 2 || producers[0].Backend != "ast" || producers[1].Backend != "standard-cover" {
		t.Fatalf("producer breakdown = %#v", producers)
	}
}

func TestCapabilityMappingsExposeBodyMetricsOnly(t *testing.T) {
	t.Parallel()

	if got, ok := backend.DecisionCapability(cover.DecisionSwitchCase); !ok || got != backend.CapabilityConditionlessSwitchDecision {
		t.Fatalf("conditionless decision capability = %q, %t", got, ok)
	}
	if got, ok := backend.ClauseBodyCapability(cover.ClauseTypeSwitch); !ok || got != backend.CapabilityTypeSwitchClauseBody {
		t.Fatalf("type switch body capability = %q, %t", got, ok)
	}
	if _, ok := backend.ClauseBodyCapability("matched-type-alternative"); ok {
		t.Fatal("exact type alternative was mapped to a body metric")
	}
}

func TestInstrumentationCoverageSeparatesCapabilityAndProbeProduction(t *testing.T) {
	t.Parallel()

	var coverage backend.InstrumentationCoverage
	coverage.Add(backend.CapabilitySupported, 2, true)
	coverage.Add(backend.CapabilitySupported, 1, false)
	coverage.Add(backend.CapabilityUnsupportedByBackend, 1, false)
	coverage.Add(backend.CapabilityUnknown, 1, false)
	want := backend.InstrumentationCoverage{
		Discovered: 5, Supported: 3, Instrumented: 2, Unsupported: 1, Unknown: 2, Percentage: 40,
	}
	if coverage != want {
		t.Fatalf("coverage = %#v, want %#v", coverage, want)
	}
	report := backend.NewInstrumentationReport(map[string]backend.InstrumentationCoverage{"decision": coverage})
	if !report.HasGaps() {
		t.Fatal("missing probe/unsupported entities did not create a strict-mode gap")
	}
}
