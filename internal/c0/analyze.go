package c0

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"path"
	"sort"
	"strings"
)

type originalFileKey struct {
	packagePath  string
	originalPath string
}

type originalFileAccumulator struct {
	key       originalFileKey
	source    []byte
	inventory *FileInventory
	blocks    []mappedProfileBlock
}

type mappedProfileBlock struct {
	profilePath  string
	profileRange SourceRange
	original     ProfileBlock
}

type analyzedProfileBlock struct {
	profile  ProfileBlock
	evidence bool
}

type preparedFileMapping struct {
	generated bool
	original  *originalFileAccumulator
	blocks    map[SourceRange]BlockMapping
}

type analyzedFile struct {
	packagePath string
	report      FileReport
}

// Analyze maps and aggregates C0 evidence while ctx permits recovery work.
func Analyze(ctx context.Context, profile Profile, sourceMap SourceMap, options Options) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	if !profile.Mode.valid() {
		return Report{}, fmt.Errorf("analyze C0 profile: unsupported mode %q", profile.Mode)
	}
	if sourceMap.ModulePath == "" {
		return Report{}, errors.New("analyze C0 profile: source map module path is empty")
	}

	mappings, originals, err := prepareSourceMap(ctx, sourceMap)
	if err != nil {
		return Report{}, err
	}
	excluded := make([]ExcludedBlock, 0)
	seenProfilePaths := make(map[string]struct{})
	profileFiles := append([]ProfileFile(nil), profile.Files...)
	sort.Slice(profileFiles, func(i, j int) bool { return profileFiles[i].Path < profileFiles[j].Path })
	for _, profileFile := range profileFiles {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		if profileFile.Path == "" {
			return Report{}, errors.New("analyze C0 profile: profile file path is empty")
		}
		if _, duplicate := seenProfilePaths[profileFile.Path]; duplicate {
			return Report{}, fmt.Errorf("analyze C0 profile: duplicate profile file %q", profileFile.Path)
		}
		seenProfilePaths[profileFile.Path] = struct{}{}
		mapping, mapped := mappings[profileFile.Path]
		blocks := append([]ProfileBlock(nil), profileFile.Blocks...)
		sort.Slice(blocks, func(i, j int) bool { return lessRange(blocks[i].Position, blocks[j].Position) })
		for _, block := range blocks {
			if err := ctx.Err(); err != nil {
				return Report{}, err
			}
			if err := validateProfileBlock(block); err != nil {
				return Report{}, fmt.Errorf("analyze C0 profile %q: %w", profileFile.Path, err)
			}
			if !mapped {
				excluded = append(excluded, excludedProfileBlock(profileFile.Path, block, "", nil, ExcludeUnmappedFile))
				continue
			}
			if mapping.generated {
				excluded = append(excluded, excludedProfileBlock(profileFile.Path, block, "", nil, ExcludeGeneratedFile))
				continue
			}

			originalRange := block.Position
			if override, found := mapping.blocks[block.Position]; found {
				if override.Generated {
					excluded = append(excluded, excludedProfileBlock(profileFile.Path, block, mapping.original.key.originalPath, nil, ExcludeGeneratedBlock))
					continue
				}
				originalRange = override.OriginalRange
			}
			mapping.original.blocks = append(mapping.original.blocks, mappedProfileBlock{
				profilePath:  profileFile.Path,
				profileRange: block.Position,
				original:     ProfileBlock{Position: originalRange, Statements: block.Statements, Count: block.Count},
			})
		}
	}

	files := make([]analyzedFile, 0, len(originals))
	for _, original := range originals {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		fileReport, fileExcluded, include, err := analyzeOriginalFile(ctx, original, profile.Mode, options)
		if err != nil {
			return Report{}, err
		}
		excluded = append(excluded, fileExcluded...)
		if include {
			files = append(files, analyzedFile{packagePath: original.key.packagePath, report: fileReport})
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].packagePath != files[j].packagePath {
			return files[i].packagePath < files[j].packagePath
		}
		return files[i].report.Path < files[j].report.Path
	})
	sortExcluded(excluded)

	report := Report{
		Mode:       profile.Mode,
		ModulePath: sourceMap.ModulePath,
		Packages:   make([]PackageReport, 0),
		Excluded:   excluded,
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		packageReport := ensureReportPackage(&report, file.packagePath)
		packageReport.Files = append(packageReport.Files, file.report)
		packageReport.Evidence = packageReport.Evidence || file.report.Evidence
		if err := addSummary(&packageReport.Summary, file.report.Summary); err != nil {
			return Report{}, fmt.Errorf("aggregate package %q: %w", file.packagePath, err)
		}
		if err := addSummary(&report.Summary, file.report.Summary); err != nil {
			return Report{}, fmt.Errorf("aggregate module: %w", err)
		}
	}
	return report, nil
}

