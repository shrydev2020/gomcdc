package c0

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
)

// CorrespondencePlanInput contains the original obligation inventory and the
// cover-region inventory predicted from rewritten source. GeneratedProfileFiles
// is the exact logical filename set reserved for gomcdc statements.
type CorrespondencePlanInput struct {
	PackagePath           string
	OriginalPath          string
	Original              FileInventory
	Rewritten             FileInventory
	GeneratedProfileFiles []string
}

type inventoryStatementKey struct {
	profileFile string
	position    Position
}

type inventoryStatementLineKey struct {
	profileFile string
	line        int
}

type inventoryRegionKey struct {
	profileFile string
	sourceRange SourceRange
}

type rewrittenStatementRef struct {
	blockIndex     int
	statementIndex int
}

// PlanCoverageCorrespondence proves how every rewritten Go cover block maps
// to statement units from the unchanged original inventory. It never promotes
// a relation from range overlap or basename similarity.
func PlanCoverageCorrespondence(ctx context.Context, input CorrespondencePlanInput) (CoverageCorrespondence, error) {
	if err := ctx.Err(); err != nil {
		return CoverageCorrespondence{}, err
	}
	if input.PackagePath == "" {
		return CoverageCorrespondence{}, fmt.Errorf("plan coverage correspondence: package path is empty")
	}
	originalPath, err := moduleRelativePath(input.OriginalPath)
	if err != nil {
		return CoverageCorrespondence{}, fmt.Errorf("plan coverage correspondence: %w", err)
	}

	generatedFiles := make(map[string]struct{}, len(input.GeneratedProfileFiles))
	for index, filename := range input.GeneratedProfileFiles {
		if err := ctx.Err(); err != nil {
			return CoverageCorrespondence{}, err
		}
		filename = normalizeCorrespondenceProfilePath(filename)
		if filename == "" || filename == "." {
			return CoverageCorrespondence{}, fmt.Errorf("plan coverage correspondence: generated profile file %d is empty", index)
		}
		if _, duplicate := generatedFiles[filename]; duplicate {
			return CoverageCorrespondence{}, fmt.Errorf("plan coverage correspondence: duplicate generated profile file %q", filename)
		}
		generatedFiles[filename] = struct{}{}
	}

	anchorCandidates := make(map[inventoryStatementKey][]StatementObligation)
	lineCandidates := make(map[inventoryStatementLineKey][]StatementObligation)
	exactBlocks := make(map[inventoryRegionKey][]StatementObligation)
	allObligations := make(map[StatementObligation]struct{})
	for blockIndex, block := range input.Original.Blocks {
		if err := ctx.Err(); err != nil {
			return CoverageCorrespondence{}, err
		}
		units, err := validatePlannerInventoryBlock("original", blockIndex, block)
		if err != nil {
			return CoverageCorrespondence{}, err
		}
		obligations := make([]StatementObligation, 0, len(units))
		for statementIndex, unit := range units {
			obligation := StatementObligation{
				PackagePath:    input.PackagePath,
				OriginalPath:   originalPath,
				BlockIndex:     blockIndex,
				StatementIndex: statementIndex,
			}
			for _, profileFile := range correspondenceProfileAliases(originalPath, unit.ProfileFile) {
				key := inventoryStatementKey{profileFile: profileFile, position: unit.ProfilePosition}
				anchorCandidates[key] = append(anchorCandidates[key], obligation)
				lineKey := inventoryStatementLineKey{profileFile: profileFile, line: unit.ProfilePosition.Line}
				lineCandidates[lineKey] = append(lineCandidates[lineKey], obligation)
			}
			allObligations[obligation] = struct{}{}
			obligations = append(obligations, obligation)
		}
		if len(obligations) > 0 {
			for _, profileFile := range correspondenceProfileAliases(originalPath, block.ProfileFile) {
				key := inventoryRegionKey{profileFile: profileFile, sourceRange: block.ProfileRange}
				if _, duplicate := exactBlocks[key]; duplicate {
					return CoverageCorrespondence{}, fmt.Errorf(
						"plan coverage correspondence: original inventory has duplicate region %q %s",
						key.profileFile,
						formatRange(key.sourceRange),
					)
				}
				exactBlocks[key] = obligations
			}
		}
	}
	rewrittenUnits := make([][]InventoryStatement, len(input.Rewritten.Blocks))
	for blockIndex, block := range input.Rewritten.Blocks {
		units, err := validatePlannerInventoryBlock("rewritten", blockIndex, block)
		if err != nil {
			return CoverageCorrespondence{}, err
		}
		rewrittenUnits[blockIndex] = units
	}
	statementCandidates, err := planRewrittenStatementCandidates(
		rewrittenUnits,
		generatedFiles,
		anchorCandidates,
		lineCandidates,
	)
	if err != nil {
		return CoverageCorrespondence{}, err
	}

	regions := make([]RegionCorrespondence, 0, len(input.Rewritten.Blocks))
	mappedObligations := make(map[StatementObligation]struct{}, len(allObligations))
	for blockIndex, block := range input.Rewritten.Blocks {
		if err := ctx.Err(); err != nil {
			return CoverageCorrespondence{}, err
		}
		units := rewrittenUnits[blockIndex]
		if len(units) == 0 {
			continue
		}

		region := RegionCorrespondence{
			Region: CoverRegion{
				ProfilePath: normalizeCorrespondenceProfilePath(block.ProfileFile),
				Range:       block.ProfileRange,
				Statements:  block.Statements,
			},
		}
		generatedCount := 0
		ambiguous := false
		seenRegionObligations := make(map[StatementObligation]struct{})
		for statementIndex, unit := range units {
			profileFile := normalizeCorrespondenceProfilePath(unit.ProfileFile)
			if _, generated := generatedFiles[profileFile]; generated {
				generatedCount++
				continue
			}
			candidates := statementCandidates[rewrittenStatementRef{blockIndex: blockIndex, statementIndex: statementIndex}]
			if len(candidates) == 0 {
				return CoverageCorrespondence{}, fmt.Errorf(
					"plan coverage correspondence: rewritten block %d statement %d at logical %q %s physical %s has no original obligation",
					blockIndex,
					statementIndex,
					profileFile,
					formatPosition(unit.ProfilePosition),
					formatPosition(unit.PhysicalPosition),
				)
			}
			if len(candidates) > 1 {
				ambiguous = true
			}
			for _, candidate := range candidates {
				if _, duplicate := seenRegionObligations[candidate]; duplicate {
					ambiguous = true
					continue
				}
				seenRegionObligations[candidate] = struct{}{}
				mappedObligations[candidate] = struct{}{}
				region.Obligations = append(region.Obligations, candidate)
			}
		}

		switch {
		case generatedCount == len(units):
			region.Relation = CorrespondenceGenerated
		case generatedCount > 0:
			region.Relation = CorrespondencePartial
		case ambiguous:
			region.Relation = CorrespondenceAmbiguous
		default:
			exact := exactBlocks[inventoryRegionKey{profileFile: region.Region.ProfilePath, sourceRange: region.Region.Range}]
			if sameStatementObligations(exact, region.Obligations) {
				region.Relation = CorrespondenceExact
			} else {
				region.Relation = CorrespondenceCoversAll
			}
		}
		regions = append(regions, region)
	}

	for obligation := range allObligations {
		if _, mapped := mappedObligations[obligation]; !mapped {
			return CoverageCorrespondence{}, fmt.Errorf(
				"plan coverage correspondence: original obligation %q %q block %d statement %d has no rewritten cover region",
				obligation.PackagePath,
				obligation.OriginalPath,
				obligation.BlockIndex,
				obligation.StatementIndex,
			)
		}
	}

	downgradeCompetingProjectableRegions(regions)
	correspondence, err := NewCoverageCorrespondence(regions)
	if err != nil {
		return CoverageCorrespondence{}, fmt.Errorf("plan coverage correspondence: %w", err)
	}
	return correspondence, nil
}

