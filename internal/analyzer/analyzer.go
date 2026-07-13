// Package analyzer discovers boolean decisions, their atomic conditions, and
// selectable clauses in original Go source files. IDs are deterministic for
// one source revision and never depend on traversal or package load order.
package analyzer

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	cover "github.com/shrydev2020/gomcdc/internal/coverage"
)

// StableIDVersion is part of every decision key. Changing the key schema must
// use a new version so IDs never silently change meaning.
const (
	StableIDVersion       = "gomcdc-decision/v2"
	ConditionIDVersion    = "gomcdc-condition/v1"
	ClauseStableIDVersion = "gomcdc-clause/v1"
	ClauseGroupIDVersion  = "gomcdc-clause-group/v1"
)

// FileOptions identifies an original source file in its module and package.
type FileOptions struct {
	Path        string
	ModuleDir   string
	ModulePath  string
	PackagePath string
}

// Span is a zero-based, half-open byte range in the original source file.
type Span struct {
	Start int
	End   int
}

// Decision couples report metadata with the exact condition range used when
// instrumenting an unchanged copy of the source file.
type Decision struct {
	Metadata     cover.DecisionMetadata
	CanonicalKey string
	Condition    Span
	Conditions   []Condition
}

// Condition couples report metadata to the exact source bytes which must be
// wrapped by the instrumenter. Repeated textual expressions remain distinct
// occurrences with distinct indexes.
type Condition struct {
	Metadata     cover.ConditionMetadata
	CanonicalKey string
	Span         Span
}

// Clause couples clause metadata to the source node used to match the copied
// AST. DecisionIDs lists all boolean case expressions for a conditionless
// switch clause and mirrors Metadata.DecisionIDs for instrumentation matching.
type Clause struct {
	Metadata     cover.ClauseMetadata
	CanonicalKey string
	Span         Span
	DecisionIDs  []cover.DecisionID
}

// File is the immutable analysis result for one original source file.
type File struct {
	OriginalPath string
	RelativePath string
	PackageName  string
	Generated    bool
	// Source is the immutable pre-instrumentation revision used for IDs,
	// rewriting, and final C0 inventory. Consumers must not re-read the live
	// worktree after tests have started.
	Source       []byte
	SourceHash   [sha256.Size]byte
	Identifiers  []string
	Decisions    []Decision
	Clauses      []Clause
	NoMatches    []cover.NoMatchMetadata
	LineMappings []LineMapping
}

// LineMapping describes a source-position segment introduced by a user line
// directive. Analyzer metadata remains physical; C0 importers can use these
// segments to reverse compiler/cover logical positions without silently
// attributing imaginary filenames to the wrong source.
type LineMapping struct {
	PhysicalLine  int
	LogicalFile   string
	LogicalLine   int
	LogicalColumn int
}

// CollisionError reports two distinct canonical keys that produced one ID.
// Callers must stop instrumentation rather than merge these decisions.
type CollisionError struct {
	ID        uint64
	FirstKey  string
	SecondKey string
}

func (e *CollisionError) Error() string {
	return fmt.Sprintf("stable decision ID collision for %016x", e.ID)
}

