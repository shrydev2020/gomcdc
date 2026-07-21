package c0

import (
	"fmt"
	"sort"
)

// CorrespondenceRelation states what execution of one Go cover region proves
// about statement obligations from the original-source inventory.
type CorrespondenceRelation string

const (
	CorrespondenceExact     CorrespondenceRelation = "exact"
	CorrespondenceCoversAll CorrespondenceRelation = "covers-all"
	CorrespondencePartial   CorrespondenceRelation = "partial"
	CorrespondenceAmbiguous CorrespondenceRelation = "ambiguous"
	CorrespondenceGenerated CorrespondenceRelation = "generated"
)

// StatementObligation identifies one statement unit within an original-source
// inventory block. Both indexes are meaningful only within the named package
// and file inventory.
type StatementObligation struct {
	PackagePath    string
	OriginalPath   string
	BlockIndex     int
	StatementIndex int
}

// CoverRegion identifies one region emitted by Go cover after gomcdc source
// rewriting. It is evidence identity, never denominator authority.
type CoverRegion struct {
	ProfilePath string
	Range       SourceRange
	// Statements is the expected Go cover NumStmt for producer
	// compatibility. It never defines the original-source denominator.
	Statements int
}

// RegionCorrespondence records the logical guarantee made by one cover
// region. Partial and ambiguous relations are retained for diagnostics but are
// forbidden from coverage projection.
type RegionCorrespondence struct {
	Region      CoverRegion
	Relation    CorrespondenceRelation
	Obligations []StatementObligation
}

// CoverageCorrespondence is a validated, deterministically ordered plan from
// Go cover regions to original-source statement obligations. It owns copies of
// all nested slices so callers cannot mutate a plan after validation.
type CoverageCorrespondence struct {
	regions []RegionCorrespondence
}

type coverRegionIdentity struct {
	profilePath string
	sourceRange SourceRange
}

// NewCoverageCorrespondence validates and takes an immutable copy of a
// correspondence plan. A statement obligation may have only one projectable
// producer region; split or competing regions must remain partial or
// ambiguous until the planner can prove a stronger relation.
func NewCoverageCorrespondence(regions []RegionCorrespondence) (CoverageCorrespondence, error) {
	cloned := cloneRegionCorrespondences(regions)
	sortRegionCorrespondences(cloned)

	seenRegions := make(map[coverRegionIdentity]struct{}, len(cloned))
	seenProjectableObligations := make(map[StatementObligation]CoverRegion)
	for index := range cloned {
		region := &cloned[index]
		if err := validateRegionCorrespondence(*region); err != nil {
			return CoverageCorrespondence{}, fmt.Errorf("coverage correspondence region %d: %w", index, err)
		}
		identity := coverRegionIdentity{profilePath: region.Region.ProfilePath, sourceRange: region.Region.Range}
		if _, duplicate := seenRegions[identity]; duplicate {
			return CoverageCorrespondence{}, fmt.Errorf(
				"coverage correspondence contains duplicate cover region %q %s",
				region.Region.ProfilePath,
				formatRange(region.Region.Range),
			)
		}
		seenRegions[identity] = struct{}{}

		if region.Relation != CorrespondenceExact && region.Relation != CorrespondenceCoversAll {
			continue
		}
		for _, obligation := range region.Obligations {
			if previous, duplicate := seenProjectableObligations[obligation]; duplicate {
				return CoverageCorrespondence{}, fmt.Errorf(
					"statement obligation %q %q block %d statement %d has multiple projectable cover regions: %q %s and %q %s",
					obligation.PackagePath,
					obligation.OriginalPath,
					obligation.BlockIndex,
					obligation.StatementIndex,
					previous.ProfilePath,
					formatRange(previous.Range),
					region.Region.ProfilePath,
					formatRange(region.Region.Range),
				)
			}
			seenProjectableObligations[obligation] = region.Region
		}
	}
	return CoverageCorrespondence{regions: cloned}, nil
}

// Regions returns a deep copy of every planned region in deterministic order.
func (correspondence CoverageCorrespondence) Regions() []RegionCorrespondence {
	return cloneRegionCorrespondences(correspondence.regions)
}

// ProjectableRegions returns only relations which logically guarantee their
// listed obligations. Generated regions are excluded. Any partial or
// ambiguous relation fails the whole projection instead of guessing from
// source overlap.
func (correspondence CoverageCorrespondence) ProjectableRegions() ([]RegionCorrespondence, error) {
	projectable := make([]RegionCorrespondence, 0, len(correspondence.regions))
	for _, region := range correspondence.regions {
		switch region.Relation {
		case CorrespondenceExact, CorrespondenceCoversAll:
			projectable = append(projectable, cloneRegionCorrespondence(region))
		case CorrespondenceGenerated:
			continue
		case CorrespondencePartial, CorrespondenceAmbiguous:
			return nil, fmt.Errorf(
				"cover region %q %s has non-projectable %s correspondence",
				region.Region.ProfilePath,
				formatRange(region.Region.Range),
				region.Relation,
			)
		default:
			return nil, fmt.Errorf("cover region %q has invalid correspondence relation %q", region.Region.ProfilePath, region.Relation)
		}
	}
	return projectable, nil
}

