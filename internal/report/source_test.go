package report

import (
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/analysis"
	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
)

func TestSourceRangeOffsetsUseByteColumns(t *testing.T) {
	source := []byte("あa\n")
	location := cover.SourceLocation{
		Start: cover.Position{Line: 1, Column: 1},
		End:   cover.Position{Line: 1, Column: 5},
	}
	mapping := sourceRangeOffsets(location, source)
	if mapping.start != 0 || mapping.end != 4 || mapping.status != "mapped" {
		t.Fatalf("mapping = %#v, want 0:4 mapped", mapping)
	}
}

func TestSourceAnnotationsUseConditionOccurrenceIDs(t *testing.T) {
	location := cover.SourceLocation{File: "p.go", StartOffset: 0, EndOffset: 1}
	file := analysis.FileReport{Functions: []analysis.FunctionReport{{Decisions: []analysis.DecisionReport{{
		DecisionID: "decision-1",
		Conditions: []analysis.ConditionReport{
			{ConditionID: "0x0000000000000001", Index: 0, Expression: "a", Location: location, True: true, NotEvaluated: 2, MCDCUnique: analysis.MCDCConditionReport{Status: "not-covered"}, MCDCMasking: analysis.MCDCConditionReport{Status: "covered"}},
			{ConditionID: "0x0000000000000002", Index: 1, Expression: "b", Location: location},
		},
	}}}}}
	annotations := sourceAnnotations(file, []byte("ab"))
	ids := make(map[string]bool)
	for _, annotation := range annotations {
		if annotation.Metric != "condition" && annotation.Metric != "mcdc-unique" && annotation.Metric != "mcdc-masking" {
			continue
		}
		if ids[annotation.EntityID] {
			t.Fatalf("duplicate condition annotation ID %q", annotation.EntityID)
		}
		ids[annotation.EntityID] = true
	}
	for _, want := range []string{
		"0x0000000000000001",
		"0x0000000000000001:mcdc-unique",
		"0x0000000000000001:mcdc-masking",
		"0x0000000000000002",
		"0x0000000000000002:mcdc-unique",
		"0x0000000000000002:mcdc-masking",
	} {
		if !ids[want] {
			t.Errorf("missing annotation ID %q", want)
		}
	}
	for _, annotation := range annotations {
		if annotation.Metric == "condition" && annotation.EntityID == "0x0000000000000001" {
			for _, required := range []string{"Condition #0: a", "true: covered", "false: not covered", "not evaluated: 2", "Unique-Cause MC/DC: not-covered", "Masking MC/DC: covered"} {
				if !strings.Contains(annotation.Tooltip, required) {
					t.Errorf("condition tooltip missing %q: %q", required, annotation.Tooltip)
				}
			}
		}
	}
}

func TestSourceMappingFailureProducesDiagnostic(t *testing.T) {
	location := cover.SourceLocation{File: "p.go", Start: cover.Position{Line: 9, Column: 1}, End: cover.Position{Line: 9, Column: 2}}
	file := analysis.FileReport{Functions: []analysis.FunctionReport{{Decisions: []analysis.DecisionReport{{
		DecisionID: "decision-1", Location: location,
	}}}}}
	annotations := sourceAnnotations(file, []byte("a\n"))
	if len(annotations) != 1 || annotations[0].MappingStatus != "unknown" || annotations[0].MappingReason == "" {
		t.Fatalf("mapping diagnostic: %+v", annotations)
	}
	called := false
	if err := walkHTMLColors(2, annotations, func(_, _ int, classes string) bool {
		called = true
		if classes != "" {
			t.Errorf("unmapped annotation colored source: %s", classes)
		}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("unmapped source omitted")
	}

}

func TestDisabledSourceMetricsDoNotOverrideEnabledStatementColor(t *testing.T) {
	location := cover.SourceLocation{File: "p.go", StartOffset: 0, EndOffset: 1}
	file := analysis.FileReport{Functions: []analysis.FunctionReport{{
		Statements: []analysis.StatementReport{{Location: location, Covered: true, Metric: analysis.MetricSummary{Enabled: true, Covered: 1, Total: 1}}},
		Decisions:  []analysis.DecisionReport{{Location: location, Conditions: []analysis.ConditionReport{{Location: location, MCDCUnique: analysis.MCDCConditionReport{Status: "disabled"}, MCDCMasking: analysis.MCDCConditionReport{Status: "disabled"}}}}},
	}}}
	err := walkHTMLColors(1, sourceAnnotations(file, []byte("a")), func(_, _ int, classes string) bool {
		if !strings.Contains(classes, "view-combined-covered") || strings.Contains(classes, "view-decision-") {
			t.Errorf("disabled metrics changed colors: %s", classes)
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
}