func prepareSourceMap(ctx context.Context, sourceMap SourceMap) (map[string]preparedFileMapping, map[originalFileKey]*originalFileAccumulator, error) {
	mappings := make(map[string]preparedFileMapping, len(sourceMap.Files))
	originals := make(map[originalFileKey]*originalFileAccumulator)
	for index, file := range sourceMap.Files {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if file.ProfilePath == "" {
			return nil, nil, fmt.Errorf("prepare C0 source map file %d: profile path is empty", index)
		}
		if _, duplicate := mappings[file.ProfilePath]; duplicate {
			return nil, nil, fmt.Errorf("prepare C0 source map: duplicate profile path %q", file.ProfilePath)
		}
		generated := file.Generated
		prepared := preparedFileMapping{
			generated: generated,
			blocks:    make(map[SourceRange]BlockMapping, len(file.Blocks)),
		}
		if generated {
			mappings[file.ProfilePath] = prepared
			continue
		}
		if file.PackagePath == "" {
			return nil, nil, fmt.Errorf("prepare C0 source map %q: package path is empty", file.ProfilePath)
		}
		originalPath, err := moduleRelativePath(file.OriginalPath)
		if err != nil {
			return nil, nil, fmt.Errorf("prepare C0 source map %q: %w", file.ProfilePath, err)
		}
		key := originalFileKey{packagePath: file.PackagePath, originalPath: originalPath}
		inventory, err := cloneAndValidateInventory(ctx, file.Inventory)
		if err != nil {
			return nil, nil, fmt.Errorf("prepare C0 source map %q inventory: %w", file.ProfilePath, err)
		}
		original := originals[key]
		if original == nil {
			original = &originalFileAccumulator{
				key:       key,
				source:    append([]byte(nil), file.OriginalSource...),
				inventory: inventory,
				blocks:    make([]mappedProfileBlock, 0),
			}
			originals[key] = original
		} else if !bytes.Equal(original.source, file.OriginalSource) {
			return nil, nil, fmt.Errorf("prepare C0 source map %q: source differs for original file %q", file.ProfilePath, originalPath)
		} else if !equalInventory(original.inventory, inventory) {
			return nil, nil, fmt.Errorf("prepare C0 source map %q: inventory differs for original file %q", file.ProfilePath, originalPath)
		}
		prepared.original = original
		for blockIndex, block := range file.Blocks {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			if err := validateProfileRange(block.ProfileRange); err != nil {
				return nil, nil, fmt.Errorf("prepare C0 source map %q block %d profile range: %w", file.ProfilePath, blockIndex, err)
			}
			if _, duplicate := prepared.blocks[block.ProfileRange]; duplicate {
				return nil, nil, fmt.Errorf("prepare C0 source map %q: duplicate block mapping for %s", file.ProfilePath, formatRange(block.ProfileRange))
			}
			if !block.Generated {
				if err := validateOriginalRange(block.OriginalRange); err != nil {
					return nil, nil, fmt.Errorf("prepare C0 source map %q block %d original range: %w", file.ProfilePath, blockIndex, err)
				}
			}
			prepared.blocks[block.ProfileRange] = block
		}
		mappings[file.ProfilePath] = prepared
	}
	return mappings, originals, nil
}

