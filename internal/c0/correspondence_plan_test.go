package c0_test

import (
	"context"
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/c0"
)

func TestPlanCoverageCorrespondenceMatchesUnchangedInventoryExactly(t *testing.T) {
	t.Parallel()

	const source = `package p

func F(value bool) int {
	result := 0
	if value {
		result = 1
	}
	return result
}
`
	inventory, err := c0.BuildInventory("p/p.go", []byte(source))
	if err != nil {
		t.Fatalf("BuildInventory: %v", err)
	}
	correspondence, err := c0.PlanCoverageCorrespondence(t.Context(), c0.CorrespondencePlanInput{
		PackagePath: "example.test/m/p", OriginalPath: "p/p.go", Original: inventory, Rewritten: inventory,
	})
	if err != nil {
		t.Fatalf("PlanCoverageCorrespondence: %v", err)
	}
	regions, err := correspondence.ProjectableRegions()
	if err != nil {
		t.Fatalf("ProjectableRegions: %v", err)
	}
	wantRegions := 0
	wantObligations := 0
	for _, block := range inventory.Blocks {
		if block.Statements == 0 {
			continue
		}
		wantRegions++
		wantObligations += block.Statements
	}
	if len(regions) != wantRegions {
		t.Fatalf("region count = %d, want %d", len(regions), wantRegions)
	}
	gotObligations := 0
	for _, region := range regions {
		if region.Relation != c0.CorrespondenceExact {
			t.Fatalf("unchanged region relation = %q, want exact", region.Relation)
		}
		gotObligations += len(region.Obligations)
	}
	if gotObligations != wantObligations {
		t.Fatalf("obligation count = %d, want %d", gotObligations, wantObligations)
	}
}

func TestPlanCoverageCorrespondenceClassifiesCoversAllGeneratedAndMixedRegions(t *testing.T) {
	t.Parallel()

	first := plannerBlock("p.go", 1, 2, plannerStatement("p.go", 1))
	second := plannerBlock("p.go", 3, 4, plannerStatement("p.go", 3))
	generated := plannerStatement(".gomcdc/generated/p.go", 1)
	tests := []struct {
		name        string
		rewritten   c0.FileInventory
		want        c0.CorrespondenceRelation
		projectable bool
	}{
		{
			name: "covers all",
			rewritten: c0.FileInventory{Blocks: []c0.InventoryBlock{
				plannerBlock("p.go", 1, 4, plannerStatement("p.go", 1), plannerStatement("p.go", 3)),
			}},
			want: c0.CorrespondenceCoversAll, projectable: true,
		},
		{
			name: "generated",
			rewritten: c0.FileInventory{Blocks: []c0.InventoryBlock{
				plannerBlock(".gomcdc/generated/p.go", 1, 2, generated),
				first,
				second,
			}},
			want: c0.CorrespondenceGenerated, projectable: true,
		},
		{
			name: "mixed",
			rewritten: c0.FileInventory{Blocks: []c0.InventoryBlock{
				plannerBlock("p.go", 1, 3, plannerStatement("p.go", 1), generated),
				second,
			}},
			want: c0.CorrespondencePartial,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			correspondence, err := c0.PlanCoverageCorrespondence(t.Context(), c0.CorrespondencePlanInput{
				PackagePath: "example.test/m/p", OriginalPath: "p.go",
				Original: c0.FileInventory{Blocks: []c0.InventoryBlock{first, second}}, Rewritten: test.rewritten,
				GeneratedProfileFiles: []string{".gomcdc/generated/p.go"},
			})
			if err != nil {
				t.Fatalf("PlanCoverageCorrespondence: %v", err)
			}
			found := false
			for _, region := range correspondence.Regions() {
				if region.Relation == test.want {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("relations = %#v, want %q", correspondence.Regions(), test.want)
			}
			_, projectionErr := correspondence.ProjectableRegions()
			if test.projectable && projectionErr != nil {
				t.Fatalf("ProjectableRegions: %v", projectionErr)
			}
			if !test.projectable && projectionErr == nil {
				t.Fatal("ProjectableRegions accepted a non-projectable relation")
			}
		})
	}
}

func TestPlanCoverageCorrespondenceFailsClosedForUnknownAndAmbiguousAnchors(t *testing.T) {
	t.Parallel()

	t.Run("unknown", func(t *testing.T) {
		t.Parallel()
		original := c0.FileInventory{Blocks: []c0.InventoryBlock{plannerBlock("p.go", 1, 2, plannerStatement("p.go", 1))}}
		rewritten := c0.FileInventory{Blocks: []c0.InventoryBlock{plannerBlock("p.go", 8, 9, plannerStatement("p.go", 8))}}
		_, err := c0.PlanCoverageCorrespondence(t.Context(), c0.CorrespondencePlanInput{
			PackagePath: "example.test/m/p", OriginalPath: "p.go", Original: original, Rewritten: rewritten,
		})
		if err == nil || !strings.Contains(err.Error(), "has no original obligation") {
			t.Fatalf("PlanCoverageCorrespondence error = %v", err)
		}
	})

	t.Run("ambiguous", func(t *testing.T) {
		t.Parallel()
		shared := plannerStatement("p.go", 1)
		original := c0.FileInventory{Blocks: []c0.InventoryBlock{
			plannerBlock("p.go", 1, 2, shared),
			plannerBlock("p.go", 3, 4, shared),
		}}
		rewritten := c0.FileInventory{Blocks: []c0.InventoryBlock{
			plannerBlock("p.go", 1, 4, shared),
		}}
		correspondence, err := c0.PlanCoverageCorrespondence(t.Context(), c0.CorrespondencePlanInput{
			PackagePath: "example.test/m/p", OriginalPath: "p.go", Original: original, Rewritten: rewritten,
		})
		if err != nil {
			t.Fatalf("PlanCoverageCorrespondence: %v", err)
		}
		if got := correspondence.Regions(); len(got) != 1 || got[0].Relation != c0.CorrespondenceAmbiguous {
			t.Fatalf("regions = %#v, want one ambiguous relation", got)
		}
		if _, err := correspondence.ProjectableRegions(); err == nil || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("ProjectableRegions error = %v", err)
		}
	})
}

