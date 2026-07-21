// Package instrument rewrites copied Go files for AST coverage probes.
// It never writes an analyzer.File's original source path.
package instrument

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/printer"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/shrydev2020/gomcdc/internal/analyzer"
	"github.com/shrydev2020/gomcdc/internal/c0"
	cover "github.com/shrydev2020/gomcdc/internal/coverage"
)

const defaultHelperBase = "__gomcdcHooks"

type decisionLocation struct {
	kind       cover.DecisionKind
	start, end int
}

type clauseLocation struct {
	kind       cover.ClauseKind
	role       cover.ClauseRole
	start, end int
}

type noMatchLocation struct {
	kind       cover.ClauseKind
	start, end int
}

// FileMapping maps analysis of an original file to its unchanged copied path.
type FileMapping struct {
	CopyPath                string
	Analysis                analyzer.File
	OriginalInventory       *c0.FileInventory
	ExcludeFromCoveragePlan bool
}

// PackageOptions describes one copied package to instrument. ActiveFiles must
// include every build-active source and test file whose identifiers may collide
// with the generated package-wide helper.
type PackageOptions struct {
	Context                    context.Context
	Directory                  string
	PackageName                string
	PackagePath                string
	RuntimeImportPath          string
	CompilerClauseSelection    bool
	PlanCoverageCorrespondence bool
	TestOnly                   bool
	ActiveFiles                []string
	Files                      []FileMapping
}

// PackageResult identifies the generated bridge and helper used by rewritten
// files.
type PackageResult struct {
	HelperName     string
	BridgePath     string
	GeneratedFiles []string
	SourceMaps     []SourceMap
	CoveragePlans  []FileCoveragePlan
}

// SourceMap identifies the original logical filename used by printer.SourcePos,
// the virtual filename requested for synthetic statements, and the manifest
// needed when Go cover retains the physical filename despite //line directives.
type SourceMap struct {
	InstrumentedFile string
	OriginalFile     string
	GeneratedFile    string
	CompilerFile     string
	GeneratedRegions []GeneratedRegion
	LineMappings     []analyzer.LineMapping
}

// FileCoveragePlan keeps coverage acceptance authority separate from SourceMap
// coordinate translation. Correspondence is planned before Go cover executes.
type FileCoveragePlan struct {
	WorkspaceFile  string
	OriginalPath   string
	Correspondence c0.CoverageCorrespondence
}

// GeneratedRegion identifies instrumentation-only statements which Go's
// cover tool may merge into a user block even when //line uses a virtual file.
// Consumers should derive statement coverage from the original statement
// inventory and use this manifest to subtract/ignore synthetic statements.
type GeneratedRegion struct {
	Kind           string
	Anchor         cover.SourceLocation
	StatementCount uint32
}

// InstrumentPackage selects one collision-free helper name, validates and
// transforms every requested copied file, then creates the generated bridge.
func InstrumentPackage(options PackageOptions) (PackageResult, error) {
	ctx := options.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return PackageResult{}, err
	}
	if options.Directory == "" {
		return PackageResult{}, errors.New("instrument package: directory is empty")
	}
	if !token.IsIdentifier(options.PackageName) {
		return PackageResult{}, fmt.Errorf("instrument package: invalid package name %q", options.PackageName)
	}
	if options.RuntimeImportPath == "" {
		return PackageResult{}, errors.New("instrument package: runtime import path is empty")
	}
	if options.PackagePath == "" {
		return PackageResult{}, errors.New("instrument package: package path is empty")
	}

	activeFiles := append([]string(nil), options.ActiveFiles...)
	if len(activeFiles) == 0 {
		for _, mapping := range options.Files {
			activeFiles = append(activeFiles, mapping.CopyPath)
		}
	}
	helperName, err := SelectHelperName(ctx, activeFiles)
	if err != nil {
		return PackageResult{}, fmt.Errorf("instrument package: select helper name: %w", err)
	}

	type transformedFile struct {
		path string
		mode os.FileMode
		data []byte
		map_ SourceMap
	}
	transformed := make([]transformedFile, 0, len(options.Files))
	coveragePlans := make([]FileCoveragePlan, 0, len(options.Files))
	for _, mapping := range options.Files {
		if err := ctx.Err(); err != nil {
			return PackageResult{}, err
		}
		planCoverage := options.PlanCoverageCorrespondence && !mapping.ExcludeFromCoveragePlan
		var originalInventory c0.FileInventory
		if planCoverage {
			if mapping.OriginalInventory != nil {
				originalInventory = *mapping.OriginalInventory
			} else {
				originalInventory, err = c0.BuildInventory(mapping.Analysis.RelativePath, mapping.Analysis.Source)
				if err != nil {
					return PackageResult{}, fmt.Errorf("plan coverage for copied file %q: build original inventory: %w", mapping.CopyPath, err)
				}
			}
		}
		if len(mapping.Analysis.Decisions) == 0 && len(mapping.Analysis.Clauses) == 0 {
			if planCoverage {
				contents, err := os.ReadFile(mapping.CopyPath)
				if err != nil {
					return PackageResult{}, fmt.Errorf("plan coverage for copied file %q: read: %w", mapping.CopyPath, err)
				}
				if hash := sha256.Sum256(contents); hash != mapping.Analysis.SourceHash {
					return PackageResult{}, fmt.Errorf("plan coverage for copied file %q: copy differs from analyzed original", mapping.CopyPath)
				}
				correspondence, err := planRewrittenCoverageCorrespondence(
					ctx, options.PackagePath, mapping.Analysis.RelativePath, originalInventory, contents, nil,
				)
				if err != nil {
					return PackageResult{}, fmt.Errorf("plan coverage for copied file %q: %w", mapping.CopyPath, err)
				}
				coveragePlans = append(coveragePlans, FileCoveragePlan{
					WorkspaceFile: mapping.CopyPath, OriginalPath: mapping.Analysis.RelativePath, Correspondence: correspondence,
				})
			}
			continue
		}
		data, mode, sourceMap, err := transformCopiedFile(mapping.CopyPath, mapping.Analysis, helperName, options.CompilerClauseSelection)
		if err != nil {
			return PackageResult{}, err
		}
		if planCoverage {
			generatedProfileFiles := []string{
				filepath.ToSlash(filepath.Join(filepath.Dir(mapping.Analysis.RelativePath), sourceMap.GeneratedFile)),
				filepath.ToSlash(filepath.Join(filepath.Dir(mapping.Analysis.RelativePath), sourceMap.CompilerFile)),
			}
			correspondence, err := planRewrittenCoverageCorrespondence(
				ctx, options.PackagePath, mapping.Analysis.RelativePath, originalInventory, data, generatedProfileFiles,
			)
			if err != nil {
				return PackageResult{}, fmt.Errorf("instrument copied file %q: %w", mapping.CopyPath, err)
			}
			coveragePlans = append(coveragePlans, FileCoveragePlan{
				WorkspaceFile: mapping.CopyPath, OriginalPath: mapping.Analysis.RelativePath, Correspondence: correspondence,
			})
		}
		if err := ctx.Err(); err != nil {
			return PackageResult{}, err
		}
		transformed = append(transformed, transformedFile{path: mapping.CopyPath, mode: mode, data: data, map_: sourceMap})
	}
	for _, file := range transformed {
		if err := ctx.Err(); err != nil {
			return PackageResult{}, err
		}
		if err := replaceFile(file.path, file.data, file.mode); err != nil {
			return PackageResult{}, fmt.Errorf("instrument copied file %q: write: %w", file.path, err)
		}
	}

	if err := ctx.Err(); err != nil {
		return PackageResult{}, err
	}
	bridgePath, err := WriteBridge(BridgeOptions{
		Directory:         options.Directory,
		PackageName:       options.PackageName,
		PackagePath:       options.PackagePath,
		RuntimeImportPath: options.RuntimeImportPath,
		HelperName:        helperName,
		TestOnly:          options.TestOnly,
	})
	if err != nil {
		return PackageResult{}, err
	}
	result := PackageResult{
		HelperName: helperName, BridgePath: bridgePath, GeneratedFiles: []string{bridgePath}, CoveragePlans: coveragePlans,
	}
	for _, file := range transformed {
		result.SourceMaps = append(result.SourceMaps, file.map_)
	}
	return result, nil
}