func validateRegionCorrespondence(correspondence RegionCorrespondence) error {
	if correspondence.Region.ProfilePath == "" {
		return fmt.Errorf("profile path is empty")
	}
	if err := validateProfileRange(correspondence.Region.Range); err != nil {
		return fmt.Errorf("invalid cover region: %w", err)
	}
	if correspondence.Region.Statements <= 0 {
		return fmt.Errorf("cover region statement count must be positive: %d", correspondence.Region.Statements)
	}

	minimum, maximum := correspondenceObligationBounds(correspondence.Relation)
	if minimum < 0 {
		return fmt.Errorf("invalid relation %q", correspondence.Relation)
	}
	if len(correspondence.Obligations) < minimum || maximum >= 0 && len(correspondence.Obligations) > maximum {
		return fmt.Errorf(
			"relation %q requires %s, got %d",
			correspondence.Relation,
			formatObligationBounds(minimum, maximum),
			len(correspondence.Obligations),
		)
	}
	if (correspondence.Relation == CorrespondenceExact || correspondence.Relation == CorrespondenceCoversAll) &&
		correspondence.Region.Statements != len(correspondence.Obligations) {
		return fmt.Errorf(
			"projectable relation %q has Go cover statement count %d but %d original obligations",
			correspondence.Relation,
			correspondence.Region.Statements,
			len(correspondence.Obligations),
		)
	}

	seen := make(map[StatementObligation]struct{}, len(correspondence.Obligations))
	for index, obligation := range correspondence.Obligations {
		if obligation.PackagePath == "" {
			return fmt.Errorf("obligation %d package path is empty", index)
		}
		if obligation.OriginalPath == "" {
			return fmt.Errorf("obligation %d original path is empty", index)
		}
		if obligation.BlockIndex < 0 {
			return fmt.Errorf("obligation %d block index is negative: %d", index, obligation.BlockIndex)
		}
		if obligation.StatementIndex < 0 {
			return fmt.Errorf("obligation %d statement index is negative: %d", index, obligation.StatementIndex)
		}
		if _, duplicate := seen[obligation]; duplicate {
			return fmt.Errorf(
				"duplicate statement obligation %q %q block %d statement %d",
				obligation.PackagePath,
				obligation.OriginalPath,
				obligation.BlockIndex,
				obligation.StatementIndex,
			)
		}
		seen[obligation] = struct{}{}
	}
	return nil
}

func correspondenceObligationBounds(relation CorrespondenceRelation) (minimum, maximum int) {
	switch relation {
	case CorrespondenceExact:
		return 1, -1
	case CorrespondenceCoversAll:
		return 1, -1
	case CorrespondencePartial:
		return 1, -1
	case CorrespondenceAmbiguous:
		return 1, -1
	case CorrespondenceGenerated:
		return 0, 0
	default:
		return -1, -1
	}
}

func formatObligationBounds(minimum, maximum int) string {
	switch {
	case minimum == maximum:
		return fmt.Sprintf("exactly %d obligations", minimum)
	case maximum < 0:
		return fmt.Sprintf("at least %d obligations", minimum)
	default:
		return fmt.Sprintf("between %d and %d obligations", minimum, maximum)
	}
}

func cloneRegionCorrespondences(regions []RegionCorrespondence) []RegionCorrespondence {
	cloned := make([]RegionCorrespondence, len(regions))
	for index, region := range regions {
		cloned[index] = cloneRegionCorrespondence(region)
	}
	return cloned
}

func cloneRegionCorrespondence(region RegionCorrespondence) RegionCorrespondence {
	region.Obligations = append([]StatementObligation(nil), region.Obligations...)
	return region
}

func sortRegionCorrespondences(regions []RegionCorrespondence) {
	for index := range regions {
		sort.Slice(regions[index].Obligations, func(left, right int) bool {
			return lessStatementObligation(regions[index].Obligations[left], regions[index].Obligations[right])
		})
	}
	sort.Slice(regions, func(left, right int) bool {
		leftRegion := regions[left].Region
		rightRegion := regions[right].Region
		if leftRegion.ProfilePath != rightRegion.ProfilePath {
			return leftRegion.ProfilePath < rightRegion.ProfilePath
		}
		if lessRange(leftRegion.Range, rightRegion.Range) {
			return true
		}
		if lessRange(rightRegion.Range, leftRegion.Range) {
			return false
		}
		if leftRegion.Statements != rightRegion.Statements {
			return leftRegion.Statements < rightRegion.Statements
		}
		return regions[left].Relation < regions[right].Relation
	})
}

func lessStatementObligation(left, right StatementObligation) bool {
	if left.PackagePath != right.PackagePath {
		return left.PackagePath < right.PackagePath
	}
	if left.OriginalPath != right.OriginalPath {
		return left.OriginalPath < right.OriginalPath
	}
	if left.BlockIndex != right.BlockIndex {
		return left.BlockIndex < right.BlockIndex
	}
	return left.StatementIndex < right.StatementIndex
}
