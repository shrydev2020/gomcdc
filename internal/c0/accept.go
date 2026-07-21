package c0

import (
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"path"
	"sort"
	"strings"
)

// SourceCoveragePlan binds one immutable original-source inventory to the
// correspondence planned before source rewriting. Original Inventory is the
// only obligation authority; Profile and Correspondence contribute evidence.
type SourceCoveragePlan struct {
	PackagePath    string
	OriginalPath   string
	OriginalSource []byte
	Inventory      FileInventory
	Correspondence CoverageCorrespondence
}

// ProfileAcceptanceOptions names known non-obligation files and states whether
// a successful test run requires every planned cover region to be present.
type ProfileAcceptanceOptions struct {
	ModulePath           string
	RunComplete          bool
	GeneratedProfilePath []string
	IgnoredProfilePath   []string
}

// AcceptedCoverEvidence contains only cover counters whose producer identity,
// NumStmt, correspondence, and original obligation ownership were accepted.
// It is deliberately separate from the projected C0 report.
type AcceptedCoverEvidence struct {
	Mode     Mode
	Sources  []AcceptedSourceEvidence
	Excluded []ExcludedBlock
}

// AcceptedSourceEvidence retains the original authority and accepted evidence
// for one source. Missing block evidence is represented by Observed=false.
type AcceptedSourceEvidence struct {
	PackagePath    string
	OriginalPath   string
	OriginalSource []byte
	Inventory      FileInventory
	Blocks         []AcceptedBlockEvidence
}

// AcceptedBlockEvidence projects rewritten cover regions back to one original
// Go cover block. Count comes from the region owning statement index zero,
// which is the original block-entry counter location.
type AcceptedBlockEvidence struct {
	BlockIndex int
	Count      uint64
	Observed   bool
}

type plannedRegionRef struct {
	planIndex   int
	regionIndex int
	region      RegionCorrespondence
	observed    bool
	count       uint64
}

type coverRegionShape struct {
	sourceRange SourceRange
	statements  int
}

