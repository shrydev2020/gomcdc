// Portions of this file are adapted from Go's cmd/cover implementation.
// Copyright 2009 The Go Authors. See ../../THIRD_PARTY_NOTICES.md.
package c0

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
)

// BuildInventory derives the same source block denominator as Go's cover
// instrumentation, without compiling, editing, or executing the source.
// filename should be the stable module-relative original path.
func BuildInventory(filename string, source []byte) (FileInventory, error) {
	if filename == "" {
		return FileInventory{}, fmt.Errorf("build C0 inventory: filename is empty")
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, source, parser.ParseComments|parser.AllErrors)
	if err != nil {
		return FileInventory{}, fmt.Errorf("build C0 inventory %q: parse: %w", filename, err)
	}
	builder := &inventoryBuilder{
		fset:   fset,
		source: append([]byte(nil), source...),
		seen:   make(map[inventoryPositionPair]struct{}),
		blocks: make([]InventoryBlock, 0),
	}
	ast.Walk(inventoryVisitor{builder: builder}, file)
	return FileInventory{
		Blocks: builder.blocks,
	}, nil
}

type inventoryBuilder struct {
	fset   *token.FileSet
	source []byte
	seen   map[inventoryPositionPair]struct{}
	blocks []InventoryBlock
}

type inventoryVisitor struct {
	builder     *inventoryBuilder
	profileFile string
}

type inventoryPositionPair struct {
	startFile  string
	endFile    string
	start, end Position
}

func (visitor inventoryVisitor) Visit(node ast.Node) ast.Visitor {
	if node == nil {
		return nil
	}
	builder := visitor.builder
	switch current := node.(type) {
	case *ast.BlockStmt:
		if len(current.List) > 0 {
			switch current.List[0].(type) {
			case *ast.CaseClause:
				for _, statement := range current.List {
					clause := statement.(*ast.CaseClause)
					visitor.addCounters(clause.Colon+1, clause.Colon+1, clause.End(), clause.Body, false)
				}
				return visitor
			case *ast.CommClause:
				for _, statement := range current.List {
					clause := statement.(*ast.CommClause)
					visitor.addCounters(clause.Colon+1, clause.Colon+1, clause.End(), clause.Body, false)
				}
				return visitor
			}
		}
		visitor.addCounters(current.Lbrace, current.Lbrace+1, current.Rbrace+1, current.List, true)

	case *ast.IfStmt:
		if current.Init != nil {
			ast.Walk(visitor, current.Init)
		}
		ast.Walk(visitor, current.Cond)
		ast.Walk(visitor, current.Body)
		if current.Else == nil {
			return nil
		}
		elseOffset := builder.findText(current.Body.End(), "else")
		if elseOffset < 0 {
			return nil
		}
		position := builder.fset.File(current.Body.End()).Pos(elseOffset + len("else"))
		switch statement := current.Else.(type) {
		case *ast.IfStmt:
			block := &ast.BlockStmt{Lbrace: position, List: []ast.Stmt{statement}, Rbrace: statement.End()}
			ast.Walk(visitor, block)
		case *ast.BlockStmt:
			block := *statement
			block.Lbrace = position
			ast.Walk(visitor, &block)
		}
		return nil

	case *ast.SelectStmt:
		if current.Body == nil || len(current.Body.List) == 0 {
			return nil
		}
	case *ast.SwitchStmt:
		if current.Body == nil || len(current.Body.List) == 0 {
			if current.Init != nil {
				ast.Walk(visitor, current.Init)
			}
			if current.Tag != nil {
				ast.Walk(visitor, current.Tag)
			}
			return nil
		}
	case *ast.TypeSwitchStmt:
		if current.Body == nil || len(current.Body.List) == 0 {
			if current.Init != nil {
				ast.Walk(visitor, current.Init)
			}
			ast.Walk(visitor, current.Assign)
			return nil
		}

	case *ast.FuncDecl:
		if current.Name.Name == "_" || current.Body == nil {
			return nil
		}
		child := visitor
		child.profileFile = builder.profileFilename(current.Pos())
		ast.Walk(child, current.Body)
		return nil

	case *ast.FuncLit:
		child := visitor
		if child.profileFile == "" {
			child.profileFile = builder.profileFilename(current.Pos())
		}
		ast.Walk(child, current.Body)
		return nil
	}
	return visitor
}

func (visitor inventoryVisitor) addCounters(
	position token.Pos,
	insertPosition token.Pos,
	blockEnd token.Pos,
	statements []ast.Stmt,
	extendToClosingBrace bool,
) {
	if len(statements) == 0 {
		visitor.record(insertPosition, blockEnd, 0, nil)
		return
	}
	statements = append([]ast.Stmt(nil), statements...)
	for {
		last := 0
		end := blockEnd
		for ; last < len(statements); last++ {
			statement := statements[last]
			end = statementBoundary(statement)
			if endsBasicSourceBlock(statement) {
				if label, isLabel := statement.(*ast.LabeledStmt); isLabel && !isControl(label.Stmt) {
					newLabel := *label
					newLabel.Stmt = &ast.EmptyStmt{Semicolon: label.Stmt.Pos(), Implicit: true}
					end = label.Pos()
					statements[last] = &newLabel
					statements = append(statements, nil)
					copy(statements[last+1:], statements[last:])
					statements[last+1] = label.Stmt
				}
				last++
				extendToClosingBrace = false
				break
			}
		}
		if extendToClosingBrace {
			end = blockEnd
		}
		if position != end {
			visitor.record(position, end, last, statements[:last])
		}
		statements = statements[last:]
		if len(statements) == 0 {
			break
		}
		position = statements[0].Pos()
	}
}