func planRewrittenCoverageCorrespondence(
	ctx context.Context,
	packagePath string,
	originalPath string,
	originalInventory c0.FileInventory,
	rewritten []byte,
	generatedProfileFiles []string,
) (c0.CoverageCorrespondence, error) {
	rewrittenInventory, err := c0.BuildInventory(originalPath, rewritten)
	if err != nil {
		return c0.CoverageCorrespondence{}, fmt.Errorf("build rewritten coverage inventory: %w", err)
	}
	return c0.PlanCoverageCorrespondence(ctx, c0.CorrespondencePlanInput{
		PackagePath: packagePath, OriginalPath: originalPath,
		Original: originalInventory, Rewritten: rewrittenInventory, GeneratedProfileFiles: generatedProfileFiles,
	})
}

// InstrumentFile transforms one copied file using analysis from its unchanged
// original. It rejects the original itself, symlinks/hardlinks to it, and stale
// copies whose bytes differ from the analyzed source.
func InstrumentFile(copyPath string, analysis analyzer.File, helperName string) error {
	if !token.IsIdentifier(helperName) {
		return fmt.Errorf("instrument copied file %q: invalid helper name %q", copyPath, helperName)
	}
	if len(analysis.Decisions) == 0 && len(analysis.Clauses) == 0 {
		return nil
	}
	data, mode, _, err := transformCopiedFile(copyPath, analysis, helperName, false)
	if err != nil {
		return err
	}
	if err := replaceFile(copyPath, data, mode); err != nil {
		return fmt.Errorf("instrument copied file %q: write: %w", copyPath, err)
	}
	return nil
}

// SelectHelperName scans every supplied source/test file and returns a package-
// wide identifier absent from all of them.
func SelectHelperName(ctx context.Context, activeFiles []string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	used := make(map[string]struct{})
	paths := append([]string(nil), activeFiles...)
	sort.Strings(paths)
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read active file %q: %w", path, err)
		}
		file := token.NewFileSet().AddFile(path, -1, len(contents))
		var lexer scanner.Scanner
		// Identifier collision detection must not turn a malformed _test.go file
		// into an instrumentation error. go test remains the authority for syntax
		// failures, so scanner diagnostics are intentionally ignored here.
		lexer.Init(file, contents, nil, 0)
		for {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			_, kind, literal := lexer.Scan()
			if kind == token.EOF {
				break
			}
			if kind == token.IDENT {
				used[literal] = struct{}{}
			}
		}
	}
	for suffix := 0; ; suffix++ {
		candidate := defaultHelperBase
		if suffix > 0 {
			candidate += "_" + strconv.Itoa(suffix)
		}
		_, helperExists := used[candidate]
		_, slotsExist := used[candidate+"Slots"]
		_, runtimeAliasExists := used[candidate+"Runtime"]
		_, evaluationTypeExists := used[candidate+"EvaluationID"]
		if !helperExists && !slotsExist && !runtimeAliasExists && !evaluationTypeExists {
			return candidate, nil
		}
	}
}

// BridgeOptions describes a generated same-package import bridge.
type BridgeOptions struct {
	Directory         string
	PackageName       string
	PackagePath       string
	RuntimeImportPath string
	HelperName        string
	TestOnly          bool
}

// WriteBridge creates one generated bridge file without overwriting an
// existing file. User source imports remain untouched.
func WriteBridge(options BridgeOptions) (string, error) {
	if options.Directory == "" {
		return "", errors.New("write bridge: directory is empty")
	}
	if !token.IsIdentifier(options.PackageName) {
		return "", fmt.Errorf("write bridge: invalid package name %q", options.PackageName)
	}
	if options.RuntimeImportPath == "" {
		return "", errors.New("write bridge: runtime import path is empty")
	}
	if options.PackagePath == "" {
		return "", errors.New("write bridge: package path is empty")
	}
	if !token.IsIdentifier(options.HelperName) {
		return "", fmt.Errorf("write bridge: invalid helper name %q", options.HelperName)
	}

	source := fmt.Sprintf(`// Code generated by gomcdc. DO NOT EDIT.

package %s

import %sRuntime %s

var %s = %sRuntime.NewHooks(%s)

type %sEvaluationID = %sRuntime.EvaluationID
`, options.PackageName,
		options.HelperName, strconv.Quote(options.RuntimeImportPath),
		options.HelperName, options.HelperName, strconv.Quote(options.PackagePath),
		options.HelperName, options.HelperName,
	)
	formatted, err := format.Source([]byte(source))
	if err != nil {
		return "", fmt.Errorf("write bridge: format generated source: %w", err)
	}

	suffix := strings.TrimPrefix(options.HelperName, defaultHelperBase)
	suffix = strings.TrimPrefix(suffix, "_")
	if suffix == "" {
		suffix = "0"
	}
	baseName := "zz_gomcdc_bridge_" + suffix
	extension := ".go"
	if options.TestOnly {
		extension = "_test.go"
	}
	for index := 0; ; index++ {
		indexSuffix := ""
		if index > 0 {
			indexSuffix = "_" + strconv.Itoa(index)
		}
		path := filepath.Join(options.Directory, baseName+indexSuffix+extension)
		file, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(createErr, fs.ErrExist) {
			continue
		}
		if createErr != nil {
			return "", fmt.Errorf("write bridge %q: create: %w", path, createErr)
		}
		writeErr := error(nil)
		if _, err := file.Write(formatted); err != nil {
			writeErr = err
		}
		if err := file.Close(); writeErr == nil && err != nil {
			writeErr = err
		}
		if writeErr != nil {
			_ = os.Remove(path)
			return "", fmt.Errorf("write bridge %q: %w", path, writeErr)
		}
		return path, nil
	}
}