func cloneAndValidateInventory(ctx context.Context, inventory *FileInventory) (*FileInventory, error) {
	if inventory == nil {
		return nil, nil
	}
	cloned := &FileInventory{Blocks: make([]InventoryBlock, len(inventory.Blocks))}
	for index, block := range inventory.Blocks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := validateOriginalRange(block.PhysicalRange); err != nil {
			return nil, fmt.Errorf("block %d physical range: %w", index, err)
		}
		if err := validateProfileRange(block.ProfileRange); err != nil {
			return nil, fmt.Errorf("block %d profile range: %w", index, err)
		}
		if block.Statements < 0 {
			return nil, fmt.Errorf("block %d has negative NumStmt %d", index, block.Statements)
		}
		cloned.Blocks[index] = block
		cloned.Blocks[index].ProfileAnchors = append([]Position(nil), block.ProfileAnchors...)
		cloned.Blocks[index].StatementUnits = append([]InventoryStatement(nil), block.StatementUnits...)
		for anchorIndex, anchor := range block.ProfileAnchors {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if anchor.Line < 0 || anchor.Column < 0 {
				return nil, fmt.Errorf("block %d anchor %d must be nonnegative: %s", index, anchorIndex, formatPosition(anchor))
			}
		}
	}
	return cloned, nil
}

func equalInventory(left, right *FileInventory) bool {
	if left == nil || right == nil {
		return left == right
	}
	if len(left.Blocks) != len(right.Blocks) {
		return false
	}
	for index := range left.Blocks {
		leftBlock := left.Blocks[index]
		rightBlock := right.Blocks[index]
		if leftBlock.PhysicalRange != rightBlock.PhysicalRange ||
			leftBlock.ProfileFile != rightBlock.ProfileFile ||
			leftBlock.ProfileRange != rightBlock.ProfileRange ||
			leftBlock.Statements != rightBlock.Statements ||
			len(leftBlock.ProfileAnchors) != len(rightBlock.ProfileAnchors) ||
			len(leftBlock.StatementUnits) != len(rightBlock.StatementUnits) {
			return false
		}
		for anchorIndex := range leftBlock.ProfileAnchors {
			if leftBlock.ProfileAnchors[anchorIndex] != rightBlock.ProfileAnchors[anchorIndex] {
				return false
			}
		}
		for statementIndex := range leftBlock.StatementUnits {
			if leftBlock.StatementUnits[statementIndex] != rightBlock.StatementUnits[statementIndex] {
				return false
			}
		}
	}
	return true
}

func analyzeOriginalFile(ctx context.Context, original *originalFileAccumulator, mode Mode, options Options) (FileReport, []ExcludedBlock, bool, error) {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, original.key.originalPath, original.source, parser.ParseComments|parser.AllErrors)
	if err != nil {
		return FileReport{}, nil, false, fmt.Errorf("analyze C0 source %q: parse: %w", original.key.originalPath, err)
	}
	if err := ctx.Err(); err != nil {
		return FileReport{}, nil, false, err
	}
	mappedBlocks := sortedMappedBlocks(original.blocks)
	functions := discoverFunctions(fset, parsed)
	if original.inventory != nil {
		return analyzeOriginalInventory(ctx, original, mappedBlocks, functions, options)
	}
	excluded := make([]ExcludedBlock, 0)
	accepted := make(map[SourceRange]ProfileBlock)
	for _, block := range mappedBlocks {
		if err := ctx.Err(); err != nil {
			return FileReport{}, nil, false, err
		}
		owner := ownerForBlock(functions, block.original.Position)
		if owner < 0 || block.original.Statements == 0 {
			originalRange := block.original.Position
			excluded = append(excluded, excludedProfileBlock(block.profilePath, profileBlock(block), original.key.originalPath, &originalRange, ExcludeNoOriginalStatement))
			continue
		}
		if existing, found := accepted[block.original.Position]; found {
			if existing.Statements != block.original.Statements {
				return FileReport{}, nil, false, fmt.Errorf(
					"analyze C0 source %q: inconsistent mapped NumStmt for %s: %d then %d",
					original.key.originalPath,
					formatRange(block.original.Position),
					existing.Statements,
					block.original.Statements,
				)
			}
			count, err := mergeCount(mode, existing.Count, block.original.Count)
			if err != nil {
				return FileReport{}, nil, false, fmt.Errorf("analyze C0 source %q: %w", original.key.originalPath, err)
			}
			existing.Count = count
			accepted[block.original.Position] = existing
			continue
		}
		accepted[block.original.Position] = block.original
	}

	blocks := blocksWithEvidence(sortedBlocks(accepted), true)
	fileReport, include, err := buildFileReport(ctx, original, functions, blocks, options)
	return fileReport, excluded, include, err
}

