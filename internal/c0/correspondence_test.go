package c0_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/internal/c0"
)

func TestCoverageCorrespondenceOwnsDeterministicPlan(t *testing.T) {
	t.Parallel()

	first := obligation("example.test/m/b", "b.go", 2)
	second := obligation("example.test/m/a", "a.go", 1)
	input := []c0.RegionCorrespondence{
		{
			Region:      coverRegion("z.go", 20, 21, 2),
			Relation:    c0.CorrespondenceCoversAll,
			Obligations: []c0.StatementObligation{first, second},
		},
		{
			Region:      coverRegion("a.go", 10, 11),
			Relation:    c0.CorrespondenceExact,
			Obligations: []c0.StatementObligation{obligation("example.test/m/a", "a.go", 0)},
		},
	}
	correspondence, err := c0.NewCoverageCorrespondence(input)
	if err != nil {
		t.Fatalf("NewCoverageCorrespondence: %v", err)
	}

	input[0].Region.ProfilePath = "mutated.go"
	input[0].Obligations[0].OriginalPath = "mutated.go"
	got := correspondence.Regions()
	if got[0].Region.ProfilePath != "a.go" || got[1].Region.ProfilePath != "z.go" {
		t.Fatalf("region order = %#v", got)
	}
	if got[1].Obligations[0] != second || got[1].Obligations[1] != first {
		t.Fatalf("obligation order = %#v", got[1].Obligations)
	}

	got[0].Region.ProfilePath = "caller-mutation.go"
	got[0].Obligations[0].OriginalPath = "caller-mutation.go"
	again := correspondence.Regions()
	if again[0].Region.ProfilePath != "a.go" || again[0].Obligations[0].OriginalPath != "a.go" {
		t.Fatalf("returned plan aliases caller mutation: %#v", again[0])
	}
}