func (visitor inventoryVisitor) record(start, end token.Pos, statementCount int, statements []ast.Stmt) {
	builder := visitor.builder
	physicalStart := builder.fset.PositionFor(start, false)
	physicalEnd := builder.fset.PositionFor(end, false)
	profileStart := builder.fset.PositionFor(start, true)
	profileEnd := builder.fset.PositionFor(end, true)
	profileFile := visitor.profileFile
	if profileFile == "" {
		profileFile = profileStart.Filename
	}
	pair := inventoryPositionPair{
		startFile: profileStart.Filename,
		endFile:   profileEnd.Filename,
		start:     Position{Line: profileStart.Line, Column: profileStart.Column},
		end:       Position{Line: profileEnd.Line, Column: profileEnd.Column},
	}
	for {
		if _, duplicate := builder.seen[pair]; !duplicate {
			break
		}
		pair.end.Column++
	}
	builder.seen[pair] = struct{}{}
	anchors := make([]Position, 0, len(statements))
	for _, statement := range statements {
		position := builder.fset.PositionFor(statement.Pos(), true)
		anchors = append(anchors, Position{Line: position.Line, Column: position.Column})
	}
	builder.blocks = append(builder.blocks, InventoryBlock{
		PhysicalRange: SourceRange{
			Start: Position{Line: physicalStart.Line, Column: physicalStart.Column},
			End:   Position{Line: physicalEnd.Line, Column: physicalEnd.Column},
		},
		ProfileFile:    profileFile,
		ProfileRange:   SourceRange{Start: pair.start, End: pair.end},
		ProfileAnchors: anchors,
		Statements:     statementCount,
	})
}

func (builder *inventoryBuilder) findText(position token.Pos, text string) int {
	start := builder.fset.PositionFor(position, false).Offset
	needle := []byte(text)
	for index := start; index < len(builder.source); {
		if bytes.HasPrefix(builder.source[index:], needle) {
			return index
		}
		if index+2 <= len(builder.source) && builder.source[index] == '/' && builder.source[index+1] == '/' {
			for index < len(builder.source) && builder.source[index] != '\n' {
				index++
			}
			continue
		}
		if index+2 <= len(builder.source) && builder.source[index] == '/' && builder.source[index+1] == '*' {
			index += 2
			for index+1 < len(builder.source) {
				if builder.source[index] == '*' && builder.source[index+1] == '/' {
					index += 2
					break
				}
				index++
			}
			continue
		}
		index++
	}
	return -1
}

func (builder *inventoryBuilder) profileFilename(position token.Pos) string {
	return builder.fset.PositionFor(position, true).Filename
}

func statementBoundary(statement ast.Stmt) token.Pos {
	switch current := statement.(type) {
	case *ast.BlockStmt:
		return current.Lbrace
	case *ast.IfStmt:
		if found, position := firstFuncLiteral(current.Init); found {
			return position
		}
		if found, position := firstFuncLiteral(current.Cond); found {
			return position
		}
		return current.Body.Lbrace
	case *ast.ForStmt:
		if found, position := firstFuncLiteral(current.Init); found {
			return position
		}
		if found, position := firstFuncLiteral(current.Cond); found {
			return position
		}
		if found, position := firstFuncLiteral(current.Post); found {
			return position
		}
		return current.Body.Lbrace
	case *ast.LabeledStmt:
		return statementBoundary(current.Stmt)
	case *ast.RangeStmt:
		if found, position := firstFuncLiteral(current.X); found {
			return position
		}
		return current.Body.Lbrace
	case *ast.SwitchStmt:
		if found, position := firstFuncLiteral(current.Init); found {
			return position
		}
		if found, position := firstFuncLiteral(current.Tag); found {
			return position
		}
		return current.Body.Lbrace
	case *ast.SelectStmt:
		return current.Body.Lbrace
	case *ast.TypeSwitchStmt:
		if found, position := firstFuncLiteral(current.Init); found {
			return position
		}
		return current.Body.Lbrace
	default:
		if found, position := firstFuncLiteral(statement); found {
			return position
		}
		return statement.End()
	}
}

func endsBasicSourceBlock(statement ast.Stmt) bool {
	switch current := statement.(type) {
	case *ast.BlockStmt, *ast.BranchStmt, *ast.ForStmt, *ast.IfStmt, *ast.LabeledStmt,
		*ast.RangeStmt, *ast.SwitchStmt, *ast.SelectStmt, *ast.TypeSwitchStmt:
		return true
	case *ast.ExprStmt:
		if call, ok := current.X.(*ast.CallExpr); ok {
			if identifier, ok := call.Fun.(*ast.Ident); ok && identifier.Name == "panic" && len(call.Args) == 1 {
				return true
			}
		}
	}
	found, _ := firstFuncLiteral(statement)
	return found
}

func isControl(statement ast.Stmt) bool {
	switch statement.(type) {
	case *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.SelectStmt, *ast.TypeSwitchStmt:
		return true
	default:
		return false
	}
}

func firstFuncLiteral(node ast.Node) (bool, token.Pos) {
	if node == nil {
		return false, token.NoPos
	}
	finder := &funcLiteralFinder{}
	ast.Walk(finder, node)
	return finder.position != token.NoPos, finder.position
}

type funcLiteralFinder struct{ position token.Pos }

func (finder *funcLiteralFinder) Visit(node ast.Node) ast.Visitor {
	if finder.position != token.NoPos {
		return nil
	}
	if literal, ok := node.(*ast.FuncLit); ok {
		finder.position = literal.Body.Lbrace
		return nil
	}
	return finder
}