func transformCopiedFile(copyPath string, analysis analyzer.File, helperName string, compilerClauseSelection bool) ([]byte, os.FileMode, SourceMap, error) {
	if copyPath == "" {
		return nil, 0, SourceMap{}, errors.New("instrument copied file: copy path is empty")
	}
	if err := ensureDistinctCopy(copyPath, analysis.OriginalPath); err != nil {
		return nil, 0, SourceMap{}, fmt.Errorf("instrument copied file %q: %w", copyPath, err)
	}
	info, err := os.Stat(copyPath)
	if err != nil {
		return nil, 0, SourceMap{}, fmt.Errorf("instrument copied file %q: stat: %w", copyPath, err)
	}
	contents, err := os.ReadFile(copyPath)
	if err != nil {
		return nil, 0, SourceMap{}, fmt.Errorf("instrument copied file %q: read: %w", copyPath, err)
	}
	if hash := sha256.Sum256(contents); hash != analysis.SourceHash {
		return nil, 0, SourceMap{}, fmt.Errorf("instrument copied file %q: copy differs from analyzed original", copyPath)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.ToSlash(analysis.RelativePath), contents, parser.ParseComments|parser.AllErrors)
	if err != nil {
		return nil, 0, SourceMap{}, fmt.Errorf("instrument copied file %q: parse: %w", copyPath, err)
	}
	transformer, err := newFileTransformer(fset, helperName, analysis, compilerClauseSelection)
	if err != nil {
		return nil, 0, SourceMap{}, fmt.Errorf("instrument copied file %q: %w", copyPath, err)
	}
	if err := transformer.transform(file); err != nil {
		return nil, 0, SourceMap{}, fmt.Errorf("instrument copied file %q: %w", copyPath, err)
	}

	var output bytes.Buffer
	config := printer.Config{Mode: printer.SourcePos | printer.TabIndent | printer.UseSpaces, Tabwidth: 8}
	if err := config.Fprint(&output, fset, file); err != nil {
		return nil, 0, SourceMap{}, fmt.Errorf("instrument copied file %q: print with source positions: %w", copyPath, err)
	}
	generatedIdentity := sha256.Sum256(append(append([]byte(nil), analysis.RelativePath...), analysis.SourceHash[:]...))
	sourceMap := SourceMap{
		InstrumentedFile: copyPath,
		OriginalFile:     filepath.ToSlash(analysis.RelativePath),
		GeneratedFile:    filepath.ToSlash(filepath.Join(".gomcdc", "generated", fmt.Sprintf("%x.go", generatedIdentity[:12]))),
		CompilerFile:     filepath.ToSlash(filepath.Join(".gomcdc", "compiler", fmt.Sprintf("%x.go", generatedIdentity[:12]))),
		GeneratedRegions: append([]GeneratedRegion(nil), transformer.generatedRegions...),
		LineMappings:     append([]analyzer.LineMapping(nil), analysis.LineMappings...),
	}
	formatted, err := mapGeneratedStatements(
		output.Bytes(), helperName, sourceMap.OriginalFile, sourceMap.GeneratedFile, sourceMap.CompilerFile, analysis.Source,
	)
	if err != nil {
		return nil, 0, SourceMap{}, fmt.Errorf("instrument copied file %q: map generated coverage lines: %w", copyPath, err)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), copyPath, formatted, parser.ParseComments|parser.AllErrors); err != nil {
		return nil, 0, SourceMap{}, fmt.Errorf("instrument copied file %q: validate transformed source: %w", copyPath, err)
	}
	return append([]byte(nil), formatted...), info.Mode().Perm(), sourceMap, nil
}

type logicalLine struct {
	file   string
	line   int
	column int
}

// mapGeneratedStatements requests a virtual filename for every inserted
// statement while retaining printer.SourcePos's logical line for the next
// original line. Some Go cover versions retain the physical filename anyway;
// SourceMap.GeneratedRegions is the authoritative exclusion mechanism when the
// compiler profile retains physical filenames.
func mapGeneratedStatements(source []byte, helperName, originalFile, generatedFile, compilerFile string, originalSource []byte) ([]byte, error) {
	lines := strings.Split(string(source), "\n")
	originalPositions := originalLogicalLines(originalSource, originalFile)
	for index, line := range lines {
		line = restoreUserLogicalDirective(line, originalFile, originalPositions)
		lines[index] = normalizeOriginalLineDirective(line, originalFile)
	}
	generatedLines, err := generatedStatementLineKinds([]byte(strings.Join(lines, "\n")), helperName)
	if err != nil {
		return nil, fmt.Errorf("locate generated statements: %w", err)
	}
	positions := make([]logicalLine, len(lines))
	current := logicalLine{file: originalFile, line: 1}
	for index, line := range lines {
		positions[index] = current
		if file, number, column, ok := parseLineDirective(line); ok {
			current = logicalLine{file: file, line: number, column: column}
			continue
		}
		current.line++
	}

	virtualLine, ok := unusedLogicalLineRange(positions, len(lines)+1)
	if !ok {
		return nil, errors.New("no collision-free logical line range remains for generated statements")
	}
	var output strings.Builder
	resetNeeded := false
	activeGeneratedKind := generatedLineNone
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		kind := generatedLines[index+1]
		if kind != generatedLineNone {
			virtualFile := generatedFile
			if kind == generatedLineCompiler {
				virtualFile = compilerFile
			}
			if kind != activeGeneratedKind {
				fmt.Fprintf(&output, "//line %s:%d\n", virtualFile, virtualLine+index)
			}
			// printer.SourcePos may place an original //line directive or comment
			// between the receiver and method name of a positionless generated
			// selector. Keep ordinary comments, but suppress directives inside the
			// generated statement so they cannot remap part of that statement back
			// onto an original coverage obligation.
			if _, _, _, directive := parseLineDirective(line); !directive {
				output.WriteString(line)
			}
			if index != len(lines)-1 {
				output.WriteByte('\n')
			}
			resetNeeded = true
			activeGeneratedKind = kind
			continue
		}
		activeGeneratedKind = generatedLineNone
		if resetNeeded && trimmed != "" && !strings.HasPrefix(trimmed, "//line ") {
			position := positions[index]
			writeLineDirective(&output, position)
			resetNeeded = false
		}
		output.WriteString(line)
		if index != len(lines)-1 {
			output.WriteByte('\n')
		}
	}
	return []byte(output.String()), nil
}

