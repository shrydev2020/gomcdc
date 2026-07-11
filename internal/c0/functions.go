package c0

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"sort"
)

type functionExtent struct {
	name            string
	position        SourceRange
	body            SourceRange
	depth           int
	statementStarts []Position
}

type functionVisitor struct {
	fset    *token.FileSet
	parents []string
	funcs   *[]functionExtent
}

func discoverFunctions(fset *token.FileSet, file *ast.File) []functionExtent {
	functions := make([]functionExtent, 0)
	ast.Walk(functionVisitor{fset: fset, funcs: &functions}, file)
	sort.Slice(functions, func(i, j int) bool {
		if lessRange(functions[i].position, functions[j].position) {
			return true
		}
		if lessRange(functions[j].position, functions[i].position) {
			return false
		}
		return functions[i].name < functions[j].name
	})
	return functions
}

func (visitor functionVisitor) Visit(node ast.Node) ast.Visitor {
	if node == nil {
		return nil
	}

	switch current := node.(type) {
	case *ast.FuncDecl:
		if current.Body == nil {
			return nil
		}
		name := declaredFunctionName(visitor.fset, current)
		*visitor.funcs = append(*visitor.funcs, functionExtent{
			name:            name,
			position:        nodeRange(visitor.fset, current),
			body:            nodeRange(visitor.fset, current.Body),
			depth:           len(visitor.parents),
			statementStarts: directStatementStarts(visitor.fset, current.Body),
		})
		child := visitor
		child.parents = append(append([]string(nil), visitor.parents...), name)
		return child

	case *ast.FuncLit:
		position := physicalPosition(visitor.fset, current.Type.Func)
		name := fmt.Sprintf("<func@%d:%d>", position.Line, position.Column)
		if len(visitor.parents) > 0 {
			name = fmt.Sprintf("%s.func@%d:%d", visitor.parents[len(visitor.parents)-1], position.Line, position.Column)
		}
		*visitor.funcs = append(*visitor.funcs, functionExtent{
			name:            name,
			position:        nodeRange(visitor.fset, current),
			body:            nodeRange(visitor.fset, current.Body),
			depth:           len(visitor.parents),
			statementStarts: directStatementStarts(visitor.fset, current.Body),
		})
		child := visitor
		child.parents = append(append([]string(nil), visitor.parents...), name)
		return child
	}
	return visitor
}

func declaredFunctionName(fset *token.FileSet, declaration *ast.FuncDecl) string {
	if declaration.Recv == nil || len(declaration.Recv.List) == 0 {
		return declaration.Name.Name
	}
	var receiver bytes.Buffer
	if err := format.Node(&receiver, fset, declaration.Recv.List[0].Type); err != nil {
		return declaration.Name.Name
	}
	return receiver.String() + "." + declaration.Name.Name
}

func directStatementStarts(fset *token.FileSet, body *ast.BlockStmt) []Position {
	positions := make([]Position, 0)
	ast.Inspect(body, func(node ast.Node) bool {
		if node == nil {
			return false
		}
		if _, nested := node.(*ast.FuncLit); nested {
			return false
		}
		statement, isStatement := node.(ast.Stmt)
		if !isStatement {
			return true
		}
		switch statement.(type) {
		case *ast.BlockStmt, *ast.EmptyStmt:
			return true
		default:
			positions = append(positions, physicalPosition(fset, statement.Pos()))
			return true
		}
	})
	sort.Slice(positions, func(i, j int) bool {
		return comparePosition(positions[i], positions[j]) < 0
	})
	return positions
}

func nodeRange(fset *token.FileSet, node ast.Node) SourceRange {
	return SourceRange{
		Start: physicalPosition(fset, node.Pos()),
		End:   physicalPosition(fset, node.End()),
	}
}

func physicalPosition(fset *token.FileSet, position token.Pos) Position {
	resolved := fset.PositionFor(position, false)
	return Position{Line: resolved.Line, Column: resolved.Column}
}

func ownerForBlock(functions []functionExtent, block SourceRange) int {
	owner := -1
	ownerDepth := -1
	for index := range functions {
		function := &functions[index]
		if !containsPosition(function.body, block.Start) || !containsOriginalStatement(*function, block) {
			continue
		}
		if function.depth > ownerDepth {
			owner = index
			ownerDepth = function.depth
		}
	}
	return owner
}

func containsOriginalStatement(function functionExtent, block SourceRange) bool {
	for _, start := range function.statementStarts {
		if containsPosition(block, start) {
			return true
		}
	}
	return false
}

func containsPosition(sourceRange SourceRange, position Position) bool {
	return comparePosition(position, sourceRange.Start) >= 0 && comparePosition(position, sourceRange.End) < 0
}