func analyzeOriginalInventory(
	ctx context.Context,
	original *originalFileAccumulator,
	mappedBlocks []mappedProfileBlock,
	functions []functionExtent,
	options Options,
) (FileReport, []ExcludedBlock, bool, error) {
	inventoryBlocks := append([]InventoryBlock(nil), original.inventory.Blocks...)
	sort.SliceStable(inventoryBlocks, func(i, j int) bool {
		if inventoryBlocks[i].ProfileFile != inventoryBlocks[j].ProfileFile {
			return inventoryBlocks[i].ProfileFile < inventoryBlocks[j].ProfileFile
		}
		if lessRange(inventoryBlocks[i].ProfileRange, inventoryBlocks[j].ProfileRange) {
			return true
		}
		if lessRange(inventoryBlocks[j].ProfileRange, inventoryBlocks[i].ProfileRange) {
			return false
		}
		return lessRange(inventoryBlocks[i].PhysicalRange, inventoryBlocks[j].PhysicalRange)
	})

	used := make([]bool, len(mappedBlocks))
	positiveUsed := make([]bool, len(mappedBlocks))
	accepted := make([]analyzedProfileBlock, 0, len(inventoryBlocks))
	for _, inventoryBlock := range inventoryBlocks {
		if err := ctx.Err(); err != nil {
			return FileReport{}, nil, false, err
		}
		candidateIndex, err := selectInventoryCandidate(ctx, inventoryBlock, mappedBlocks, used)
		if err != nil {
			return FileReport{}, nil, false, err
		}
		count := uint64(0)
		if candidateIndex >= 0 {
			used[candidateIndex] = true
			count = mappedBlocks[candidateIndex].original.Count
		}
		if inventoryBlock.Statements == 0 {
			continue
		}
		if candidateIndex >= 0 {
			positiveUsed[candidateIndex] = true
		}
		if ownerForBlock(functions, inventoryBlock.PhysicalRange) < 0 {
			return FileReport{}, nil, false, fmt.Errorf(
				"analyze C0 source %q: inventory block %s has no function owner",
				original.key.originalPath,
				formatRange(inventoryBlock.PhysicalRange),
			)
		}
		accepted = append(accepted, analyzedProfileBlock{
			profile: ProfileBlock{
				Position:   inventoryBlock.PhysicalRange,
				Statements: inventoryBlock.Statements,
				Count:      count,
			},
			evidence: candidateIndex >= 0,
		})
	}
	sort.Slice(accepted, func(i, j int) bool { return lessRange(accepted[i].profile.Position, accepted[j].profile.Position) })

	excluded := make([]ExcludedBlock, 0)
	for index, block := range mappedBlocks {
		if err := ctx.Err(); err != nil {
			return FileReport{}, nil, false, err
		}
		if positiveUsed[index] {
			continue
		}
		originalRange := block.original.Position
		excluded = append(excluded, excludedProfileBlock(
			block.profilePath,
			profileBlock(block),
			original.key.originalPath,
			&originalRange,
			ExcludeNoOriginalStatement,
		))
	}
	fileReport, include, err := buildFileReport(ctx, original, functions, accepted, options)
	return fileReport, excluded, include, err
}