type generatedLineKind uint8

const (
	generatedLineNone generatedLineKind = iota
	generatedLineRuntime
	generatedLineCompiler
)

// generatedStatementLineKinds identifies generated statement ranges from the
// parsed syntax rather than from rendered line prefixes. printer.SourcePos can
// legally split a positionless selector across comments and line directives,
// so textual prefix matching is not a sound generated/original boundary.
func generatedStatementLineKinds(source []byte, helperName string) (map[int]generatedLineKind, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "generated.go", source, parser.ParseComments|parser.AllErrors)
	if err != nil {
		return nil, err
	}
	lines := make(map[int]generatedLineKind)
	var classificationErr error
	ast.Inspect(file, func(node ast.Node) bool {
		if classificationErr != nil {
			return false
		}
		statement, ok := node.(ast.Stmt)
		if !ok {
			return true
		}
		kind := classifyGeneratedStatement(statement, helperName)
		if kind == generatedLineNone {
			return true
		}
		start := fset.PositionFor(statement.Pos(), false).Line
		end := fset.PositionFor(statement.End(), false).Line
		if start <= 0 || end < start {
			classificationErr = fmt.Errorf("invalid generated statement physical range %d-%d", start, end)
			return false
		}
		for line := start; line <= end; line++ {
			if existing := lines[line]; existing != generatedLineNone && existing != kind {
				classificationErr = fmt.Errorf("generated statement line %d has conflicting producer classes", line)
				return false
			}
			lines[line] = kind
		}
		return false
	})
	if classificationErr != nil {
		return nil, classificationErr
	}
	return lines, nil
}

func classifyGeneratedStatement(statement ast.Stmt, helperName string) generatedLineKind {
	switch current := statement.(type) {
	case *ast.DeclStmt:
		declaration, ok := current.Decl.(*ast.GenDecl)
		if !ok || declaration.Tok != token.VAR {
			return generatedLineNone
		}
		for _, raw := range declaration.Specs {
			specification, ok := raw.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range specification.Names {
				if name.Name == helperName+"Slots" || name.Name == helperName+"Switches" {
					return generatedLineRuntime
				}
			}
		}
	case *ast.DeferStmt:
		if method, ok := generatedHelperMethod(current.Call, helperName); ok && method == "AbortSlots" {
			return generatedLineRuntime
		}
	case *ast.ExprStmt:
		call, ok := current.X.(*ast.CallExpr)
		if !ok {
			return generatedLineNone
		}
		method, ok := generatedHelperMethod(call, helperName)
		if !ok {
			return generatedLineNone
		}
		switch method {
		case "SelectClause":
			return generatedLineRuntime
		case "CompilerDirectClause", "CompilerNoMatch":
			return generatedLineCompiler
		}
	case *ast.IfStmt:
		assignment, ok := current.Init.(*ast.AssignStmt)
		if !ok || assignment.Tok != token.DEFINE || len(assignment.Lhs) != 1 {
			return generatedLineNone
		}
		name, ok := assignment.Lhs[0].(*ast.Ident)
		if ok && strings.HasPrefix(name.Name, helperName+"Boundary") {
			return generatedLineRuntime
		}
	}
	return generatedLineNone
}

func generatedHelperMethod(call *ast.CallExpr, helperName string) (string, bool) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	receiver, ok := selector.X.(*ast.Ident)
	if !ok || receiver.Name != helperName {
		return "", false
	}
	return selector.Sel.Name, true
}

type logicalLineKey struct {
	file string
	line int
}

type originalLogicalPositions struct {
	physical        map[int]logicalLine
	columnByLogical map[logicalLineKey]logicalLine
	ambiguousColumn map[logicalLineKey]bool
}

func originalLogicalLines(source []byte, originalFile string) originalLogicalPositions {
	lines := strings.Split(string(source), "\n")
	positions := originalLogicalPositions{
		physical:        make(map[int]logicalLine, len(lines)),
		columnByLogical: make(map[logicalLineKey]logicalLine),
		ambiguousColumn: make(map[logicalLineKey]bool),
	}
	current := logicalLine{file: originalFile, line: 1}
	for index, line := range lines {
		physicalLine := index + 1
		positions.physical[physicalLine] = current
		if current.column > 0 {
			key := logicalLineKey{file: normalizeLogicalFilename(current.file), line: current.line}
			if existing, found := positions.columnByLogical[key]; found && existing.column != current.column {
				positions.ambiguousColumn[key] = true
			} else {
				positions.columnByLogical[key] = current
			}
		}
		if file, number, column, ok := parseLineDirective(line); ok {
			current = logicalLine{file: file, line: number, column: column}
			continue
		}
		current.line++
	}
	return positions
}

func restoreUserLogicalDirective(line, originalFile string, positions originalLogicalPositions) string {
	file, physicalLine, column, ok := parseLineDirective(line)
	if !ok {
		return line
	}
	key := logicalLineKey{file: normalizeLogicalFilename(file), line: physicalLine}
	if column > 0 {
		if logical, found := positions.columnByLogical[key]; found && !positions.ambiguousColumn[key] && logical.column == column {
			return line
		}
	}
	// printer.SourcePos can omit a user directive's column. Restore it from an
	// exact logical filename+line only when the original source establishes one
	// unique positive column. This check precedes physical-line restoration so
	// a user virtual filename equal to the original basename is not mistaken for
	// a printer reference to the unchanged file.
	if column == 0 {
		if logical, found := positions.columnByLogical[key]; found && !positions.ambiguousColumn[key] {
			var restored strings.Builder
			writeLineDirective(&restored, logical)
			return strings.TrimSuffix(restored.String(), "\n")
		}
	}
	if !sameOriginalDirectiveFile(file, originalFile) {
		return line
	}
	logical, found := positions.physical[physicalLine]
	if !found {
		return line
	}
	var restored strings.Builder
	writeLineDirective(&restored, logical)
	return strings.TrimSuffix(restored.String(), "\n")
}