func TestCoverageCorrespondenceRejectsInvalidPlans(t *testing.T) {
	t.Parallel()

	validRegion := coverRegion("p.go", 1, 2)
	validObligation := obligation("example.test/m/p", "p.go", 0)
	tests := []struct {
		name    string
		regions []c0.RegionCorrespondence
		want    string
	}{
		{
			name: "empty profile path",
			regions: []c0.RegionCorrespondence{{
				Region: c0.CoverRegion{Range: validRegion.Range}, Relation: c0.CorrespondenceExact,
				Obligations: []c0.StatementObligation{validObligation},
			}},
			want: "profile path is empty",
		},
		{
			name: "backward range",
			regions: []c0.RegionCorrespondence{{
				Region: c0.CoverRegion{ProfilePath: "p.go", Range: correspondenceLineRange(2, 1)}, Relation: c0.CorrespondenceExact,
				Obligations: []c0.StatementObligation{validObligation},
			}},
			want: "range end precedes start",
		},
		{
			name: "unknown relation",
			regions: []c0.RegionCorrespondence{{
				Region: validRegion, Relation: c0.CorrespondenceRelation("invented"),
			}},
			want: `invalid relation "invented"`,
		},
		{
			name: "exact without obligation",
			regions: []c0.RegionCorrespondence{{
				Region: validRegion, Relation: c0.CorrespondenceExact,
			}},
			want: "at least 1 obligations",
		},
		{
			name: "covers all without obligation",
			regions: []c0.RegionCorrespondence{{
				Region: validRegion, Relation: c0.CorrespondenceCoversAll,
			}},
			want: "at least 1 obligations",
		},
		{
			name: "missing Go cover statement count",
			regions: []c0.RegionCorrespondence{{
				Region: c0.CoverRegion{ProfilePath: "p.go", Range: validRegion.Range}, Relation: c0.CorrespondenceExact,
				Obligations: []c0.StatementObligation{validObligation},
			}},
			want: "statement count must be positive",
		},
		{
			name: "projectable statement count differs",
			regions: []c0.RegionCorrespondence{{
				Region: coverRegion("p.go", 1, 2, 2), Relation: c0.CorrespondenceExact,
				Obligations: []c0.StatementObligation{validObligation},
			}},
			want: "statement count 2 but 1 original obligations",
		},
		{
			name: "generated with obligation",
			regions: []c0.RegionCorrespondence{{
				Region: validRegion, Relation: c0.CorrespondenceGenerated,
				Obligations: []c0.StatementObligation{validObligation},
			}},
			want: "exactly 0 obligations",
		},
		{
			name: "invalid obligation",
			regions: []c0.RegionCorrespondence{{
				Region: validRegion, Relation: c0.CorrespondenceExact,
				Obligations: []c0.StatementObligation{{PackagePath: "example.test/m/p", OriginalPath: "p.go", BlockIndex: -1}},
			}},
			want: "block index is negative",
		},
		{
			name: "invalid statement index",
			regions: []c0.RegionCorrespondence{{
				Region: validRegion, Relation: c0.CorrespondenceExact,
				Obligations: []c0.StatementObligation{{PackagePath: "example.test/m/p", OriginalPath: "p.go", BlockIndex: 0, StatementIndex: -1}},
			}},
			want: "statement index is negative",
		},
		{
			name: "duplicate obligation in region",
			regions: []c0.RegionCorrespondence{{
				Region: coverRegion("p.go", 1, 2, 2), Relation: c0.CorrespondenceCoversAll,
				Obligations: []c0.StatementObligation{validObligation, validObligation},
			}},
			want: "duplicate statement obligation",
		},
		{
			name: "duplicate cover region",
			regions: []c0.RegionCorrespondence{
				{Region: validRegion, Relation: c0.CorrespondenceExact, Obligations: []c0.StatementObligation{validObligation}},
				{Region: validRegion, Relation: c0.CorrespondenceGenerated},
			},
			want: "duplicate cover region",
		},
		{
			name: "multiple projectable regions",
			regions: []c0.RegionCorrespondence{
				{Region: validRegion, Relation: c0.CorrespondenceExact, Obligations: []c0.StatementObligation{validObligation}},
				{Region: coverRegion("p.go", 3, 4), Relation: c0.CorrespondenceExact, Obligations: []c0.StatementObligation{validObligation}},
			},
			want: "multiple projectable cover regions",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := c0.NewCoverageCorrespondence(test.regions)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewCoverageCorrespondence error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestCoverageCorrespondenceProjectionFailsClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		relation c0.CorrespondenceRelation
		want     string
	}{
		{name: "partial", relation: c0.CorrespondencePartial, want: "non-projectable partial correspondence"},
		{name: "ambiguous", relation: c0.CorrespondenceAmbiguous, want: "non-projectable ambiguous correspondence"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			obligations := []c0.StatementObligation{
				obligation("example.test/m/p", "p.go", 0),
				obligation("example.test/m/p", "p.go", 1),
			}
			if test.relation == c0.CorrespondencePartial {
				obligations = obligations[:1]
			}
			correspondence, err := c0.NewCoverageCorrespondence([]c0.RegionCorrespondence{{
				Region: coverRegion("p.go", 1, 2), Relation: test.relation, Obligations: obligations,
			}})
			if err != nil {
				t.Fatalf("NewCoverageCorrespondence: %v", err)
			}
			if _, err := correspondence.ProjectableRegions(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ProjectableRegions error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestCoverageCorrespondenceProjectsOnlyProvenRegions(t *testing.T) {
	t.Parallel()

	exact := c0.RegionCorrespondence{
		Region: coverRegion("p.go", 1, 2), Relation: c0.CorrespondenceExact,
		Obligations: []c0.StatementObligation{obligation("example.test/m/p", "p.go", 0)},
	}
	coversAll := c0.RegionCorrespondence{
		Region: coverRegion("p.go", 3, 4, 2), Relation: c0.CorrespondenceCoversAll,
		Obligations: []c0.StatementObligation{
			obligation("example.test/m/p", "p.go", 1),
			obligation("example.test/m/p", "p.go", 2),
		},
	}
	generated := c0.RegionCorrespondence{
		Region: coverRegion(".gomcdc/generated/p.go", 1, 2), Relation: c0.CorrespondenceGenerated,
	}
	correspondence, err := c0.NewCoverageCorrespondence([]c0.RegionCorrespondence{generated, coversAll, exact})
	if err != nil {
		t.Fatalf("NewCoverageCorrespondence: %v", err)
	}
	got, err := correspondence.ProjectableRegions()
	if err != nil {
		t.Fatalf("ProjectableRegions: %v", err)
	}
	if want := []c0.RegionCorrespondence{exact, coversAll}; !reflect.DeepEqual(got, want) {
		t.Fatalf("projectable regions = %#v, want %#v", got, want)
	}
}

func obligation(packagePath, originalPath string, blockIndex int) c0.StatementObligation {
	return c0.StatementObligation{PackagePath: packagePath, OriginalPath: originalPath, BlockIndex: blockIndex, StatementIndex: 0}
}

func coverRegion(profilePath string, startLine, endLine int, statements ...int) c0.CoverRegion {
	statementCount := 1
	if len(statements) > 0 {
		statementCount = statements[0]
	}
	return c0.CoverRegion{
		ProfilePath: profilePath, Range: correspondenceLineRange(startLine, endLine), Statements: statementCount,
	}
}

func correspondenceLineRange(startLine, endLine int) c0.SourceRange {
	return c0.SourceRange{
		Start: c0.Position{Line: startLine, Column: 1},
		End:   c0.Position{Line: endLine, Column: 1},
	}
}