func selectInventoryCandidate(ctx context.Context, inventory InventoryBlock, candidates []mappedProfileBlock, used []bool) (int, error) {
	candidate, err := bestInventoryCandidate(ctx, inventory, candidates, used, true)
	if err != nil {
		return -1, err
	}
	if candidate >= 0 {
		return candidate, nil
	}
	return bestInventoryCandidate(ctx, inventory, candidates, used, false)
}

func bestInventoryCandidate(ctx context.Context, inventory InventoryBlock, candidates []mappedProfileBlock, used []bool, requireUnused bool) (int, error) {
	bestIndex := -1
	bestScore := 0
	for index, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return -1, err
		}
		if requireUnused && used[index] {
			continue
		}
		if !profileFileCompatible(candidate.profilePath, inventory.ProfileFile) {
			continue
		}
		score := inventoryCandidateScore(inventory, candidate)
		if score > bestScore {
			bestIndex = index
			bestScore = score
		}
	}
	return bestIndex, nil
}

func inventoryCandidateScore(inventory InventoryBlock, candidate mappedProfileBlock) int {
	if candidate.profileRange == inventory.ProfileRange {
		return 10_000
	}
	if candidate.original.Position == inventory.PhysicalRange {
		return 9_500
	}
	if sameLineRange(candidate.profileRange, inventory.ProfileRange) {
		return 8_000
	}
	if sameLineRange(candidate.original.Position, inventory.PhysicalRange) {
		return 7_500
	}
	for index, anchor := range inventory.ProfileAnchors {
		if rangeContainsPosition(candidate.profileRange, anchor) {
			return 6_000 - index
		}
	}
	if rangeContainsPosition(candidate.profileRange, inventory.ProfileRange.Start) {
		return 5_000
	}
	if overlap := lineOverlap(candidate.profileRange, inventory.ProfileRange); overlap > 0 {
		if overlap > 999 {
			overlap = 999
		}
		return 1_000 + overlap
	}
	return 0
}

func profileFileCompatible(profilePath, inventoryFile string) bool {
	if inventoryFile == "" {
		return true
	}
	profilePath = strings.ReplaceAll(profilePath, "\\", "/")
	inventoryFile = strings.ReplaceAll(inventoryFile, "\\", "/")
	if profilePath == inventoryFile || strings.HasSuffix(profilePath, "/"+strings.TrimPrefix(inventoryFile, "/")) {
		return true
	}
	return path.Base(profilePath) == path.Base(inventoryFile)
}

func sameLineRange(left, right SourceRange) bool {
	return left.Start.Line == right.Start.Line && left.End.Line == right.End.Line
}

func rangeContainsPosition(sourceRange SourceRange, position Position) bool {
	if position.Line < sourceRange.Start.Line || position.Line > sourceRange.End.Line {
		return false
	}
	if position.Line == sourceRange.Start.Line && sourceRange.Start.Column > 0 && position.Column > 0 && position.Column < sourceRange.Start.Column {
		return false
	}
	if position.Line == sourceRange.End.Line && sourceRange.End.Column > 0 && position.Column > 0 && position.Column >= sourceRange.End.Column {
		return false
	}
	return true
}

func lineOverlap(left, right SourceRange) int {
	start := left.Start.Line
	if right.Start.Line > start {
		start = right.Start.Line
	}
	end := left.End.Line
	if right.End.Line < end {
		end = right.End.Line
	}
	if end < start {
		return 0
	}
	return end - start + 1
}