// planRewrittenStatementCandidates first uses exact logical positions. When
// printer.SourcePos relocates a statement that is governed by a //line column,
// its logical column can change even though its exact logical file, line, and
// inventory order remain stable. We accept ordinal correspondence only when
// rewritten and still-unclaimed original statements on that exact logical file
// and line have equal cardinality. AST rewriting preserves inventory traversal
// order, so this is a structural proof rather than range or basename inference.
// Cardinality mismatches remain multi-candidate and later fail closed as
// ambiguous or competing regions.
func planRewrittenStatementCandidates(
	rewritten [][]InventoryStatement,
	generatedFiles map[string]struct{},
	anchorCandidates map[inventoryStatementKey][]StatementObligation,
	lineCandidates map[inventoryStatementLineKey][]StatementObligation,
) (map[rewrittenStatementRef][]StatementObligation, error) {
	assignments := make(map[rewrittenStatementRef][]StatementObligation)
	claimed := make(map[StatementObligation]struct{})
	lineGroups := make(map[inventoryStatementLineKey][]rewrittenStatementRef)

	for blockIndex, units := range rewritten {
		for statementIndex, unit := range units {
			profileFile := normalizeCorrespondenceProfilePath(unit.ProfileFile)
			if _, generated := generatedFiles[profileFile]; generated {
				continue
			}
			ref := rewrittenStatementRef{blockIndex: blockIndex, statementIndex: statementIndex}
			exact := anchorCandidates[inventoryStatementKey{profileFile: profileFile, position: unit.ProfilePosition}]
			if len(exact) > 0 {
				assignments[ref] = append([]StatementObligation(nil), exact...)
				if len(exact) == 1 {
					claimed[exact[0]] = struct{}{}
				}
				continue
			}
			lineKey := inventoryStatementLineKey{profileFile: profileFile, line: unit.ProfilePosition.Line}
			lineGroups[lineKey] = append(lineGroups[lineKey], ref)
		}
	}

	lineKeys := make([]inventoryStatementLineKey, 0, len(lineGroups))
	for lineKey := range lineGroups {
		lineKeys = append(lineKeys, lineKey)
	}
	sort.Slice(lineKeys, func(left, right int) bool {
		if lineKeys[left].profileFile != lineKeys[right].profileFile {
			return lineKeys[left].profileFile < lineKeys[right].profileFile
		}
		return lineKeys[left].line < lineKeys[right].line
	})
	for _, lineKey := range lineKeys {
		refs := lineGroups[lineKey]
		candidates := lineCandidates[lineKey]
		available := make([]StatementObligation, 0, len(candidates))
		for _, candidate := range candidates {
			if _, alreadyClaimed := claimed[candidate]; !alreadyClaimed {
				available = append(available, candidate)
			}
		}
		if len(refs) == len(available) && len(refs) > 0 {
			for index, ref := range refs {
				assignments[ref] = []StatementObligation{available[index]}
			}
			continue
		}
		if len(candidates) == 0 {
			ref := refs[0]
			unit := rewritten[ref.blockIndex][ref.statementIndex]
			return nil, fmt.Errorf(
				"plan coverage correspondence: rewritten block %d statement %d at logical %q %s physical %s has no original obligation",
				ref.blockIndex,
				ref.statementIndex,
				lineKey.profileFile,
				formatPosition(unit.ProfilePosition),
				formatPosition(unit.PhysicalPosition),
			)
		}
		for _, ref := range refs {
			assignments[ref] = append([]StatementObligation(nil), candidates...)
		}
	}
	return assignments, nil
}