func normalizeLogicalFilename(filename string) string {
	return filepath.ToSlash(filepath.Clean(filename))
}

func sameOriginalDirectiveFile(left, right string) bool {
	left = normalizeLogicalFilename(left)
	right = normalizeLogicalFilename(right)
	return left == right || filepath.Base(left) == filepath.Base(right)
}

func writeLineDirective(output *strings.Builder, position logicalLine) {
	if position.column > 0 {
		fmt.Fprintf(output, "//line %s:%d:%d\n", position.file, position.line, position.column)
		return
	}
	fmt.Fprintf(output, "//line %s:%d\n", position.file, position.line)
}

// normalizeOriginalLineDirective keeps printer.SourcePos's stable
// module-relative identity from being resolved a second time against the
// copied source directory by the compiler and parser.
func normalizeOriginalLineDirective(line, originalFile string) string {
	normalized := filepath.ToSlash(originalFile)
	base := filepath.ToSlash(filepath.Base(normalized))
	if normalized == "" || normalized == "." || normalized == base {
		return line
	}
	trimmed := strings.TrimSpace(line)
	prefix := "//line " + normalized
	if !strings.HasPrefix(trimmed, prefix+":") {
		return line
	}
	return "//line " + base + strings.TrimPrefix(trimmed, prefix)
}

func unusedLogicalLineRange(positions []logicalLine, width int) (int, bool) {
	if width <= 0 {
		return 0, false
	}
	usedSet := make(map[int]struct{}, len(positions))
	for _, position := range positions {
		if position.line > 0 {
			usedSet[position.line] = struct{}{}
		}
	}
	used := make([]int, 0, len(usedSet))
	for line := range usedSet {
		used = append(used, line)
	}
	sort.Ints(used)
	maximum := int(^uint(0) >> 1)
	start := 1
	for _, line := range used {
		if line < start {
			continue
		}
		if line-start >= width {
			return start, true
		}
		if line == maximum {
			return 0, false
		}
		start = line + 1
	}
	if start > maximum-width+1 {
		return 0, false
	}
	return start, true
}

func parseLineDirective(line string) (string, int, int, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "//line ") {
		return "", 0, 0, false
	}
	value := strings.TrimPrefix(trimmed, "//line ")
	separator := strings.LastIndexByte(value, ':')
	if separator <= 0 || separator == len(value)-1 {
		return "", 0, 0, false
	}
	number, err := strconv.Atoi(value[separator+1:])
	if err != nil || number <= 0 {
		return "", 0, 0, false
	}
	file := value[:separator]
	if previous := strings.LastIndexByte(file, ':'); previous > 0 {
		if lineNumber, lineErr := strconv.Atoi(file[previous+1:]); lineErr == nil && lineNumber > 0 {
			// filename:line:column -- the last numeric component is the
			// column, while logical line tracking uses the penultimate one.
			return file[:previous], lineNumber, number, true
		}
	}
	return file, number, 0, true
}

type fileTransformer struct {
	fset              *token.FileSet
	helperName        string
	decisions         map[decisionLocation]analyzer.Decision
	clauses           map[clauseLocation]analyzer.Clause
	noMatches         map[noMatchLocation]cover.NoMatchMetadata
	matchedDec        map[decisionLocation]bool
	matchedCla        map[cover.ClauseID]bool
	matchedNoMatch    map[cover.SwitchID]bool
	funcs             map[*ast.BlockStmt]bool
	originalFile      string
	compilerSelection bool
	generatedRegions  []GeneratedRegion
	reservedNames     map[string]struct{}
	boundaryCount     int
}

type functionState struct {
	transformer *fileTransformer
	slotsName   string
	slotCount   int
}

func newFileTransformer(fset *token.FileSet, helperName string, analysis analyzer.File, compilerClauseSelection bool) (*fileTransformer, error) {
	transformer := &fileTransformer{
		fset:              fset,
		helperName:        helperName,
		decisions:         make(map[decisionLocation]analyzer.Decision, len(analysis.Decisions)),
		clauses:           make(map[clauseLocation]analyzer.Clause, len(analysis.Clauses)),
		noMatches:         make(map[noMatchLocation]cover.NoMatchMetadata, len(analysis.NoMatches)),
		matchedDec:        make(map[decisionLocation]bool, len(analysis.Decisions)),
		matchedCla:        make(map[cover.ClauseID]bool, len(analysis.Clauses)),
		matchedNoMatch:    make(map[cover.SwitchID]bool, len(analysis.NoMatches)),
		funcs:             make(map[*ast.BlockStmt]bool),
		originalFile:      analysis.RelativePath,
		compilerSelection: compilerClauseSelection,
		reservedNames:     make(map[string]struct{}, len(analysis.Identifiers)),
	}
	for _, identifier := range analysis.Identifiers {
		transformer.reservedNames[identifier] = struct{}{}
	}
	for _, decision := range analysis.Decisions {
		key := decisionLocation{kind: decision.Metadata.Kind, start: decision.Condition.Start, end: decision.Condition.End}
		if _, exists := transformer.decisions[key]; exists {
			return nil, fmt.Errorf("duplicate analyzed decision at bytes %d:%d", key.start, key.end)
		}
		transformer.decisions[key] = decision
	}
	for _, clause := range analysis.Clauses {
		key := clauseLocation{kind: clause.Metadata.Kind, role: clause.Metadata.Role, start: clause.Span.Start, end: clause.Span.End}
		if _, exists := transformer.clauses[key]; exists {
			return nil, fmt.Errorf("duplicate analyzed %s clause at bytes %d:%d", clause.Metadata.Role, key.start, key.end)
		}
		transformer.clauses[key] = clause
	}
	for _, noMatch := range analysis.NoMatches {
		key := noMatchLocation{kind: noMatch.Kind, start: noMatch.Location.StartOffset, end: noMatch.Location.EndOffset}
		if _, exists := transformer.noMatches[key]; exists {
			return nil, fmt.Errorf("duplicate analyzed %s no-match at bytes %d:%d", noMatch.Kind, key.start, key.end)
		}
		transformer.noMatches[key] = noMatch
	}
	return transformer, nil
}