func buildFileReport(
	ctx context.Context,
	original *originalFileAccumulator,
	functions []functionExtent,
	blocks []analyzedProfileBlock,
	options Options,
) (FileReport, bool, error) {
	owned := make([][]StatementBlock, len(functions))
	fileReport := FileReport{
		Path:      original.key.originalPath,
		Functions: make([]FunctionReport, 0),
	}
	for _, block := range blocks {
		if err := ctx.Err(); err != nil {
			return FileReport{}, false, err
		}
		owner := ownerForBlock(functions, block.profile.Position)
		statementBlock := newStatementBlock(block.profile, block.evidence)
		owned[owner] = append(owned[owner], statementBlock)
		fileReport.Evidence = fileReport.Evidence || statementBlock.Evidence
		if err := addSummary(&fileReport.Summary, statementBlock.Summary); err != nil {
			return FileReport{}, false, fmt.Errorf("analyze C0 source %q: aggregate block: %w", original.key.originalPath, err)
		}
	}

	for index, function := range functions {
		if err := ctx.Err(); err != nil {
			return FileReport{}, false, err
		}
		blocks := owned[index]
		if len(blocks) == 0 && !options.IncludeEmptyFunctions {
			continue
		}
		if blocks == nil {
			blocks = make([]StatementBlock, 0)
		}
		functionReport := FunctionReport{
			Name:             function.name,
			Position:         function.position,
			CompleteEvidence: true,
			Blocks:           blocks,
		}
		for _, block := range blocks {
			if err := ctx.Err(); err != nil {
				return FileReport{}, false, err
			}
			functionReport.Evidence = functionReport.Evidence || block.Evidence
			functionReport.CompleteEvidence = functionReport.CompleteEvidence && block.Evidence
			if err := addSummary(&functionReport.Summary, block.Summary); err != nil {
				return FileReport{}, false, fmt.Errorf("analyze C0 function %q: aggregate block: %w", function.name, err)
			}
		}
		functionReport.Summary.Functions.Total = 1
		if functionReport.Summary.Statements.Covered > 0 {
			functionReport.Summary.Functions.Covered = 1
		}
		fileReport.Functions = append(fileReport.Functions, functionReport)
		if err := addCounts(&fileReport.Summary.Functions, functionReport.Summary.Functions); err != nil {
			return FileReport{}, false, fmt.Errorf("analyze C0 source %q: aggregate functions: %w", original.key.originalPath, err)
		}
	}
	include := fileReport.Summary.Blocks.Total > 0 || len(fileReport.Functions) > 0
	return fileReport, include, nil
}

func blocksWithEvidence(blocks []ProfileBlock, evidence bool) []analyzedProfileBlock {
	result := make([]analyzedProfileBlock, len(blocks))
	for index, block := range blocks {
		result[index] = analyzedProfileBlock{profile: block, evidence: evidence}
	}
	return result
}

func sortedBlocks(blocks map[SourceRange]ProfileBlock) []ProfileBlock {
	result := make([]ProfileBlock, 0, len(blocks))
	for _, block := range blocks {
		result = append(result, block)
	}
	sort.Slice(result, func(i, j int) bool { return lessRange(result[i].Position, result[j].Position) })
	return result
}

func sortedMappedBlocks(blocks []mappedProfileBlock) []mappedProfileBlock {
	result := append([]mappedProfileBlock(nil), blocks...)
	sort.Slice(result, func(i, j int) bool {
		if lessRange(result[i].original.Position, result[j].original.Position) {
			return true
		}
		if lessRange(result[j].original.Position, result[i].original.Position) {
			return false
		}
		if result[i].profilePath != result[j].profilePath {
			return result[i].profilePath < result[j].profilePath
		}
		return lessRange(result[i].profileRange, result[j].profileRange)
	})
	return result
}

func profileBlock(block mappedProfileBlock) ProfileBlock {
	return ProfileBlock{Position: block.profileRange, Statements: block.original.Statements, Count: block.original.Count}
}

func newStatementBlock(block ProfileBlock, evidence bool) StatementBlock {
	covered := block.Count > 0
	result := StatementBlock{
		Position:   block.Position,
		Statements: block.Statements,
		Count:      block.Count,
		Evidence:   evidence,
		Summary: Summary{
			Statements: Counts{Total: block.Statements},
			Blocks:     Counts{Total: 1},
		},
	}
	if covered {
		result.Summary.Statements.Covered = block.Statements
		result.Summary.Blocks.Covered = 1
	}
	return result
}