// AnalyzeFiles analyzes all files and rejects ID collisions across the set.
func AnalyzeFiles(options []FileOptions) ([]File, error) {
	files := make([]File, 0, len(options))
	for _, option := range options {
		file, err := AnalyzeFile(option)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	if err := DetectCollisions(files); err != nil {
		return nil, err
	}
	return files, nil
}

// AnalyzeFile parses one original Go file. Files with Go's standard generated
// marker retain source metadata but do not produce coverage obligations.
func AnalyzeFile(options FileOptions) (File, error) {
	if options.Path == "" {
		return File{}, errors.New("analyze file: source path is empty")
	}
	if options.ModuleDir == "" {
		return File{}, errors.New("analyze file: module directory is empty")
	}
	if options.ModulePath == "" {
		return File{}, errors.New("analyze file: module path is empty")
	}
	if options.PackagePath == "" {
		return File{}, errors.New("analyze file: package path is empty")
	}

	originalPath, err := filepath.Abs(options.Path)
	if err != nil {
		return File{}, fmt.Errorf("analyze file %q: resolve source path: %w", options.Path, err)
	}
	moduleDir, err := filepath.Abs(options.ModuleDir)
	if err != nil {
		return File{}, fmt.Errorf("analyze file %q: resolve module directory: %w", options.Path, err)
	}
	relativePath, err := moduleRelativePath(moduleDir, originalPath)
	if err != nil {
		return File{}, fmt.Errorf("analyze file %q: %w", options.Path, err)
	}

	source, err := os.ReadFile(originalPath)
	if err != nil {
		return File{}, fmt.Errorf("analyze file %q: read: %w", options.Path, err)
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, originalPath, source, parser.ParseComments|parser.AllErrors)
	if err != nil {
		return File{}, fmt.Errorf("analyze file %q: parse: %w", options.Path, err)
	}

	result := File{
		OriginalPath: originalPath,
		RelativePath: relativePath,
		PackageName:  parsed.Name.Name,
		Generated:    ast.IsGenerated(parsed),
		Source:       append([]byte(nil), source...),
		SourceHash:   sha256.Sum256(source),
		Identifiers:  collectIdentifiers(parsed),
		LineMappings: collectLineMappings(fset, parsed),
	}
	if result.Generated {
		return result, nil
	}
	context := analysisContext{
		fset:        fset,
		file:        parsed,
		modulePath:  options.ModulePath,
		packagePath: options.PackagePath,
		relative:    relativePath,
	}
	ast.Walk(decisionVisitor{context: &context}, parsed)
	if context.err != nil {
		return File{}, fmt.Errorf("analyze file %q: %w", options.Path, context.err)
	}
	sort.Slice(context.decisions, func(i, j int) bool {
		left := context.decisions[i]
		right := context.decisions[j]
		if left.Condition.Start != right.Condition.Start {
			return left.Condition.Start < right.Condition.Start
		}
		if left.Condition.End != right.Condition.End {
			return left.Condition.End < right.Condition.End
		}
		return left.Metadata.Kind < right.Metadata.Kind
	})
	sort.Slice(context.clauses, func(i, j int) bool {
		left := context.clauses[i]
		right := context.clauses[j]
		if left.Span.Start != right.Span.Start {
			return left.Span.Start < right.Span.Start
		}
		if left.Span.End != right.Span.End {
			return left.Span.End < right.Span.End
		}
		if left.Metadata.Role != right.Metadata.Role {
			return left.Metadata.Role < right.Metadata.Role
		}
		return left.Metadata.ID < right.Metadata.ID
	})
	result.Decisions = context.decisions
	result.Clauses = context.clauses
	result.NoMatches = context.noMatches
	return result, nil
}

func collectLineMappings(fset *token.FileSet, file *ast.File) []LineMapping {
	tokenFile := fset.File(file.Pos())
	if tokenFile == nil {
		return nil
	}
	var mappings []LineMapping
	lastFile := ""
	lastDelta := 0
	lastColumnDelta := 0
	for line := 1; line <= tokenFile.LineCount(); line++ {
		position := tokenFile.LineStart(line)
		physical := fset.PositionFor(position, false)
		logical := fset.PositionFor(position, true)
		if physical.Filename == logical.Filename && physical.Line == logical.Line {
			lastFile = ""
			lastDelta = 0
			lastColumnDelta = 0
			continue
		}
		delta := logical.Line - physical.Line
		columnDelta := logical.Column - physical.Column
		if logical.Filename == lastFile && delta == lastDelta && columnDelta == lastColumnDelta {
			continue
		}
		mappings = append(mappings, LineMapping{
			PhysicalLine:  physical.Line,
			LogicalFile:   logical.Filename,
			LogicalLine:   logical.Line,
			LogicalColumn: logical.Column,
		})
		lastFile = logical.Filename
		lastDelta = delta
		lastColumnDelta = columnDelta
	}
	return mappings
}

// CanonicalKey returns the versioned, machine-independent key hashed for a
// decision ID. Positions must be the physical positions from the original
// source, not //line-adjusted display positions.
func CanonicalKey(metadata cover.DecisionMetadata) string {
	fields := []string{
		StableIDVersion,
		metadata.ModulePath,
		metadata.Package,
		filepath.ToSlash(metadata.Location.File),
		strconv.Itoa(metadata.Location.StartOffset),
		strconv.Itoa(metadata.Location.EndOffset),
		string(metadata.Kind),
	}
	return strings.Join(fields, "\x00")
}

// ClauseCanonicalKey returns the versioned key used for a clause ID.
func ClauseCanonicalKey(metadata cover.ClauseMetadata) string {
	fields := []string{
		ClauseStableIDVersion,
		metadata.ModulePath,
		metadata.Package,
		filepath.ToSlash(metadata.Location.File),
		strconv.Itoa(metadata.Location.StartOffset),
		strconv.Itoa(metadata.Location.EndOffset),
		string(metadata.Kind),
	}
	return strings.Join(fields, "\x00")
}

// ConditionCanonicalKey returns the versioned key used for a condition ID.
func ConditionCanonicalKey(decision cover.DecisionMetadata, condition cover.ConditionMetadata) string {
	fields := []string{
		ConditionIDVersion,
		decision.ModulePath,
		decision.Package,
		filepath.ToSlash(condition.Location.File),
		strconv.Itoa(condition.Location.StartOffset),
		strconv.Itoa(condition.Location.EndOffset),
		"condition",
		strconv.Itoa(int(condition.Index)),
	}
	return strings.Join(fields, "\x00")
}

// StableID hashes a canonical key and returns its first big-endian uint64.
func StableID(canonicalKey string) uint64 {
	digest := sha256.Sum256([]byte(canonicalKey))
	id := binary.BigEndian.Uint64(digest[:8])
	if id == 0 {
		// Runtime ID zero is the disabled/failed-instrumentation sentinel.
		// Collision detection still protects the deterministic remap target.
		return 1
	}
	return id
}

// DetectCollisions rejects one ID assigned to distinct canonical keys.
func DetectCollisions(files []File) error {
	keysByID := make(map[uint64]string)
	for _, file := range files {
		for _, decision := range file.Decisions {
			key := decision.CanonicalKey
			if key == "" {
				key = CanonicalKey(decision.Metadata)
			}
			id := uint64(decision.Metadata.ID)
			if first, exists := keysByID[id]; exists && first != key {
				return &CollisionError{
					ID:        id,
					FirstKey:  first,
					SecondKey: key,
				}
			}
			keysByID[id] = key
			for _, condition := range decision.Conditions {
				conditionKey := condition.CanonicalKey
				if conditionKey == "" {
					conditionKey = ConditionCanonicalKey(decision.Metadata, condition.Metadata)
				}
				conditionID := uint64(condition.Metadata.ID)
				if first, exists := keysByID[conditionID]; exists && first != conditionKey {
					return &CollisionError{ID: conditionID, FirstKey: first, SecondKey: conditionKey}
				}
				keysByID[conditionID] = conditionKey
			}
		}
	}
	for _, file := range files {
		for _, clause := range file.Clauses {
			key := clause.CanonicalKey
			if key == "" {
				key = ClauseCanonicalKey(clause.Metadata)
			}
			id := uint64(clause.Metadata.ID)
			if first, exists := keysByID[id]; exists && first != key {
				return &CollisionError{
					ID:        id,
					FirstKey:  first,
					SecondKey: key,
				}
			}
			keysByID[id] = key
		}
	}
	return nil
}

type analysisContext struct {
	fset        *token.FileSet
	file        *ast.File
	modulePath  string
	packagePath string
	relative    string
	decisions   []Decision
	clauses     []Clause
	noMatches   []cover.NoMatchMetadata
	err         error
}

type decisionVisitor struct {
	context          *analysisContext
	function         string
	functionLocation cover.SourceLocation
}

func (visitor decisionVisitor) Visit(node ast.Node) ast.Visitor {
	if node == nil || visitor.context.err != nil {
		return nil
	}

	switch current := node.(type) {
	case *ast.FuncDecl:
		child := visitor
		child.function = functionName(visitor.context.fset, current)
		child.functionLocation = visitor.sourceLocation(current)
		return child
	case *ast.FuncLit:
		child := visitor
		position := visitor.context.fset.PositionFor(current.Type.Func, false)
		if child.function == "" {
			child.function = fmt.Sprintf("<func@%d:%d>", position.Line, position.Column)
		} else {
			child.function = fmt.Sprintf("%s.func@%d:%d", child.function, position.Line, position.Column)
		}
		child.functionLocation = visitor.sourceLocation(current)
		return child
	case *ast.IfStmt:
		visitor.addDecision(cover.DecisionIf, current.Cond)
	case *ast.ForStmt:
		if current.Cond != nil {
			visitor.addDecision(cover.DecisionFor, current.Cond)
		}
	case *ast.SwitchStmt:
		kind := cover.ClauseExpressionSwitch
		if current.Tag == nil {
			kind = cover.ClauseConditionlessSwitch
		}
		visitor.addSwitchClauses(kind, current, current.Body.List)
	case *ast.TypeSwitchStmt:
		visitor.addSwitchClauses(cover.ClauseTypeSwitch, current, current.Body.List)
	case *ast.SelectStmt:
		visitor.addSelectClauses(current)
	}
	return visitor
}

func (visitor decisionVisitor) addDecision(kind cover.DecisionKind, condition ast.Expr) {
	start := visitor.context.fset.PositionFor(condition.Pos(), false)
	end := visitor.context.fset.PositionFor(condition.End(), false)
	function := visitor.function
	if function == "" {
		function = "<package>"
	}
	metadata := cover.DecisionMetadata{
		ModulePath: visitor.context.modulePath,
		Package:    visitor.context.packagePath,
		Location: cover.SourceLocation{
			File:        visitor.context.relative,
			Start:       cover.Position{Line: start.Line, Column: start.Column},
			End:         cover.Position{Line: end.Line, Column: end.Column},
			StartOffset: start.Offset,
			EndOffset:   end.Offset,
		},
		Function:         function,
		FunctionLocation: visitor.functionLocation,
		Kind:             kind,
		Expression:       formatExpression(visitor.context.fset, condition),
	}
	conditions, expressionTree := visitor.conditions(condition)
	metadata.Conditions = make([]cover.ConditionMetadata, len(conditions))
	for index := range conditions {
		key := ConditionCanonicalKey(metadata, conditions[index].Metadata)
		conditions[index].CanonicalKey = key
		conditions[index].Metadata.ID = cover.ConditionID(StableID(key))
		metadata.Conditions[index] = conditions[index].Metadata
	}
	metadata.ExpressionTree = expressionTree
	key := CanonicalKey(metadata)
	metadata.ID = cover.DecisionID(StableID(key))
	visitor.context.decisions = append(visitor.context.decisions, Decision{
		Metadata:     metadata,
		CanonicalKey: key,
		Condition: Span{
			Start: start.Offset,
			End:   end.Offset,
		},
		Conditions: conditions,
	})
}

func (visitor decisionVisitor) conditions(expression ast.Expr) ([]Condition, *cover.BooleanExpression) {
	conditions := make([]Condition, 0, 4)
	var walk func(ast.Expr) *cover.BooleanExpression
	walk = func(current ast.Expr) *cover.BooleanExpression {
		if visitor.context.err != nil {
			return cover.NewConstantExpression(false)
		}
		switch node := unparen(current).(type) {
		case *ast.BinaryExpr:
			switch node.Op {
			case token.LAND:
				return cover.NewAndExpression(walk(node.X), walk(node.Y))
			case token.LOR:
				return cover.NewOrExpression(walk(node.X), walk(node.Y))
			}
		case *ast.UnaryExpr:
			// Negation is always an expression-tree node. The atomic occurrence
			// of !a is a, so condition coverage records a's value while decision
			// coverage still observes the result of !a.
			if node.Op == token.NOT {
				return cover.NewNotExpression(walk(node.X))
			}
		}

		position := visitor.context.fset.PositionFor(current.Pos(), false)
		end := visitor.context.fset.PositionFor(current.End(), false)
		if len(conditions) >= int(^uint16(0)) {
			visitor.context.err = fmt.Errorf("decision at byte %d has more than %d atomic conditions", position.Offset, ^uint16(0))
			return cover.NewConstantExpression(false)
		}
		index := uint16(len(conditions))
		metadata := cover.ConditionMetadata{
			Index:      index,
			Expression: formatExpression(visitor.context.fset, current),
			Location: cover.SourceLocation{
				File:        visitor.context.relative,
				Start:       cover.Position{Line: position.Line, Column: position.Column},
				End:         cover.Position{Line: end.Line, Column: end.Column},
				StartOffset: position.Offset,
				EndOffset:   end.Offset,
			},
		}
		conditions = append(conditions, Condition{
			Metadata: metadata,
			Span:     Span{Start: position.Offset, End: end.Offset},
		})
		return cover.NewConditionExpression(index)
	}
	return conditions, walk(expression)
}

func unparen(expression ast.Expr) ast.Expr {
	for {
		parenthesized, ok := expression.(*ast.ParenExpr)
		if !ok {
			return expression
		}
		expression = parenthesized.X
	}
}

func (visitor decisionVisitor) addSwitchClauses(kind cover.ClauseKind, switchNode ast.Node, statements []ast.Stmt) {
	groupID := visitor.clauseGroupID(kind, switchNode)
	hasDefault := false
	for index, statement := range statements {
		if index >= int(^uint16(0)) {
			visitor.context.err = fmt.Errorf("%s has more than %d clauses", kind, ^uint16(0))
			return
		}
		clause, ok := statement.(*ast.CaseClause)
		if !ok {
			continue
		}
		role := cover.ClauseCase
		if clause.List == nil {
			role = cover.ClauseDefault
			hasDefault = true
		}
		added := visitor.addClause(groupID, kind, role, uint16(index), clause, clause.List)
		if kind != cover.ClauseConditionlessSwitch || clause.List == nil {
			continue
		}
		for _, expression := range clause.List {
			before := len(visitor.context.decisions)
			visitor.addDecision(cover.DecisionSwitchCase, expression)
			decisionID := visitor.context.decisions[before].Metadata.ID
			added.DecisionIDs = append(added.DecisionIDs, decisionID)
		}
		added.Metadata.DecisionIDs = append([]cover.DecisionID(nil), added.DecisionIDs...)
	}
	if (kind == cover.ClauseExpressionSwitch || kind == cover.ClauseTypeSwitch) && !hasDefault {
		if len(statements) > int(^uint16(0)) {
			visitor.context.err = fmt.Errorf("%s has too many clauses to represent no-match", kind)
			return
		}
		visitor.addNoMatch(groupID, kind, switchNode)
	}
}

func (visitor decisionVisitor) addNoMatch(groupID cover.ClauseGroupID, kind cover.ClauseKind, node ast.Node) {
	start := visitor.context.fset.PositionFor(node.Pos(), false)
	end := visitor.context.fset.PositionFor(node.End(), false)
	function := visitor.function
	if function == "" {
		function = "<package>"
	}
	visitor.context.noMatches = append(visitor.context.noMatches, cover.NoMatchMetadata{
		SwitchID:         cover.SwitchID(groupID),
		ModulePath:       visitor.context.modulePath,
		Package:          visitor.context.packagePath,
		Function:         function,
		FunctionLocation: visitor.functionLocation,
		Kind:             kind,
		Location: cover.SourceLocation{
			File:        visitor.context.relative,
			Start:       cover.Position{Line: start.Line, Column: start.Column},
			End:         cover.Position{Line: end.Line, Column: end.Column},
			StartOffset: start.Offset,
			EndOffset:   end.Offset,
		},
	})
}

func (visitor decisionVisitor) addSelectClauses(statement *ast.SelectStmt) {
	groupID := visitor.clauseGroupID(cover.ClauseSelect, statement)
	for index, raw := range statement.Body.List {
		if index >= int(^uint16(0)) {
			visitor.context.err = fmt.Errorf("select has more than %d clauses", ^uint16(0))
			return
		}
		clause, ok := raw.(*ast.CommClause)
		if !ok {
			continue
		}
		role := cover.ClauseCase
		if clause.Comm == nil {
			role = cover.ClauseDefault
		}
		visitor.addClause(groupID, cover.ClauseSelect, role, uint16(index), clause, nil)
	}
}

func (visitor decisionVisitor) addClause(
	groupID cover.ClauseGroupID,
	kind cover.ClauseKind,
	role cover.ClauseRole,
	index uint16,
	node ast.Node,
	expressions []ast.Expr,
) *Clause {
	start := visitor.context.fset.PositionFor(node.Pos(), false)
	end := visitor.context.fset.PositionFor(node.End(), false)
	function := visitor.function
	if function == "" {
		function = "<package>"
	}
	metadata := cover.ClauseMetadata{
		ModulePath:       visitor.context.modulePath,
		Package:          visitor.context.packagePath,
		GroupID:          groupID,
		SwitchID:         cover.SwitchID(groupID),
		Function:         function,
		FunctionLocation: visitor.functionLocation,
		Kind:             kind,
		Role:             role,
		Index:            index,
		Location: cover.SourceLocation{
			File:        visitor.context.relative,
			Start:       cover.Position{Line: start.Line, Column: start.Column},
			End:         cover.Position{Line: end.Line, Column: end.Column},
			StartOffset: start.Offset,
			EndOffset:   end.Offset,
		},
	}
	for _, expression := range expressions {
		formatted := formatExpression(visitor.context.fset, expression)
		if kind == cover.ClauseTypeSwitch {
			metadata.Types = append(metadata.Types, formatted)
		} else {
			metadata.Expressions = append(metadata.Expressions, formatted)
		}
	}
	key := ClauseCanonicalKey(metadata)
	metadata.ID = cover.ClauseID(StableID(key))
	visitor.context.clauses = append(visitor.context.clauses, Clause{
		Metadata:     metadata,
		CanonicalKey: key,
		Span:         Span{Start: start.Offset, End: end.Offset},
	})
	return &visitor.context.clauses[len(visitor.context.clauses)-1]
}

func (visitor decisionVisitor) sourceLocation(node ast.Node) cover.SourceLocation {
	start := visitor.context.fset.PositionFor(node.Pos(), false)
	end := visitor.context.fset.PositionFor(node.End(), false)
	return cover.SourceLocation{
		File:  visitor.context.relative,
		Start: cover.Position{Line: start.Line, Column: start.Column},
		End:   cover.Position{Line: end.Line, Column: end.Column},
	}
}

func (visitor decisionVisitor) clauseGroupID(kind cover.ClauseKind, node ast.Node) cover.ClauseGroupID {
	start := visitor.context.fset.PositionFor(node.Pos(), false)
	end := visitor.context.fset.PositionFor(node.End(), false)
	key := strings.Join([]string{
		ClauseGroupIDVersion,
		visitor.context.modulePath,
		visitor.context.packagePath,
		visitor.context.relative,
		string(kind),
		strconv.Itoa(start.Offset),
		strconv.Itoa(end.Offset),
	}, "\x00")
	return cover.ClauseGroupID(StableID(key))
}

func functionName(fset *token.FileSet, declaration *ast.FuncDecl) string {
	if declaration.Recv == nil || len(declaration.Recv.List) == 0 {
		return declaration.Name.Name
	}
	var receiver bytes.Buffer
	if err := format.Node(&receiver, fset, declaration.Recv.List[0].Type); err != nil {
		return declaration.Name.Name
	}
	return receiver.String() + "." + declaration.Name.Name
}

func formatExpression(fset *token.FileSet, expression ast.Expr) string {
	var formatted bytes.Buffer
	if err := format.Node(&formatted, fset, expression); err != nil {
		return ""
	}
	return formatted.String()
}

func collectIdentifiers(file *ast.File) []string {
	identifiers := make(map[string]struct{})
	ast.Inspect(file, func(node ast.Node) bool {
		if identifier, ok := node.(*ast.Ident); ok {
			identifiers[identifier.Name] = struct{}{}
		}
		return true
	})
	result := make([]string, 0, len(identifiers))
	for identifier := range identifiers {
		result = append(result, identifier)
	}
	sort.Strings(result)
	return result
}

func moduleRelativePath(moduleDir, path string) (string, error) {
	relative, err := filepath.Rel(moduleDir, path)
	if err != nil {
		return "", fmt.Errorf("make module-relative path: %w", err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("source is outside module directory %q", moduleDir)
	}
	return filepath.ToSlash(filepath.Clean(relative)), nil
}