func (transformer *fileTransformer) transform(file *ast.File) error {
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Body != nil {
			if err := transformer.transformFunction(function.Body); err != nil {
				return err
			}
		}
	}
	var literalErr error
	ast.Inspect(file, func(node ast.Node) bool {
		if literalErr != nil {
			return false
		}
		literal, ok := node.(*ast.FuncLit)
		if !ok {
			return true
		}
		if err := transformer.transformFunction(literal.Body); err != nil {
			literalErr = err
		}
		return false
	})
	if literalErr != nil {
		return literalErr
	}
	if len(transformer.matchedDec) != len(transformer.decisions) {
		return fmt.Errorf("matched %d of %d analyzed decisions", len(transformer.matchedDec), len(transformer.decisions))
	}
	if len(transformer.matchedCla) != len(transformer.clauses) {
		return fmt.Errorf("matched %d of %d analyzed clauses", len(transformer.matchedCla), len(transformer.clauses))
	}
	if transformer.compilerSelection && len(transformer.matchedNoMatch) != len(transformer.noMatches) {
		return fmt.Errorf("matched %d of %d analyzed no-match switches", len(transformer.matchedNoMatch), len(transformer.noMatches))
	}
	return nil
}

func (transformer *fileTransformer) transformFunction(body *ast.BlockStmt) error {
	if transformer.funcs[body] {
		return nil
	}
	transformer.funcs[body] = true
	state := &functionState{
		transformer: transformer,
		slotsName:   transformer.helperName + "Slots",
	}
	statements, err := state.transformStatements(body.List)
	if err != nil {
		return err
	}
	body.List = statements
	var prologue []ast.Stmt
	if state.slotCount > 0 {
		prologue = append(prologue,
			arrayDeclaration(state.slotsName, state.slotCount, transformer.helperName+"EvaluationID"),
			&ast.DeferStmt{Call: state.call("AbortSlots", sliceExpression(state.slotsName))},
		)
		prologue = append(prologue, transformer.coverageBoundary(body.Lbrace)...)
		transformer.recordGenerated("evaluation-prologue", body, uint32(len(prologue)))
	}
	body.List = append(prologue, body.List...)
	var nestedErr error
	ast.Inspect(body, func(node ast.Node) bool {
		if nestedErr != nil {
			return false
		}
		literal, ok := node.(*ast.FuncLit)
		if !ok {
			return true
		}
		if err := transformer.transformFunction(literal.Body); err != nil {
			nestedErr = err
		}
		return false
	})
	return nestedErr
}

func (state *functionState) transformStatements(statements []ast.Stmt) ([]ast.Stmt, error) {
	result := make([]ast.Stmt, 0, len(statements)+2)
	for _, statement := range statements {
		transformed, err := state.transformStatement(statement)
		if err != nil {
			return nil, err
		}
		result = append(result, transformed...)
	}
	return result, nil
}

func (state *functionState) transformStatement(statement ast.Stmt) ([]ast.Stmt, error) {
	switch current := statement.(type) {
	case *ast.BlockStmt:
		list, err := state.transformStatements(current.List)
		current.List = list
		return []ast.Stmt{current}, err
	case *ast.IfStmt:
		wrapped, err := state.wrapDecision(current.Cond, cover.DecisionIf, nil)
		if err != nil {
			return nil, err
		}
		current.Cond = wrapped
		if transformed, err := state.transformStatements(current.Body.List); err != nil {
			return nil, err
		} else {
			current.Body.List = transformed
		}
		if current.Else != nil {
			transformed, err := state.transformStatement(current.Else)
			if err != nil {
				return nil, err
			}
			if len(transformed) != 1 {
				return nil, errors.New("else statement expanded to more than one statement")
			}
			current.Else = transformed[0]
		}
		return []ast.Stmt{current}, nil
	case *ast.ForStmt:
		if current.Cond != nil {
			wrapped, err := state.wrapDecision(current.Cond, cover.DecisionFor, nil)
			if err != nil {
				return nil, err
			}
			current.Cond = wrapped
		}
		transformed, err := state.transformStatements(current.Body.List)
		current.Body.List = transformed
		return []ast.Stmt{current}, err
	case *ast.RangeStmt:
		transformed, err := state.transformStatements(current.Body.List)
		current.Body.List = transformed
		return []ast.Stmt{current}, err
	case *ast.SwitchStmt:
		return state.transformSwitch(current)
	case *ast.TypeSwitchStmt:
		return state.transformTypeSwitch(current)
	case *ast.SelectStmt:
		return state.transformSelect(current)
	case *ast.LabeledStmt:
		transformed, err := state.transformStatement(current.Stmt)
		if err != nil {
			return nil, err
		}
		// The instrumented switch remains the original statement. Keeping the
		// label on it preserves both goto Label and labeled break semantics.
		current.Stmt = transformed[0]
		transformed[0] = current
		return transformed, nil
	default:
		return []ast.Stmt{statement}, nil
	}
}

func (state *functionState) transformSwitch(statement *ast.SwitchStmt) ([]ast.Stmt, error) {
	kind := cover.ClauseExpressionSwitch
	if statement.Tag == nil {
		kind = cover.ClauseConditionlessSwitch
	}
	return state.transformCaseClauses(kind, statement, statement.Body.List)
}

func (state *functionState) transformTypeSwitch(statement *ast.TypeSwitchStmt) ([]ast.Stmt, error) {
	return state.transformCaseClauses(cover.ClauseTypeSwitch, statement, statement.Body.List)
}