func ensureReportPackage(report *Report, packagePath string) *PackageReport {
	if len(report.Packages) == 0 || report.Packages[len(report.Packages)-1].Path != packagePath {
		report.Packages = append(report.Packages, PackageReport{
			Path:  packagePath,
			Files: make([]FileReport, 0),
		})
	}
	return &report.Packages[len(report.Packages)-1]
}

func addSummary(destination *Summary, source Summary) error {
	if err := addCounts(&destination.Statements, source.Statements); err != nil {
		return fmt.Errorf("statements: %w", err)
	}
	if err := addCounts(&destination.Blocks, source.Blocks); err != nil {
		return fmt.Errorf("blocks: %w", err)
	}
	if err := addCounts(&destination.Functions, source.Functions); err != nil {
		return fmt.Errorf("functions: %w", err)
	}
	return nil
}

func addCounts(destination *Counts, source Counts) error {
	if source.Covered < 0 || source.Total < 0 || destination.Covered < 0 || destination.Total < 0 {
		return errors.New("negative coverage count")
	}
	if maxInt()-destination.Covered < source.Covered || maxInt()-destination.Total < source.Total {
		return errors.New("coverage count overflow")
	}
	destination.Covered += source.Covered
	destination.Total += source.Total
	return nil
}

func validateProfileBlock(block ProfileBlock) error {
	if err := validateProfileRange(block.Position); err != nil {
		return fmt.Errorf("invalid block range: %w", err)
	}
	if block.Statements < 0 {
		return fmt.Errorf("negative NumStmt %d", block.Statements)
	}
	return nil
}

func validateProfileRange(sourceRange SourceRange) error {
	if sourceRange.Start.Line < 0 || sourceRange.Start.Column < 0 || sourceRange.End.Line < 0 || sourceRange.End.Column < 0 {
		return fmt.Errorf("range positions must be nonnegative: %s", formatRange(sourceRange))
	}
	if comparePosition(sourceRange.End, sourceRange.Start) < 0 {
		return fmt.Errorf("range end precedes start: %s", formatRange(sourceRange))
	}
	return nil
}

func validateOriginalRange(sourceRange SourceRange) error {
	if sourceRange.Start.Line <= 0 || sourceRange.Start.Column <= 0 || sourceRange.End.Line <= 0 || sourceRange.End.Column <= 0 {
		return fmt.Errorf("original range positions must be positive: %s", formatRange(sourceRange))
	}
	if comparePosition(sourceRange.End, sourceRange.Start) < 0 {
		return fmt.Errorf("range end precedes start: %s", formatRange(sourceRange))
	}
	return nil
}

func moduleRelativePath(value string) (string, error) {
	if value == "" {
		return "", errors.New("original path is empty")
	}
	value = strings.ReplaceAll(value, "\\", "/")
	cleaned := path.Clean(value)
	if cleaned == "." || path.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("original path %q is not module-relative", value)
	}
	return cleaned, nil
}

func excludedProfileBlock(profilePath string, block ProfileBlock, originalPath string, originalRange *SourceRange, reason ExclusionReason) ExcludedBlock {
	return ExcludedBlock{
		ProfilePath:   profilePath,
		ProfileRange:  block.Position,
		OriginalPath:  originalPath,
		OriginalRange: originalRange,
		Statements:    block.Statements,
		Count:         block.Count,
		Reason:        reason,
	}
}

func sortExcluded(excluded []ExcludedBlock) {
	sort.Slice(excluded, func(i, j int) bool {
		if excluded[i].ProfilePath != excluded[j].ProfilePath {
			return excluded[i].ProfilePath < excluded[j].ProfilePath
		}
		if excluded[i].OriginalPath != excluded[j].OriginalPath {
			return excluded[i].OriginalPath < excluded[j].OriginalPath
		}
		if lessRange(excluded[i].ProfileRange, excluded[j].ProfileRange) {
			return true
		}
		if lessRange(excluded[j].ProfileRange, excluded[i].ProfileRange) {
			return false
		}
		return excluded[i].Reason < excluded[j].Reason
	})
}