// AcceptProfileEvidence validates raw Go cover output against correspondence
// planned from unchanged source. Unknown, ambiguous, partial, duplicate, or
// producer-incompatible regions fail closed.
func AcceptProfileEvidence(
	ctx context.Context,
	profile Profile,
	plans []SourceCoveragePlan,
	options ProfileAcceptanceOptions,
) (AcceptedCoverEvidence, error) {
	if err := ctx.Err(); err != nil {
		return AcceptedCoverEvidence{}, err
	}
	if !profile.Mode.valid() {
		return AcceptedCoverEvidence{}, fmt.Errorf("accept cover evidence: unsupported mode %q", profile.Mode)
	}
	if options.ModulePath == "" {
		return AcceptedCoverEvidence{}, errors.New("accept cover evidence: module path is empty")
	}

	preparedPlans, err := prepareCoveragePlans(ctx, plans)
	if err != nil {
		return AcceptedCoverEvidence{}, err
	}
	generated, err := prepareKnownProfilePaths(options.GeneratedProfilePath)
	if err != nil {
		return AcceptedCoverEvidence{}, fmt.Errorf("accept cover evidence: generated paths: %w", err)
	}
	ignored, err := prepareKnownProfilePaths(options.IgnoredProfilePath)
	if err != nil {
		return AcceptedCoverEvidence{}, fmt.Errorf("accept cover evidence: ignored paths: %w", err)
	}

	var regions []plannedRegionRef
	byShape := make(map[coverRegionShape][]int)
	for planIndex, plan := range preparedPlans {
		if _, projectErr := plan.Correspondence.ProjectableRegions(); projectErr != nil {
			return AcceptedCoverEvidence{}, fmt.Errorf("accept cover evidence %q: %w", plan.OriginalPath, projectErr)
		}
		for regionIndex, region := range plan.Correspondence.Regions() {
			index := len(regions)
			regions = append(regions, plannedRegionRef{planIndex: planIndex, regionIndex: regionIndex, region: region})
			shape := coverRegionShape{sourceRange: region.Region.Range, statements: region.Region.Statements}
			byShape[shape] = append(byShape[shape], index)
		}
	}

	excluded := make([]ExcludedBlock, 0)
	for _, file := range profile.Files {
		if err := ctx.Err(); err != nil {
			return AcceptedCoverEvidence{}, err
		}
		actualPath := normalizeAcceptancePath(file.Path)
		if actualPath == "" || actualPath == "." {
			return AcceptedCoverEvidence{}, errors.New("accept cover evidence: profile file path is empty")
		}
		for _, block := range file.Blocks {
			if err := validateProfileBlock(block); err != nil {
				return AcceptedCoverEvidence{}, fmt.Errorf("accept cover evidence %q: %w", file.Path, err)
			}
			if block.Statements == 0 {
				originalPath := ""
				for _, plan := range preparedPlans {
					if !acceptancePathMatches(actualPath, plan.OriginalPath, options.ModulePath, plan.PackagePath) {
						continue
					}
					if originalPath != "" && originalPath != plan.OriginalPath {
						return AcceptedCoverEvidence{}, fmt.Errorf(
							"accept cover evidence: zero-statement profile region %q %s matches multiple original sources",
							file.Path,
							formatRange(block.Position),
						)
					}
					originalPath = plan.OriginalPath
				}
				reason := ExclusionReason("")
				switch {
				case originalPath != "":
					reason = ExcludeNoOriginalStatement
				case matchesKnownProfilePath(actualPath, generated, options.ModulePath):
					reason = ExcludeGeneratedFile
				case matchesKnownProfilePath(actualPath, ignored, options.ModulePath):
					reason = ExcludeReportScope
				default:
					return AcceptedCoverEvidence{}, fmt.Errorf(
						"accept cover evidence: unknown zero-statement profile region %q %s",
						file.Path,
						formatRange(block.Position),
					)
				}
				excluded = append(excluded, excludedProfileBlock(file.Path, block, originalPath, nil, reason))
				continue
			}
			shape := coverRegionShape{sourceRange: block.Position, statements: block.Statements}
			matches := make([]int, 0, 1)
			for _, candidateIndex := range byShape[shape] {
				candidate := regions[candidateIndex]
				plan := preparedPlans[candidate.planIndex]
				if acceptancePathMatches(actualPath, candidate.region.Region.ProfilePath, options.ModulePath, plan.PackagePath) {
					matches = append(matches, candidateIndex)
				}
			}
			switch len(matches) {
			case 1:
				matched := &regions[matches[0]]
				if matched.observed {
					return AcceptedCoverEvidence{}, fmt.Errorf(
						"accept cover evidence: duplicate profile region %q %s",
						file.Path,
						formatRange(block.Position),
					)
				}
				matched.observed = true
				matched.count = block.Count
				if matched.region.Relation == CorrespondenceGenerated {
					excluded = append(excluded, excludedProfileBlock(file.Path, block, "", nil, ExcludeGeneratedBlock))
				}
			case 0:
				reason := ExclusionReason("")
				if matchesKnownProfilePath(actualPath, generated, options.ModulePath) {
					reason = ExcludeGeneratedFile
				} else if matchesKnownProfilePath(actualPath, ignored, options.ModulePath) {
					reason = ExcludeReportScope
				}
				if reason == "" {
					return AcceptedCoverEvidence{}, fmt.Errorf(
						"accept cover evidence: unknown profile region %q %s NumStmt=%d",
						file.Path,
						formatRange(block.Position),
						block.Statements,
					)
				}
				excluded = append(excluded, excludedProfileBlock(file.Path, block, "", nil, reason))
			default:
				return AcceptedCoverEvidence{}, fmt.Errorf(
					"accept cover evidence: profile region %q %s NumStmt=%d matches %d planned regions",
					file.Path,
					formatRange(block.Position),
					block.Statements,
					len(matches),
				)
			}
		}
	}

	if options.RunComplete {
		for _, planned := range regions {
			if !planned.observed {
				return AcceptedCoverEvidence{}, fmt.Errorf(
					"accept cover evidence: successful run omitted planned region %q %s NumStmt=%d",
					planned.region.Region.ProfilePath,
					formatRange(planned.region.Region.Range),
					planned.region.Region.Statements,
				)
			}
		}
	}

	accepted := AcceptedCoverEvidence{Mode: profile.Mode, Sources: make([]AcceptedSourceEvidence, len(preparedPlans)), Excluded: excluded}
	regionByObligation := make(map[StatementObligation]plannedRegionRef)
	for _, planned := range regions {
		if planned.region.Relation != CorrespondenceExact && planned.region.Relation != CorrespondenceCoversAll {
			continue
		}
		for _, obligation := range planned.region.Obligations {
			regionByObligation[obligation] = planned
		}
	}
	for planIndex, plan := range preparedPlans {
		source := AcceptedSourceEvidence{
			PackagePath: plan.PackagePath, OriginalPath: plan.OriginalPath,
			OriginalSource: append([]byte(nil), plan.OriginalSource...),
			Inventory:      cloneFileInventory(plan.Inventory),
		}
		for blockIndex, block := range plan.Inventory.Blocks {
			if block.Statements == 0 {
				continue
			}
			entry := StatementObligation{
				PackagePath: plan.PackagePath, OriginalPath: plan.OriginalPath,
				BlockIndex: blockIndex, StatementIndex: 0,
			}
			entryRegion, exists := regionByObligation[entry]
			if !exists {
				return AcceptedCoverEvidence{}, fmt.Errorf(
					"accept cover evidence %q: block %d entry obligation has no projectable region",
					plan.OriginalPath,
					blockIndex,
				)
			}
			observed := entryRegion.observed
			for statementIndex := 1; statementIndex < block.Statements; statementIndex++ {
				obligation := entry
				obligation.StatementIndex = statementIndex
				region, found := regionByObligation[obligation]
				if !found {
					return AcceptedCoverEvidence{}, fmt.Errorf(
						"accept cover evidence %q: block %d statement %d has no projectable region",
						plan.OriginalPath,
						blockIndex,
						statementIndex,
					)
				}
				observed = observed && region.observed
			}
			source.Blocks = append(source.Blocks, AcceptedBlockEvidence{
				BlockIndex: blockIndex, Count: entryRegion.count, Observed: observed,
			})
		}
		accepted.Sources[planIndex] = source
	}
	sortExcluded(accepted.Excluded)
	return accepted, nil
}