func (state *functionState) transformCaseClauses(kind cover.ClauseKind, statement ast.Stmt, rawClauses []ast.Stmt) ([]ast.Stmt, error) {
	var remainingDecisions []cover.DecisionID
	if kind == cover.ClauseConditionlessSwitch {
		for _, raw := range rawClauses {
			clause, ok := raw.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, expression := range clause.List {
				start := state.transformer.fset.PositionFor(expression.Pos(), false).Offset
				end := state.transformer.fset.PositionFor(expression.End(), false).Offset
				decision, exists := state.transformer.decisions[decisionLocation{kind: cover.DecisionSwitchCase, start: start, end: end}]
				if !exists {
					return nil, fmt.Errorf("conditionless switch decision at bytes %d:%d was not present in analysis", start, end)
				}
				remainingDecisions = append(remainingDecisions, decision.Metadata.ID)
			}
		}
	}
	for _, raw := range rawClauses {
		clause, ok := raw.(*ast.CaseClause)
		if !ok {
			continue
		}
		role := cover.ClauseCase
		if clause.List == nil {
			role = cover.ClauseDefault
		}
		metadata, err := state.transformer.matchClause(kind, role, clause)
		if err != nil {
			return nil, err
		}
		if kind == cover.ClauseConditionlessSwitch && clause.List != nil {
			for index, expression := range clause.List {
				if len(remainingDecisions) == 0 {
					return nil, errors.New("conditionless switch decision order was exhausted")
				}
				remainingDecisions = remainingDecisions[1:]
				wrapped, err := state.wrapDecision(expression, cover.DecisionSwitchCase, &selectionTarget{
					Skipped: append([]cover.DecisionID(nil), remainingDecisions...),
				})
				if err != nil {
					return nil, err
				}
				clause.List[index] = wrapped
			}
		}
		body, err := state.transformStatements(clause.Body)
		if err != nil {
			return nil, err
		}
		entry := &ast.ExprStmt{X: state.call("SelectClause", idLiteral(uint64(metadata.ID)), idLiteral(uint64(metadata.SwitchID)))}
		probes := []ast.Stmt{entry}
		if state.transformer.compilerSelection && (kind == cover.ClauseExpressionSwitch || kind == cover.ClauseTypeSwitch) {
			alternative := uint64(0)
			if role == cover.ClauseDefault {
				alternative = ^uint64(0)
			}
			direct := &ast.ExprStmt{X: state.call(
				"CompilerDirectClause",
				idLiteral(uint64(metadata.ID)),
				idLiteral(uint64(metadata.SwitchID)),
				idLiteral(alternative),
			)}
			probes = append([]ast.Stmt{direct}, probes...)
		}
		if len(body) > 0 {
			probes = append(probes, state.transformer.coverageBoundary(clause.Colon)...)
		}
		state.transformer.recordGenerated("switch-clause-probe", clause, uint32(len(probes)))
		clause.Body = append(probes, body...)
	}
	if state.transformer.compilerSelection && (kind == cover.ClauseExpressionSwitch || kind == cover.ClauseTypeSwitch) {
		noMatch, found, err := state.transformer.matchNoMatch(kind, statement)
		if err != nil {
			return nil, err
		}
		if found {
			marker := &ast.ExprStmt{X: state.call("CompilerNoMatch", idLiteral(uint64(noMatch.SwitchID)))}
			position := statement.End() - 1
			synthetic := &ast.CaseClause{Case: position, Colon: position, Body: []ast.Stmt{marker}}
			switch current := statement.(type) {
			case *ast.SwitchStmt:
				current.Body.List = append(current.Body.List, synthetic)
			case *ast.TypeSwitchStmt:
				current.Body.List = append(current.Body.List, synthetic)
			default:
				return nil, fmt.Errorf("cannot append no-match marker to %T", statement)
			}
			state.transformer.recordGenerated("switch-no-match-probe", statement, 1)
		}
	}
	return []ast.Stmt{statement}, nil
}

func (state *functionState) transformSelect(statement *ast.SelectStmt) ([]ast.Stmt, error) {
	for _, raw := range statement.Body.List {
		clause, ok := raw.(*ast.CommClause)
		if !ok {
			continue
		}
		role := cover.ClauseCase
		if clause.Comm == nil {
			role = cover.ClauseDefault
		}
		metadata, err := state.transformer.matchClause(cover.ClauseSelect, role, clause)
		if err != nil {
			return nil, err
		}
		body, err := state.transformStatements(clause.Body)
		if err != nil {
			return nil, err
		}
		probes := []ast.Stmt{&ast.ExprStmt{X: state.call("SelectClause", idLiteral(uint64(metadata.ID)), idLiteral(uint64(metadata.SwitchID)))}}
		if len(body) > 0 {
			probes = append(probes, state.transformer.coverageBoundary(clause.Colon)...)
		}
		clause.Body = append(probes, body...)
		state.transformer.recordGenerated("select-clause-probe", clause, uint32(len(probes)))
	}
	return []ast.Stmt{statement}, nil
}

type selectionTarget struct {
	Skipped []cover.DecisionID
}

func (state *functionState) wrapDecision(expression ast.Expr, kind cover.DecisionKind, selection *selectionTarget) (ast.Expr, error) {
	start := state.transformer.fset.PositionFor(expression.Pos(), false).Offset
	end := state.transformer.fset.PositionFor(expression.End(), false).Offset
	key := decisionLocation{kind: kind, start: start, end: end}
	decision, exists := state.transformer.decisions[key]
	if !exists {
		return nil, fmt.Errorf("source decision %s at bytes %d:%d was not present in analysis", kind, start, end)
	}
	if state.transformer.matchedDec[key] {
		return nil, fmt.Errorf("source decision %s at bytes %d:%d matched more than once", kind, start, end)
	}
	state.transformer.matchedDec[key] = true
	slotIndex := state.slotCount
	state.slotCount++
	slot := state.slotExpression(slotIndex)
	conditions, err := state.wrapAtoms(expression, decision, slot)
	if err != nil {
		return nil, err
	}
	begin := state.call("BeginInto", address(slot), idLiteral(uint64(decision.Metadata.ID)), intLiteral(len(decision.Conditions)))
	endArgs := []ast.Expr{slot, conditions}
	endMethod := "End"
	if selection != nil {
		endMethod = "EndSelect"
		for _, skipped := range selection.Skipped {
			endArgs = append(endArgs, idLiteral(uint64(skipped)))
		}
	}
	return &ast.BinaryExpr{X: begin, Op: token.LAND, Y: state.call(endMethod, endArgs...)}, nil
}

func (state *functionState) wrapAtoms(expression ast.Expr, decision analyzer.Decision, slot ast.Expr) (ast.Expr, error) {
	bySpan := make(map[[2]int]analyzer.Condition, len(decision.Conditions))
	for _, condition := range decision.Conditions {
		bySpan[[2]int{condition.Span.Start, condition.Span.End}] = condition
	}
	matched := make(map[uint16]bool, len(decision.Conditions))
	var rewrite func(ast.Expr) (ast.Expr, error)
	rewrite = func(current ast.Expr) (ast.Expr, error) {
		start := state.transformer.fset.PositionFor(current.Pos(), false).Offset
		end := state.transformer.fset.PositionFor(current.End(), false).Offset
		if condition, ok := bySpan[[2]int{start, end}]; ok {
			matched[condition.Metadata.Index] = true
			return state.call("Condition", slot, intLiteral(int(condition.Metadata.Index)), normalizeBool(current)), nil
		}
		switch node := current.(type) {
		case *ast.ParenExpr:
			inner, err := rewrite(node.X)
			node.X = inner
			return node, err
		case *ast.BinaryExpr:
			if node.Op != token.LAND && node.Op != token.LOR {
				return nil, fmt.Errorf("unmatched atomic condition at bytes %d:%d", start, end)
			}
			left, err := rewrite(node.X)
			if err != nil {
				return nil, err
			}
			right, err := rewrite(node.Y)
			node.X, node.Y = left, right
			return node, err
		case *ast.UnaryExpr:
			if node.Op != token.NOT {
				return nil, fmt.Errorf("unmatched atomic condition at bytes %d:%d", start, end)
			}
			inner, err := rewrite(node.X)
			node.X = inner
			return node, err
		default:
			return nil, fmt.Errorf("unmatched atomic condition at bytes %d:%d", start, end)
		}
	}
	rewritten, err := rewrite(expression)
	if err != nil {
		return nil, err
	}
	if len(matched) != len(decision.Conditions) {
		return nil, fmt.Errorf("matched %d of %d atomic conditions for decision 0x%016x", len(matched), len(decision.Conditions), uint64(decision.Metadata.ID))
	}
	return rewritten, nil
}

