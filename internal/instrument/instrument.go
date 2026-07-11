// Package instrument rewrites copied Go files for AST coverage probes.
// It never writes an analyzer.File's original source path.
package instrument

import (
	"bytes"
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
	cover "github.com/shrydev2020/gomcdc/internal/coverage"
)

const (
	defaultHelperBase      = "__gocoverageEvalDecision"
	defaultRuntimeFunction = "EvalDecision"
)

type decisionLocation struct {
	kind       cover.DecisionKind
	start, end int
}

type clauseLocation struct {
	kind       cover.ClauseKind
	role       cover.ClauseRole
	start, end int
}

// FileMapping maps analysis of an original file to its unchanged copied path.
type FileMapping struct {
	CopyPath string
	Analysis analyzer.File
}

// PackageOptions describes one copied package to instrument. ActiveFiles must
// include every build-active source and test file whose identifiers may collide
// with the generated package-wide helper.
type PackageOptions struct {
	Directory           string
	PackageName         string
	PackagePath         string
	RuntimeImportPath   string
	RuntimeFunctionName string
	TestOnly            bool
	ActiveFiles         []string
	Files               []FileMapping
}

// PackageResult identifies the generated bridge and helper used by rewritten
// files.
type PackageResult struct {
	HelperName     string
	BridgePath     string
	GeneratedFiles []string
	SourceMaps     []SourceMap
}