// ProjectAcceptedEvidence constructs C0 directly from accepted block evidence;
// it never invokes profile-position candidate selection.
func ProjectAcceptedEvidence(ctx context.Context, modulePath string, accepted AcceptedCoverEvidence, options Options) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	if modulePath == "" {
		return Report{}, errors.New("project accepted cover evidence: module path is empty")
	}
	if !accepted.Mode.valid() {
		return Report{}, fmt.Errorf("project accepted cover evidence: unsupported mode %q", accepted.Mode)
	}
	files := make([]analyzedFile, 0, len(accepted.Sources))
	seen := make(map[originalFileKey]struct{}, len(accepted.Sources))
	for _, source := range accepted.Sources {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		originalPath, err := moduleRelativePath(source.OriginalPath)
		if err != nil {
			return Report{}, fmt.Errorf("project accepted cover evidence: %w", err)
		}
		if source.PackagePath == "" {
			return Report{}, fmt.Errorf("project accepted cover evidence %q: package path is empty", originalPath)
		}
		key := originalFileKey{packagePath: source.PackagePath, originalPath: originalPath}
		if _, duplicate := seen[key]; duplicate {
			return Report{}, fmt.Errorf("project accepted cover evidence: duplicate source %q %q", source.PackagePath, originalPath)
		}
		seen[key] = struct{}{}
		inventory := cloneFileInventory(source.Inventory)
		if _, err := cloneAndValidateInventory(ctx, &inventory); err != nil {
			return Report{}, fmt.Errorf("project accepted cover evidence %q inventory: %w", originalPath, err)
		}
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, originalPath, source.OriginalSource, parser.ParseComments|parser.AllErrors)
		if err != nil {
			return Report{}, fmt.Errorf("project accepted cover evidence %q: parse: %w", originalPath, err)
		}
		functions := discoverFunctions(fset, parsed)
		blockEvidence := make(map[int]AcceptedBlockEvidence, len(source.Blocks))
		for _, block := range source.Blocks {
			if block.BlockIndex < 0 || block.BlockIndex >= len(inventory.Blocks) {
				return Report{}, fmt.Errorf("project accepted cover evidence %q: block index %d is out of range", originalPath, block.BlockIndex)
			}
			if _, duplicate := blockEvidence[block.BlockIndex]; duplicate {
				return Report{}, fmt.Errorf("project accepted cover evidence %q: duplicate block index %d", originalPath, block.BlockIndex)
			}
			blockEvidence[block.BlockIndex] = block
		}
		blocks := make([]analyzedProfileBlock, 0, len(inventory.Blocks))
		for blockIndex, inventoryBlock := range inventory.Blocks {
			if inventoryBlock.Statements == 0 {
				continue
			}
			if ownerForBlock(functions, inventoryBlock.PhysicalRange) < 0 {
				return Report{}, fmt.Errorf(
					"project accepted cover evidence %q: inventory block %s has no function owner",
					originalPath,
					formatRange(inventoryBlock.PhysicalRange),
				)
			}
			evidence := blockEvidence[blockIndex]
			blocks = append(blocks, analyzedProfileBlock{
				profile:  ProfileBlock{Position: inventoryBlock.PhysicalRange, Statements: inventoryBlock.Statements, Count: evidence.Count},
				evidence: evidence.Observed,
			})
		}
		original := &originalFileAccumulator{key: key, source: append([]byte(nil), source.OriginalSource...), inventory: &inventory}
		fileReport, include, err := buildFileReport(ctx, original, functions, blocks, options)
		if err != nil {
			return Report{}, err
		}
		if include {
			files = append(files, analyzedFile{packagePath: source.PackagePath, report: fileReport})
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].packagePath != files[j].packagePath {
			return files[i].packagePath < files[j].packagePath
		}
		return files[i].report.Path < files[j].report.Path
	})
	report := Report{Mode: accepted.Mode, ModulePath: modulePath, Packages: make([]PackageReport, 0), Excluded: append([]ExcludedBlock(nil), accepted.Excluded...)}
	for _, file := range files {
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

func prepareCoveragePlans(ctx context.Context, plans []SourceCoveragePlan) ([]SourceCoveragePlan, error) {
	prepared := make([]SourceCoveragePlan, len(plans))
	seen := make(map[originalFileKey]struct{}, len(plans))
	for index, plan := range plans {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		originalPath, err := moduleRelativePath(plan.OriginalPath)
		if err != nil {
			return nil, fmt.Errorf("accept cover evidence plan %d: %w", index, err)
		}
		if plan.PackagePath == "" {
			return nil, fmt.Errorf("accept cover evidence plan %d: package path is empty", index)
		}
		key := originalFileKey{packagePath: plan.PackagePath, originalPath: originalPath}
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("accept cover evidence: duplicate source plan %q %q", plan.PackagePath, originalPath)
		}
		seen[key] = struct{}{}
		inventory := cloneFileInventory(plan.Inventory)
		validated, err := cloneAndValidateInventory(ctx, &inventory)
		if err != nil {
			return nil, fmt.Errorf("accept cover evidence %q inventory: %w", originalPath, err)
		}
		derived, err := BuildInventory(originalPath, plan.OriginalSource)
		if err != nil {
			return nil, fmt.Errorf("accept cover evidence %q: %w", originalPath, err)
		}
		if !equalInventory(validated, &derived) {
			return nil, fmt.Errorf("accept cover evidence %q: inventory differs from original source", originalPath)
		}
		for _, region := range plan.Correspondence.Regions() {
			for _, obligation := range region.Obligations {
				if obligation.PackagePath != plan.PackagePath || obligation.OriginalPath != originalPath ||
					obligation.BlockIndex < 0 || obligation.BlockIndex >= len(inventory.Blocks) ||
					obligation.StatementIndex < 0 || obligation.StatementIndex >= inventory.Blocks[obligation.BlockIndex].Statements {
					return nil, fmt.Errorf(
						"accept cover evidence %q: correspondence contains foreign or out-of-range obligation %#v",
						originalPath,
						obligation,
					)
				}
			}
		}
		prepared[index] = SourceCoveragePlan{
			PackagePath: plan.PackagePath, OriginalPath: originalPath,
			OriginalSource: append([]byte(nil), plan.OriginalSource...),
			Inventory:      inventory,
			Correspondence: plan.Correspondence,
		}
	}
	sort.Slice(prepared, func(i, j int) bool {
		if prepared[i].PackagePath != prepared[j].PackagePath {
			return prepared[i].PackagePath < prepared[j].PackagePath
		}
		return prepared[i].OriginalPath < prepared[j].OriginalPath
	})
	return prepared, nil
}