func (transformer *fileTransformer) matchClause(kind cover.ClauseKind, role cover.ClauseRole, node ast.Node) (cover.ClauseMetadata, error) {
	start := transformer.fset.PositionFor(node.Pos(), false).Offset
	end := transformer.fset.PositionFor(node.End(), false).Offset
	key := clauseLocation{kind: kind, role: role, start: start, end: end}
	clause, ok := transformer.clauses[key]
	if !ok {
		return cover.ClauseMetadata{}, fmt.Errorf("source %s %s clause at bytes %d:%d was not present in analysis", kind, role, start, end)
	}
	transformer.matchedCla[clause.Metadata.ID] = true
	return clause.Metadata, nil
}

func (transformer *fileTransformer) matchNoMatch(kind cover.ClauseKind, node ast.Node) (cover.NoMatchMetadata, bool, error) {
	start := transformer.fset.PositionFor(node.Pos(), false).Offset
	end := transformer.fset.PositionFor(node.End(), false).Offset
	key := noMatchLocation{kind: kind, start: start, end: end}
	noMatch, found := transformer.noMatches[key]
	if !found {
		return cover.NoMatchMetadata{}, false, nil
	}
	if transformer.matchedNoMatch[noMatch.SwitchID] {
		return cover.NoMatchMetadata{}, false, fmt.Errorf("source %s no-match switch at bytes %d:%d matched more than once", kind, start, end)
	}
	transformer.matchedNoMatch[noMatch.SwitchID] = true
	return noMatch, true, nil
}

func (transformer *fileTransformer) recordGenerated(kind string, anchor ast.Node, count uint32) {
	start := transformer.fset.PositionFor(anchor.Pos(), false)
	end := transformer.fset.PositionFor(anchor.End(), false)
	transformer.generatedRegions = append(transformer.generatedRegions, GeneratedRegion{
		Kind: kind,
		Anchor: cover.SourceLocation{
			File:  filepath.ToSlash(transformer.originalFile),
			Start: cover.Position{Line: start.Line, Column: start.Column},
			End:   cover.Position{Line: end.Line, Column: end.Column},
		},
		StatementCount: count,
	})
}

func (state *functionState) call(method string, arguments ...ast.Expr) *ast.CallExpr {
	return &ast.CallExpr{Fun: &ast.SelectorExpr{X: ast.NewIdent(state.transformer.helperName), Sel: ast.NewIdent(method)}, Args: arguments}
}

func (state *functionState) slotExpression(index int) ast.Expr {
	return &ast.IndexExpr{X: ast.NewIdent(state.slotsName), Index: intLiteral(index)}
}

func normalizeBool(expression ast.Expr) ast.Expr {
	truth := &ast.ParenExpr{X: &ast.BinaryExpr{X: intLiteral(0), Op: token.EQL, Y: intLiteral(0)}}
	return &ast.BinaryExpr{X: &ast.ParenExpr{X: expression}, Op: token.EQL, Y: truth}
}

func address(expression ast.Expr) ast.Expr {
	return &ast.UnaryExpr{Op: token.AND, X: expression}
}

func intLiteral(value int) *ast.BasicLit {
	return &ast.BasicLit{Kind: token.INT, Value: strconv.Itoa(value)}
}

func idLiteral(value uint64) *ast.BasicLit {
	return &ast.BasicLit{Kind: token.INT, Value: fmt.Sprintf("0x%016x", value)}
}

func arrayDeclaration(name string, length int, element string) ast.Stmt {
	return &ast.DeclStmt{Decl: &ast.GenDecl{Tok: token.VAR, Specs: []ast.Spec{&ast.ValueSpec{
		Names: []*ast.Ident{ast.NewIdent(name)},
		Type:  &ast.ArrayType{Len: intLiteral(length), Elt: ast.NewIdent(element)},
	}}}}
}

func sliceExpression(name string) ast.Expr {
	return &ast.SliceExpr{X: ast.NewIdent(name)}
}

func (transformer *fileTransformer) coverageBoundary(position token.Pos) []ast.Stmt {
	for {
		name := transformer.helperName + "Boundary" + strconv.Itoa(transformer.boundaryCount)
		transformer.boundaryCount++
		if _, exists := transformer.reservedNames[name]; exists {
			continue
		}
		transformer.reservedNames[name] = struct{}{}
		return []ast.Stmt{
			&ast.IfStmt{
				If: position,
				Init: &ast.AssignStmt{
					Lhs:    []ast.Expr{ast.NewIdent(name)},
					TokPos: position,
					Tok:    token.DEFINE,
					Rhs: []ast.Expr{&ast.BinaryExpr{
						X: intLiteral(0), Op: token.NEQ, Y: intLiteral(0),
					}},
				},
				Cond: ast.NewIdent(name),
				Body: &ast.BlockStmt{Lbrace: position, Rbrace: position},
			},
		}
	}
}

func ensureDistinctCopy(copyPath, originalPath string) error {
	copyAbsolute, err := filepath.Abs(copyPath)
	if err != nil {
		return fmt.Errorf("resolve copy path: %w", err)
	}
	originalAbsolute, err := filepath.Abs(originalPath)
	if err != nil {
		return fmt.Errorf("resolve original path: %w", err)
	}
	if copyAbsolute == originalAbsolute {
		return errors.New("refusing to instrument the original source file")
	}
	copyInfo, copyErr := os.Stat(copyAbsolute)
	originalInfo, originalErr := os.Stat(originalAbsolute)
	if copyErr == nil && originalErr == nil && os.SameFile(copyInfo, originalInfo) {
		return errors.New("refusing to instrument a symlink or hardlink to the original source file")
	}
	return nil
}

func replaceFile(path string, contents []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".gomcdc-rewrite-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	removeTemporary = false
	return nil
}