func TestPlanCoverageCorrespondenceUsesProvenSameLineOrderWhenColumnsMove(t *testing.T) {
	t.Parallel()

	original := c0.FileInventory{Blocks: []c0.InventoryBlock{
		plannerBlock("p.go", 4, 4, plannerStatementAt("p.go", 4, 4, 2)),
		plannerBlock("p.go", 4, 5, plannerStatementAt("p.go", 4, 4, 20)),
	}}
	rewritten := c0.FileInventory{Blocks: []c0.InventoryBlock{
		plannerBlock("p.go", 4, 4, plannerStatementAt("p.go", 10, 4, 8)),
		plannerBlock("p.go", 4, 5, plannerStatementAt("p.go", 11, 4, 9)),
	}}
	correspondence, err := c0.PlanCoverageCorrespondence(t.Context(), c0.CorrespondencePlanInput{
		PackagePath: "example.test/m/p", OriginalPath: "p.go", Original: original, Rewritten: rewritten,
	})
	if err != nil {
		t.Fatalf("PlanCoverageCorrespondence: %v", err)
	}
	regions, err := correspondence.ProjectableRegions()
	if err != nil {
		t.Fatalf("ProjectableRegions: %v", err)
	}
	if len(regions) != 2 || len(regions[0].Obligations) != 1 || len(regions[1].Obligations) != 1 {
		t.Fatalf("regions = %#v", regions)
	}
	if regions[0].Obligations[0].BlockIndex != 0 || regions[1].Obligations[0].BlockIndex != 1 {
		t.Fatalf("ordinal obligations = %#v", regions)
	}

	mismatch := c0.FileInventory{Blocks: rewritten.Blocks[:1]}
	correspondence, err = c0.PlanCoverageCorrespondence(t.Context(), c0.CorrespondencePlanInput{
		PackagePath: "example.test/m/p", OriginalPath: "p.go", Original: original, Rewritten: mismatch,
	})
	if err != nil {
		t.Fatalf("PlanCoverageCorrespondence mismatch: %v", err)
	}
	if _, err := correspondence.ProjectableRegions(); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ProjectableRegions mismatch error = %v", err)
	}
}

func TestPlanCoverageCorrespondenceHonorsCancellationAndInventoryShape(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c0.PlanCoverageCorrespondence(ctx, c0.CorrespondencePlanInput{}); err == nil {
		t.Fatal("PlanCoverageCorrespondence accepted canceled work")
	}

	invalid := plannerBlock("p.go", 1, 2, plannerStatement("p.go", 1))
	invalid.StatementUnits = nil
	_, err := c0.PlanCoverageCorrespondence(t.Context(), c0.CorrespondencePlanInput{
		PackagePath: "example.test/m/p", OriginalPath: "p.go",
		Original: c0.FileInventory{Blocks: []c0.InventoryBlock{invalid}},
	})
	if err == nil || !strings.Contains(err.Error(), "statement count 1 differs from unit count 0") {
		t.Fatalf("PlanCoverageCorrespondence error = %v", err)
	}
}

func plannerBlock(profileFile string, startLine, endLine int, statements ...c0.InventoryStatement) c0.InventoryBlock {
	anchors := make([]c0.Position, len(statements))
	for index, statement := range statements {
		anchors[index] = statement.ProfilePosition
	}
	return c0.InventoryBlock{
		PhysicalRange:  correspondenceLineRange(startLine, endLine),
		ProfileFile:    profileFile,
		ProfileRange:   correspondenceLineRange(startLine, endLine),
		ProfileAnchors: anchors,
		StatementUnits: append([]c0.InventoryStatement(nil), statements...),
		Statements:     len(statements),
	}
}

func plannerStatement(profileFile string, line int) c0.InventoryStatement {
	return plannerStatementAt(profileFile, line, line, 1)
}

func plannerStatementAt(profileFile string, physicalLine, logicalLine, logicalColumn int) c0.InventoryStatement {
	return c0.InventoryStatement{
		PhysicalPosition: c0.Position{Line: physicalLine, Column: 1},
		ProfileFile:      profileFile,
		ProfilePosition:  c0.Position{Line: logicalLine, Column: logicalColumn},
	}
}
