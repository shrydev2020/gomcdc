package c0_test

import (
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/c0"
)

func TestAcceptAndProjectCoverEvidenceUsesOriginalInventory(t *testing.T) {
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
		t.Fatal(err)
	}
	correspondence, err := c0.PlanCoverageCorrespondence(t.Context(), c0.CorrespondencePlanInput{
		PackagePath: "example.test/m/p", OriginalPath: "p/p.go", Original: inventory, Rewritten: inventory,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := c0.SourceCoveragePlan{
		PackagePath: "example.test/m/p", OriginalPath: "p/p.go", OriginalSource: []byte(source),
		Inventory: inventory, Correspondence: correspondence,
	}
	profile := profileFromInventory("example.test/m/p/p.go", inventory)
	if len(profile.Files[0].Blocks) < 2 {
		t.Fatalf("fixture needs multiple blocks: %#v", inventory)
	}
	profile.Files[0].Blocks[0].Count = 1

	accepted, err := c0.AcceptProfileEvidence(t.Context(), profile, []c0.SourceCoveragePlan{plan}, c0.ProfileAcceptanceOptions{
		ModulePath: "example.test/m", RunComplete: true,
	})
	if err != nil {
		t.Fatalf("AcceptProfileEvidence: %v", err)
	}
	report, err := c0.ProjectAcceptedEvidence(t.Context(), "example.test/m", accepted, c0.Options{})
	if err != nil {
		t.Fatalf("ProjectAcceptedEvidence: %v", err)
	}
	if report.Summary.Statements.Total == 0 || report.Summary.Statements.Covered != profile.Files[0].Blocks[0].Statements {
		t.Fatalf("statement summary = %#v", report.Summary.Statements)
	}
	if len(report.Packages) != 1 || !report.Packages[0].Evidence {
		t.Fatalf("packages = %#v", report.Packages)
	}
}

func TestAcceptCoverEvidenceFailsClosedAndRetainsIncompleteRun(t *testing.T) {
	t.Parallel()

	const source = "package p\nfunc F() { println(1) }\n"
	inventory, err := c0.BuildInventory("p/p.go", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	correspondence, err := c0.PlanCoverageCorrespondence(t.Context(), c0.CorrespondencePlanInput{
		PackagePath: "example.test/m/p", OriginalPath: "p/p.go", Original: inventory, Rewritten: inventory,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := c0.SourceCoveragePlan{
		PackagePath: "example.test/m/p", OriginalPath: "p/p.go", OriginalSource: []byte(source),
		Inventory: inventory, Correspondence: correspondence,
	}

	_, err = c0.AcceptProfileEvidence(t.Context(), c0.Profile{Mode: c0.ModeSet}, []c0.SourceCoveragePlan{plan}, c0.ProfileAcceptanceOptions{
		ModulePath: "example.test/m", RunComplete: true,
	})
	if err == nil || !strings.Contains(err.Error(), "omitted planned region") {
		t.Fatalf("complete missing-region error = %v", err)
	}

	accepted, err := c0.AcceptProfileEvidence(t.Context(), c0.Profile{Mode: c0.ModeSet}, []c0.SourceCoveragePlan{plan}, c0.ProfileAcceptanceOptions{
		ModulePath: "example.test/m", RunComplete: false,
	})
	if err != nil {
		t.Fatalf("partial AcceptProfileEvidence: %v", err)
	}
	projected, err := c0.ProjectAcceptedEvidence(t.Context(), "example.test/m", accepted, c0.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if projected.Summary.Statements.Total == 0 || projected.Summary.Statements.Covered != 0 || projected.Packages[0].Evidence {
		t.Fatalf("partial projection = %#v", projected)
	}

	unknown := c0.Profile{Mode: c0.ModeSet, Files: []c0.ProfileFile{{Path: "example.test/m/p/unknown.go", Blocks: []c0.ProfileBlock{{
		Position: c0.SourceRange{Start: c0.Position{Line: 1, Column: 1}, End: c0.Position{Line: 1, Column: 2}}, Statements: 1,
	}}}}}
	_, err = c0.AcceptProfileEvidence(t.Context(), unknown, []c0.SourceCoveragePlan{plan}, c0.ProfileAcceptanceOptions{ModulePath: "example.test/m"})
	if err == nil || !strings.Contains(err.Error(), "unknown profile region") {
		t.Fatalf("unknown-region error = %v", err)
	}

	mismatch := profileFromInventory("example.test/m/p/p.go", inventory)
	mismatch.Files[0].Blocks[0].Statements++
	_, err = c0.AcceptProfileEvidence(t.Context(), mismatch, []c0.SourceCoveragePlan{plan}, c0.ProfileAcceptanceOptions{ModulePath: "example.test/m"})
	if err == nil || !strings.Contains(err.Error(), "unknown profile region") {
		t.Fatalf("NumStmt mismatch error = %v", err)
	}
}

func TestAcceptCoverEvidenceRejectsNonProjectableCorrespondence(t *testing.T) {
	t.Parallel()

	const source = "package p\nfunc F() { println(1) }\n"
	inventory, err := c0.BuildInventory("p/p.go", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	block := inventory.Blocks[0]
	correspondence, err := c0.NewCoverageCorrespondence([]c0.RegionCorrespondence{{
		Region:   c0.CoverRegion{ProfilePath: block.ProfileFile, Range: block.ProfileRange, Statements: block.Statements},
		Relation: c0.CorrespondencePartial,
		Obligations: []c0.StatementObligation{{
			PackagePath: "example.test/m/p", OriginalPath: "p/p.go", BlockIndex: 0, StatementIndex: 0,
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c0.AcceptProfileEvidence(t.Context(), c0.Profile{Mode: c0.ModeSet}, []c0.SourceCoveragePlan{{
		PackagePath: "example.test/m/p", OriginalPath: "p/p.go", OriginalSource: []byte(source),
		Inventory: inventory, Correspondence: correspondence,
	}}, c0.ProfileAcceptanceOptions{ModulePath: "example.test/m"})
	if err == nil || !strings.Contains(err.Error(), "non-projectable partial") {
		t.Fatalf("non-projectable error = %v", err)
	}
}

func profileFromInventory(profilePath string, inventory c0.FileInventory) c0.Profile {
	profile := c0.Profile{Mode: c0.ModeSet, Files: []c0.ProfileFile{{Path: profilePath}}}
	for _, block := range inventory.Blocks {
		if block.Statements == 0 {
			continue
		}
		profile.Files[0].Blocks = append(profile.Files[0].Blocks, c0.ProfileBlock{
			Position: block.ProfileRange, Statements: block.Statements,
		})
	}
	return profile
}