// SourceMap identifies the original logical filename used by printer.SourcePos,
// the virtual filename requested for synthetic statements, and the manifest
// needed when Go cover retains the physical filename despite //line directives.
type SourceMap struct {
	InstrumentedFile string
	OriginalFile     string
	GeneratedFile    string
	GeneratedRegions []GeneratedRegion
	LineMappings     []analyzer.LineMapping
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
	if options.Directory == "" {
		return PackageResult{}, errors.New("instrument package: directory is empty")
	}
	if !token.IsIdentifier(options.PackageName) {
		return PackageResult{}, fmt.Errorf("instrument package: invalid package name %q", options.PackageName)
	}
	if options.RuntimeImportPath == "" {
		return PackageResult{}, errors.New("instrument package: runtime import path is empty")
	}
	runtimeFunction := options.RuntimeFunctionName
	if runtimeFunction == "" {
		runtimeFunction = defaultRuntimeFunction
	}
	if !token.IsIdentifier(runtimeFunction) {
		return PackageResult{}, fmt.Errorf("instrument package: invalid runtime function name %q", runtimeFunction)
	}

	activeFiles := append([]string(nil), options.ActiveFiles...)
	if len(activeFiles) == 0 {
		for _, mapping := range options.Files {
			activeFiles = append(activeFiles, mapping.CopyPath)
		}
	}
	helperName, err := SelectHelperName(activeFiles)
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
	for _, mapping := range options.Files {
		if len(mapping.Analysis.Decisions) == 0 && len(mapping.Analysis.Clauses) == 0 {
			continue
		}
		data, mode, sourceMap, err := transformCopiedFile(mapping.CopyPath, mapping.Analysis, helperName)
		if err != nil {
			return PackageResult{}, err
		}
		transformed = append(transformed, transformedFile{path: mapping.CopyPath, mode: mode, data: data, map_: sourceMap})
	}
	for _, file := range transformed {
		if err := replaceFile(file.path, file.data, file.mode); err != nil {
			return PackageResult{}, fmt.Errorf("instrument copied file %q: write: %w", file.path, err)
		}
	}

	packagePath := options.PackagePath
	if packagePath == "" {
		for _, mapping := range options.Files {
			if len(mapping.Analysis.Decisions) != 0 {
				packagePath = mapping.Analysis.Decisions[0].Metadata.Package
				break
			}
			if len(mapping.Analysis.Clauses) != 0 {
				packagePath = mapping.Analysis.Clauses[0].Metadata.Package
				break
			}
		}
	}
	bridgePath, err := WriteBridge(BridgeOptions{
		Directory:           options.Directory,
		PackageName:         options.PackageName,
		PackagePath:         packagePath,
		RuntimeImportPath:   options.RuntimeImportPath,
		RuntimeFunctionName: runtimeFunction,
		HelperName:          helperName,
		TestOnly:            options.TestOnly,
	})
	if err != nil {
		return PackageResult{}, err
	}
	result := PackageResult{HelperName: helperName, BridgePath: bridgePath, GeneratedFiles: []string{bridgePath}}
	for _, file := range transformed {
		result.SourceMaps = append(result.SourceMaps, file.map_)
	}
	return result, nil
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
	data, mode, _, err := transformCopiedFile(copyPath, analysis, helperName)
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
func SelectHelperName(activeFiles []string) (string, error) {
	used := make(map[string]struct{})
	paths := append([]string(nil), activeFiles...)
	sort.Strings(paths)
	for _, path := range paths {
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
	Directory           string
	PackageName         string
	PackagePath         string
	RuntimeImportPath   string
	RuntimeFunctionName string
	HelperName          string
	TestOnly            bool
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
	if !token.IsIdentifier(options.RuntimeFunctionName) {
		return "", fmt.Errorf("write bridge: invalid runtime function name %q", options.RuntimeFunctionName)
	}
	if !token.IsIdentifier(options.HelperName) {
		return "", fmt.Errorf("write bridge: invalid helper name %q", options.HelperName)
	}

	var source string
	if options.PackagePath == "" {
		source = fmt.Sprintf(`// Code generated by gocoverage. DO NOT EDIT.

package %s

import __gocoverageRuntime %s

var %s = __gocoverageRuntime.%s
`, options.PackageName, strconv.Quote(options.RuntimeImportPath), options.HelperName, options.RuntimeFunctionName)
	} else {
		source = fmt.Sprintf(`// Code generated by gocoverage. DO NOT EDIT.

package %s

import %sRuntime %s

var %s = %sRuntime.NewHooks(%s)

type %sEvaluationID = %sRuntime.EvaluationID
`, options.PackageName,
			options.HelperName, strconv.Quote(options.RuntimeImportPath),
			options.HelperName, options.HelperName, strconv.Quote(options.PackagePath),
			options.HelperName, options.HelperName,
		)
	}
	formatted, err := format.Source([]byte(source))
	if err != nil {
		return "", fmt.Errorf("write bridge: format generated source: %w", err)
	}

	suffix := strings.TrimPrefix(options.HelperName, defaultHelperBase)
	suffix = strings.TrimPrefix(suffix, "_")
	if suffix == "" {
		suffix = "0"
	}
	baseName := "zz_gocoverage_bridge_" + suffix
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

func transformCopiedFile(copyPath string, analysis analyzer.File, helperName string) ([]byte, os.FileMode, SourceMap, error) {
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
	transformer, err := newFileTransformer(fset, helperName, analysis)
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
		GeneratedFile:    filepath.ToSlash(filepath.Join(".gocoverage", "generated", fmt.Sprintf("%x.go", generatedIdentity[:12]))),
		GeneratedRegions: append([]GeneratedRegion(nil), transformer.generatedRegions...),
		LineMappings:     append([]analyzer.LineMapping(nil), analysis.LineMappings...),
	}
	formatted := mapGeneratedStatements(output.Bytes(), helperName, sourceMap.OriginalFile, sourceMap.GeneratedFile)
	if _, err := parser.ParseFile(token.NewFileSet(), copyPath, formatted, parser.ParseComments|parser.AllErrors); err != nil {
		return nil, 0, SourceMap{}, fmt.Errorf("instrument copied file %q: validate transformed source: %w", copyPath, err)
	}
	return append([]byte(nil), formatted...), info.Mode().Perm(), sourceMap, nil
}

type logicalLine struct {
	file string
	line int
}

// mapGeneratedStatements requests a virtual filename for every inserted
// statement while retaining printer.SourcePos's logical line for the next
// original line. Some Go cover versions retain the physical filename anyway;
// SourceMap.GeneratedRegions is the authoritative exclusion fallback.
func mapGeneratedStatements(source []byte, helperName, originalFile, generatedFile string) []byte {
	lines := strings.Split(string(source), "\n")
	positions := make([]logicalLine, len(lines))
	current := logicalLine{file: originalFile, line: 1}
	for index, line := range lines {
		positions[index] = current
		if file, number, ok := parseLineDirective(line); ok {
			current = logicalLine{file: file, line: number}
			continue
		}
		current.line++
	}

	var output strings.Builder
	virtualLine := 1
	resetNeeded := false
	for index, line := range lines {
		if generatedStatementLine(line, helperName) {
			fmt.Fprintf(&output, "//line %s:%d\n", generatedFile, virtualLine)
			output.WriteString(line)
			output.WriteByte('\n')
			virtualLine++
			resetNeeded = true
			continue
		}
		if resetNeeded && strings.TrimSpace(line) != "" && !strings.HasPrefix(strings.TrimSpace(line), "//line ") {
			position := positions[index]
			fmt.Fprintf(&output, "//line %s:%d\n", position.file, position.line)
			resetNeeded = false
		}
		output.WriteString(line)
		if index != len(lines)-1 {
			output.WriteByte('\n')
		}
	}
	return []byte(output.String())
}

func parseLineDirective(line string) (string, int, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "//line ") {
		return "", 0, false
	}
	value := strings.TrimPrefix(trimmed, "//line ")
	separator := strings.LastIndexByte(value, ':')
	if separator <= 0 || separator == len(value)-1 {
		return "", 0, false
	}
	number, err := strconv.Atoi(value[separator+1:])
	if err != nil || number <= 0 {
		return "", 0, false
	}
	file := value[:separator]
	if previous := strings.LastIndexByte(file, ':'); previous > 0 {
		if lineNumber, lineErr := strconv.Atoi(file[previous+1:]); lineErr == nil && lineNumber > 0 {
			// filename:line:column -- the last numeric component is the
			// column, while logical line tracking uses the penultimate one.
			return file[:previous], lineNumber, true
		}
	}
	return file, number, true
}

func generatedStatementLine(line, helperName string) bool {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "var "+helperName+"Slots ") ||
		strings.HasPrefix(trimmed, "var "+helperName+"Switches ") ||
		strings.HasPrefix(trimmed, "defer "+helperName+".AbortSlots(") {
		return true
	}
	for _, method := range []string{"SelectClause"} {
		if strings.HasPrefix(trimmed, helperName+"."+method+"(") {
			return true
		}
	}
	return false
}