func validatePlannerInventoryBlock(kind string, index int, block InventoryBlock) ([]InventoryStatement, error) {
	if err := validateOriginalRange(block.PhysicalRange); err != nil {
		return nil, fmt.Errorf("plan coverage correspondence: %s block %d physical range: %w", kind, index, err)
	}
	if block.ProfileFile == "" {
		return nil, fmt.Errorf("plan coverage correspondence: %s block %d profile file is empty", kind, index)
	}
	if err := validateProfileRange(block.ProfileRange); err != nil {
		return nil, fmt.Errorf("plan coverage correspondence: %s block %d profile range: %w", kind, index, err)
	}
	if block.Statements < 0 {
		return nil, fmt.Errorf("plan coverage correspondence: %s block %d has negative statement count %d", kind, index, block.Statements)
	}
	if block.Statements != len(block.StatementUnits) {
		return nil, fmt.Errorf(
			"plan coverage correspondence: %s block %d statement count %d differs from unit count %d",
			kind,
			index,
			block.Statements,
			len(block.StatementUnits),
		)
	}
	if len(block.ProfileAnchors) != len(block.StatementUnits) {
		return nil, fmt.Errorf(
			"plan coverage correspondence: %s block %d anchor count %d differs from unit count %d",
			kind,
			index,
			len(block.ProfileAnchors),
			len(block.StatementUnits),
		)
	}
	for statementIndex, unit := range block.StatementUnits {
		if unit.ProfileFile == "" {
			return nil, fmt.Errorf("plan coverage correspondence: %s block %d statement %d profile file is empty", kind, index, statementIndex)
		}
		if unit.PhysicalPosition.Line <= 0 || unit.PhysicalPosition.Column <= 0 {
			return nil, fmt.Errorf(
				"plan coverage correspondence: %s block %d statement %d physical position must be positive: %s",
				kind,
				index,
				statementIndex,
				formatPosition(unit.PhysicalPosition),
			)
		}
		if unit.ProfilePosition.Line <= 0 || unit.ProfilePosition.Column < 0 {
			return nil, fmt.Errorf(
				"plan coverage correspondence: %s block %d statement %d profile line must be positive and column nonnegative: %s",
				kind,
				index,
				statementIndex,
				formatPosition(unit.ProfilePosition),
			)
		}
		if block.ProfileAnchors[statementIndex] != unit.ProfilePosition {
			return nil, fmt.Errorf(
				"plan coverage correspondence: %s block %d statement %d anchor %s differs from unit position %s",
				kind,
				index,
				statementIndex,
				formatPosition(block.ProfileAnchors[statementIndex]),
				formatPosition(unit.ProfilePosition),
			)
		}
	}
	return block.StatementUnits, nil
}

