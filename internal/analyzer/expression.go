package analyzer

import (
	"fmt"
	"go/parser"
	"go/token"

	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
)

// RestoreExpression reconstructs the Boolean structure of an archived
// expression using the same occurrence decomposition as instrumentation.
// It refuses a changed condition layout rather than assigning new meanings
// to the positions in saved evaluation vectors. No source files are read.
func RestoreExpression(expression string, atoms []string) (*cover.BooleanExpression, error) {
	fset := token.NewFileSet()
	expr, err := parser.ParseExprFrom(fset, "", expression, 0)
	if err != nil {
		return nil, err
	}
	context := &analysisContext{fset: fset}
	conditions, tree := (decisionVisitor{context: context}).conditions(expr)
	if context.err != nil {
		return nil, context.err
	}
	if len(conditions) != len(atoms) {
		return nil, fmt.Errorf("saved condition count %d differs from expression count %d", len(atoms), len(conditions))
	}
	for i, condition := range conditions {
		if condition.Metadata.Expression != atoms[i] {
			return nil, fmt.Errorf("saved condition %d differs from expression", i)
		}
	}
	return tree, nil
}
