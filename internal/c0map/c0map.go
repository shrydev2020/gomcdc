// Package c0map binds coverprofile file names from an instrumented workspace
// to immutable original source files. It retains original inventory when a
// partial profile omits a source and leaves unknown profile paths visible to
// c0 as excluded evidence.
package c0map

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/shrydev2020/gomcdc/internal/c0"
)

type Source struct {
	PackagePath    string
	RelativePath   string
	OriginalSource []byte
	Inventory      *c0.FileInventory
}

type GeneratedFile struct {
	// Path may be a module-relative generated file, a virtual //line filename,
	// or the exact path emitted by a coverprofile.
	Path string
}

type matchRank uint8

const (
	noMatch matchRank = iota
	suffixMatch
	exactMatch
)

// Build constructs source mappings while ctx permits recovery work and retains
// inventory-only original files so partial profiles do not erase known
// statement and function entities.
func Build(ctx context.Context, profile c0.Profile, modulePath string, sources []Source, generated []GeneratedFile) (c0.SourceMap, error) {
	if err := ctx.Err(); err != nil {
		return c0.SourceMap{}, err
	}
	if modulePath == "" {
		return c0.SourceMap{}, fmt.Errorf("C0 source mapping requires a module path")
	}
	preparedSources := append([]Source(nil), sources...)
	for index := range preparedSources {
		if err := ctx.Err(); err != nil {
			return c0.SourceMap{}, err
		}
		source := &preparedSources[index]
		source.RelativePath = normalize(source.RelativePath)
		if source.RelativePath == "" || source.RelativePath == "." || strings.HasPrefix(source.RelativePath, "../") {
			return c0.SourceMap{}, fmt.Errorf("invalid original source path %q", sources[index].RelativePath)
		}
		if source.Inventory == nil {
			inventory, err := c0.BuildInventory(source.RelativePath, source.OriginalSource)
			if err != nil {
				return c0.SourceMap{}, err
			}
			source.Inventory = &inventory
		} else {
			source.Inventory = cloneInventory(source.Inventory)
		}
	}
	generatedPaths := make([]string, 0, len(generated))
	for _, file := range generated {
		if err := ctx.Err(); err != nil {
			return c0.SourceMap{}, err
		}
		if normalized := normalize(file.Path); normalized != "" {
			generatedPaths = append(generatedPaths, normalized)
		}
	}

	result := c0.SourceMap{ModulePath: modulePath}
	profileBacked := make([]bool, len(preparedSources))
	for _, profileFile := range profile.Files {
		if err := ctx.Err(); err != nil {
			return c0.SourceMap{}, err
		}
		profilePath := normalize(profileFile.Path)
		matches := make([]int, 0, 1)
		bestRank := noMatch
		for index, source := range preparedSources {
			if err := ctx.Err(); err != nil {
				return c0.SourceMap{}, err
			}
			rank := sourceMatchRank(profilePath, modulePath, source)
			if rank > bestRank {
				bestRank = rank
				matches = matches[:0]
			}
			if rank != noMatch && rank == bestRank {
				matches = append(matches, index)
			}
		}
		if len(matches) > 1 {
			return c0.SourceMap{}, fmt.Errorf("profile path %q ambiguously matches %d original files", profileFile.Path, len(matches))
		}
		if len(matches) == 1 {
			sourceIndex := matches[0]
			source := preparedSources[sourceIndex]
			profileBacked[sourceIndex] = true
			result.Files = append(result.Files, c0.FileMapping{
				ProfilePath:    profileFile.Path,
				PackagePath:    source.PackagePath,
				OriginalPath:   source.RelativePath,
				OriginalSource: append([]byte(nil), source.OriginalSource...),
				Inventory:      cloneInventory(source.Inventory),
			})
			continue
		}
		for _, generatedPath := range generatedPaths {
			if matchesPath(profilePath, modulePath, generatedPath) {
				result.Files = append(result.Files, c0.FileMapping{ProfilePath: profileFile.Path, Generated: true})
				break
			}
		}
	}
	for index, source := range preparedSources {
		if err := ctx.Err(); err != nil {
			return c0.SourceMap{}, err
		}
		if profileBacked[index] {
			continue
		}
		result.Files = append(result.Files, c0.FileMapping{
			ProfilePath:    inventoryProfilePath(modulePath, source.RelativePath),
			PackagePath:    source.PackagePath,
			OriginalPath:   source.RelativePath,
			OriginalSource: append([]byte(nil), source.OriginalSource...),
			Inventory:      cloneInventory(source.Inventory),
		})
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].ProfilePath < result.Files[j].ProfilePath })
	return result, nil
}

func inventoryProfilePath(modulePath, relativePath string) string {
	return normalize(modulePath) + "/" + normalize(relativePath)
}

func sourceMatchRank(profilePath, modulePath string, source Source) matchRank {
	bestRank := pathMatchRank(profilePath, modulePath, source.RelativePath)
	if bestRank == exactMatch {
		return bestRank
	}
	for _, block := range source.Inventory.Blocks {
		logicalFile := normalize(block.ProfileFile)
		if logicalFile == "" {
			continue
		}
		if rank := pathMatchRank(profilePath, modulePath, logicalFile); rank > bestRank {
			bestRank = rank
		}
		packageAlias := normalize(source.PackagePath) + "/" + path.Base(logicalFile)
		if rank := pathMatchRank(profilePath, "", packageAlias); rank > bestRank {
			bestRank = rank
		}
		if bestRank == exactMatch {
			return bestRank
		}
	}
	return bestRank
}

func cloneInventory(inventory *c0.FileInventory) *c0.FileInventory {
	if inventory == nil {
		return nil
	}
	cloned := &c0.FileInventory{Blocks: make([]c0.InventoryBlock, len(inventory.Blocks))}
	for index, block := range inventory.Blocks {
		cloned.Blocks[index] = block
		cloned.Blocks[index].ProfileAnchors = append([]c0.Position(nil), block.ProfileAnchors...)
	}
	return cloned
}

func matchesPath(profilePath, modulePath, candidate string) bool {
	return pathMatchRank(profilePath, modulePath, candidate) != noMatch
}

func pathMatchRank(profilePath, modulePath, candidate string) matchRank {
	profilePath = normalize(profilePath)
	candidate = normalize(candidate)
	if profilePath == candidate || modulePath != "" && profilePath == normalize(modulePath)+"/"+candidate {
		return exactMatch
	}
	// Absolute paths and toolchain-specific prefixes are accepted only by a
	// complete path-segment suffix, never by a basename.
	if strings.Contains(candidate, "/") && strings.HasSuffix(profilePath, "/"+candidate) {
		return suffixMatch
	}
	return noMatch
}

func normalize(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	value = strings.TrimPrefix(value, "./")
	if value == "" {
		return ""
	}
	return path.Clean(value)
}