func cloneFileInventory(inventory FileInventory) FileInventory {
	cloned := FileInventory{Blocks: make([]InventoryBlock, len(inventory.Blocks))}
	for index, block := range inventory.Blocks {
		cloned.Blocks[index] = block
		cloned.Blocks[index].ProfileAnchors = append([]Position(nil), block.ProfileAnchors...)
		cloned.Blocks[index].StatementUnits = append([]InventoryStatement(nil), block.StatementUnits...)
	}
	return cloned
}

func prepareKnownProfilePaths(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		normalized := normalizeAcceptancePath(value)
		if normalized == "" || normalized == "." {
			return nil, fmt.Errorf("path %d is empty", index)
		}
		if _, duplicate := seen[normalized]; duplicate {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	sort.Strings(result)
	return result, nil
}

func matchesKnownProfilePath(actual string, candidates []string, modulePath string) bool {
	for _, candidate := range candidates {
		if acceptancePathMatches(actual, candidate, modulePath, "") {
			return true
		}
	}
	return false
}

func acceptancePathMatches(actual, planned, modulePath, packagePath string) bool {
	actual = normalizeAcceptancePath(actual)
	planned = normalizeAcceptancePath(planned)
	modulePath = normalizeAcceptancePath(modulePath)
	packagePath = normalizeAcceptancePath(packagePath)
	if actual == planned || modulePath != "" && actual == modulePath+"/"+planned {
		return true
	}
	if packagePath != "" && actual == packagePath+"/"+path.Base(planned) {
		return true
	}
	return strings.Contains(planned, "/") && strings.HasSuffix(actual, "/"+planned)
}

func normalizeAcceptancePath(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	value = strings.TrimPrefix(value, "./")
	if value == "" {
		return ""
	}
	return path.Clean(value)
}
