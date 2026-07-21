// Package c0 parses Go coverprofiles and maps statement coverage from an
// instrumented workspace back to original module-relative source.
package c0

import (
	"context"
	"io"
)

// Mode is a Go coverprofile counter mode.
type Mode string

const (
	ModeSet    Mode = "set"
	ModeCount  Mode = "count"
	ModeAtomic Mode = "atomic"
)

// Position is a physical source position. Original Go source positions are
// one-based; parsed coverprofiles may contain zero coordinates.
type Position struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

// SourceRange is a half-open physical source range.
type SourceRange struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Profile is parsed, deterministically ordered coverprofile data.
type Profile struct {
	Mode  Mode          `json:"mode"`
	Files []ProfileFile `json:"files"`
}

// ProfileFile contains all merged coverage blocks for one profile path.
type ProfileFile struct {
	Path   string         `json:"path"`
	Blocks []ProfileBlock `json:"blocks"`
}

// ProfileBlock is one Go coverprofile block. Statements is the NumStmt field
// from the profile; Count is merged according to Profile.Mode.
type ProfileBlock struct {
	Position   SourceRange `json:"position"`
	Statements int         `json:"statements"`
	Count      uint64      `json:"count"`
}

// SourceMap explicitly maps paths and blocks in an instrumented profile back
// to their original source locations.
type SourceMap struct {
	ModulePath string        `json:"module"`
	Files      []FileMapping `json:"files"`
}

// FileMapping maps one profile path. Generated files are accepted in the
// profile but excluded from all coverage denominators.
type FileMapping struct {
	ProfilePath    string         `json:"profile_path"`
	PackagePath    string         `json:"package"`
	OriginalPath   string         `json:"original_path"`
	OriginalSource []byte         `json:"-"`
	Generated      bool           `json:"generated"`
	Blocks         []BlockMapping `json:"blocks"`
	Inventory      *FileInventory `json:"inventory,omitempty"`
}

// FileInventory is the statement/block denominator derived from unchanged
// original source using Go's control-flow block rules. Instrumented profiles
// supply execution counts only; their NumStmt values are never authoritative
// when an inventory is present.
type FileInventory struct {
	Blocks []InventoryBlock `json:"blocks"`
}

// InventoryBlock couples the physical original-source range used for reports
// with the logical range/file emitted by Go coverage. ProfileAnchors are the
// starts of original statements represented by this block.
type InventoryBlock struct {
	PhysicalRange  SourceRange          `json:"physical_range"`
	ProfileFile    string               `json:"profile_file"`
	ProfileRange   SourceRange          `json:"profile_range"`
	ProfileAnchors []Position           `json:"profile_anchors"`
	StatementUnits []InventoryStatement `json:"statement_units,omitempty"`
	Statements     int                  `json:"statements"`
}

// InventoryStatement identifies one original statement unit in both physical
// source and Go cover's logical coordinate space. ProfileFile is recorded per
// statement because //line directives can make one rewritten cover block span
// original and generated logical files.
type InventoryStatement struct {
	PhysicalPosition Position `json:"physical_position"`
	ProfileFile      string   `json:"profile_file"`
	ProfilePosition  Position `json:"profile_position"`
}

// BlockMapping overrides the identity mapping for one exact profile block
// range. Unlisted blocks retain identity mapping. Generated blocks represent
// instrumentation statements and are excluded from all denominators.
type BlockMapping struct {
	ProfileRange  SourceRange `json:"profile_range"`
	OriginalRange SourceRange `json:"original_range"`
	Generated     bool        `json:"generated"`
}

// Options controls report construction.
type Options struct {
	// IncludeEmptyFunctions includes AST functions with no owned profile
	// statements. They contribute one uncovered function and zero statements.
	IncludeEmptyFunctions bool
}

// Counts is a coverage numerator and denominator.
type Counts struct {
	Covered int `json:"covered"`
	Total   int `json:"total"`
}

// Summary keeps statement, profile-block, and function counts separate.
type Summary struct {
	Statements Counts `json:"statements"`
	Blocks     Counts `json:"blocks"`
	Functions  Counts `json:"functions"`
}

// Report is a deterministic module-to-statement C0 hierarchy.
type Report struct {
	Mode       Mode            `json:"mode"`
	ModulePath string          `json:"module"`
	Summary    Summary         `json:"summary"`
	Packages   []PackageReport `json:"packages"`
	Excluded   []ExcludedBlock `json:"excluded"`
}

// PackageReport contains C0 data for one import path.
type PackageReport struct {
	Path string `json:"path"`
	// Evidence is true when at least one original statement block was found in
	// the coverprofile, including blocks whose execution count is zero.
	Evidence bool         `json:"evidence"`
	Summary  Summary      `json:"summary"`
	Files    []FileReport `json:"files"`
}

// FileReport contains the original functions and their owned statement blocks.
// Blocks without an original statement owner are listed in Report.Excluded.
type FileReport struct {
	Path string `json:"path"`
	// Evidence distinguishes an observed zero count from inventory retained
	// because a partial profile omitted this file.
	Evidence  bool             `json:"evidence"`
	Summary   Summary          `json:"summary"`
	Functions []FunctionReport `json:"functions"`
}

// FunctionReport contains the statement blocks owned by one declaration or
// function literal. Nested literals are separate, flat function entries.
type FunctionReport struct {
	Name     string      `json:"name"`
	Position SourceRange `json:"position"`
	// Evidence is true when at least one owned statement block was present in
	// the profile. A false value is static knowledge, not proof of nonexecution.
	Evidence bool `json:"evidence"`
	// CompleteEvidence is true when every nonempty original cover block owned
	// by the function was present in the profile.
	CompleteEvidence bool             `json:"complete_evidence"`
	Summary          Summary          `json:"summary"`
	Blocks           []StatementBlock `json:"blocks"`
}

// StatementBlock is one original Go cover block. Evidence distinguishes an
// observed zero count from a static block retained after partial test output.
// Its statement coverage is all-or-nothing, matching go tool cover semantics.
type StatementBlock struct {
	Position   SourceRange `json:"position"`
	Statements int         `json:"statements"`
	Count      uint64      `json:"count"`
	Evidence   bool        `json:"evidence"`
	Summary    Summary     `json:"summary"`
}

// ExclusionReason explains why a profile block was not counted.
type ExclusionReason string

const (
	ExcludeGeneratedFile       ExclusionReason = "generated_file"
	ExcludeGeneratedBlock      ExclusionReason = "generated_block"
	ExcludeUnmappedFile        ExclusionReason = "unmapped_file"
	ExcludeNoOriginalStatement ExclusionReason = "no_original_statement"
	ExcludeReportScope         ExclusionReason = "report_scope"
)

// ExcludedBlock is profile data deliberately kept out of every denominator.
// It is retained so generated or unmappable data is never silently discarded.
type ExcludedBlock struct {
	ProfilePath   string          `json:"profile_path"`
	ProfileRange  SourceRange     `json:"profile_range"`
	OriginalPath  string          `json:"original_path,omitempty"`
	OriginalRange *SourceRange    `json:"original_range,omitempty"`
	Statements    int             `json:"statements"`
	Count         uint64          `json:"count"`
	Reason        ExclusionReason `json:"reason"`
}

// ParseAndAnalyze parses a coverprofile then maps and aggregates it.
func ParseAndAnalyze(ctx context.Context, reader io.Reader, sourceMap SourceMap, options Options) (Report, error) {
	profile, err := ParseProfile(reader)
	if err != nil {
		return Report{}, err
	}
	return Analyze(ctx, profile, sourceMap, options)
}