type fileTransformer struct {
	fset             *token.FileSet
	helperName       string
	decisions        map[decisionLocation]analyzer.Decision
	clauses          map[clauseLocation]analyzer.Clause
	matchedDec       map[decisionLocation]bool
	matchedCla       map[cover.ClauseID]bool
	funcs            map[*ast.BlockStmt]bool
	originalFile     string
	generatedRegions []GeneratedRegion
}

type functionState struct {
	transformer *fileTransformer
	slotsName   string
	slotCount   int
}

func newFileTransformer(fset *token.FileSet, helperName string, analysis analyzer.File) (*fileTransformer, error) {
	transformer := &fileTransformer{
		fset:         fset,
		helperName:   helperName,
		decisions:    make(map[decisionLocation]analyzer.Decision, len(analysis.Decisions)),
		clauses:      make(map[clauseLocation]analyzer.Clause, len(analysis.Clauses)),
		matchedDec:   make(map[decisionLocation]bool, len(analysis.Decisions)),
		matchedCla:   make(map[cover.ClauseID]bool, len(analysis.Clauses)),
		funcs:        make(map[*ast.BlockStmt]bool),
		originalFile: analysis.RelativePath,
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
		transformer.recordGenerated("evaluation-prologue", body, 2)
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
		entry := &ast.ExprStmt{X: state.call("SelectClause", idLiteral(uint64(metadata.ID)))}
		state.transformer.recordGenerated("switch-clause-probe", clause, 1)
		clause.Body = append([]ast.Stmt{entry}, body...)
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
		clause.Body = append([]ast.Stmt{&ast.ExprStmt{X: state.call("SelectClause", idLiteral(uint64(metadata.ID)))}}, body...)
		state.transformer.recordGenerated("select-clause-probe", clause, 1)
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
	temporary, err := os.CreateTemp(directory, ".gocoverage-rewrite-*")
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