func downgradeCompetingProjectableRegions(regions []RegionCorrespondence) {
	owners := make(map[StatementObligation][]int)
	for index, region := range regions {
		if region.Relation != CorrespondenceExact && region.Relation != CorrespondenceCoversAll {
			continue
		}
		for _, obligation := range region.Obligations {
			owners[obligation] = append(owners[obligation], index)
		}
	}
	for _, indexes := range owners {
		if len(indexes) < 2 {
			continue
		}
		for _, index := range indexes {
			regions[index].Relation = CorrespondencePartial
		}
	}
}

func sameStatementObligations(left, right []StatementObligation) bool {
	if len(left) == 0 || len(left) != len(right) {
		return false
	}
	left = append([]StatementObligation(nil), left...)
	right = append([]StatementObligation(nil), right...)
	sort.Slice(left, func(i, j int) bool { return lessStatementObligation(left[i], left[j]) })
	sort.Slice(right, func(i, j int) bool { return lessStatementObligation(right[i], right[j]) })
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func normalizeCorrespondenceProfilePath(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	if value == "" {
		return ""
	}
	return path.Clean(value)
}

func correspondenceProfileAliases(originalPath, profileFile string) []string {
	profileFile = normalizeCorrespondenceProfilePath(profileFile)
	aliases := []string{profileFile}
	if profileFile == "" || path.IsAbs(profileFile) {
		return aliases
	}
	resolved := normalizeCorrespondenceProfilePath(path.Join(path.Dir(originalPath), profileFile))
	if resolved != profileFile {
		aliases = append(aliases, resolved)
	}
	return aliases
}
