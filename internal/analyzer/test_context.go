package analyzer

import (
	"go/ast"
	"go/doc"
	"go/token"
	"go/types"
	"strings"
)

// TestContexts describes typed test entry points and runner callbacks by their
// original physical byte offsets. It does not add test code to metric inputs.
type TestContexts struct {
	Entries   map[int]TestEntry
	Callbacks map[int]TestCallback
	Methods   map[int]string
	Examples  map[int]string
}

type TestEntry struct {
	Field int
	Name  int
}

type TestCallback struct {
	Method   string
	Argument int
}

func (contexts TestContexts) Empty() bool {
	return len(contexts.Entries)+len(contexts.Callbacks)+len(contexts.Methods)+len(contexts.Examples) == 0
}

func AnalyzeTestContexts(fset *token.FileSet, file *ast.File, info *types.Info) TestContexts {
	contexts := TestContexts{Entries: make(map[int]TestEntry), Callbacks: make(map[int]TestCallback), Methods: make(map[int]string), Examples: make(map[int]string)}
	if info == nil {
		return contexts
	}
	offset := func(pos token.Pos) int { return fset.PositionFor(pos, false).Offset }
	entry := func(signature *ast.FuncType, body *ast.BlockStmt) {
		if body == nil || signature.Params == nil {
			return
		}
		count := 0
		var candidate TestEntry
		for fieldIndex, field := range signature.Params.List {
			typ := info.TypeOf(field.Type)
			if !isTestHandle(typ) || types.NewMethodSet(typ).Lookup(nil, "Name") == nil {
				continue
			}
			for nameIndex := range max(1, len(field.Names)) {
				count++
				candidate = TestEntry{Field: fieldIndex, Name: nameIndex}
			}
		}
		if count == 1 {
			contexts.Entries[offset(body.Lbrace)] = candidate
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch current := node.(type) {
		case *ast.FuncDecl:
			entry(current.Type, current.Body)
		case *ast.FuncLit:
			entry(current.Type, current.Body)
		case *ast.SelectorExpr:
			selection := info.Selections[current]
			if selection == nil || selection.Kind() != types.MethodVal || selection.Obj().Pkg() == nil || selection.Obj().Pkg().Path() != "testing" {
				return true
			}
			signature := selection.Obj().Type().(*types.Signature)
			switch selection.Obj().Name() {
			case "Run":
				if handleName(signature.Recv().Type()) == "T" {
					contexts.Methods[offset(current.Pos())] = "Run"
				}
			case "Cleanup":
				contexts.Methods[offset(current.Pos())] = "RegisterCleanup"
			}
		case *ast.CallExpr:
			selector, ok := current.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			selection := info.Selections[selector]
			if selection == nil || selection.Kind() != types.MethodExpr || selection.Obj().Pkg() == nil || selection.Obj().Pkg().Path() != "testing" {
				return true
			}
			switch selection.Obj().Name() {
			case "Run":
				if handleName(selection.Obj().Type().(*types.Signature).Recv().Type()) == "T" {
					contexts.Callbacks[offset(current.Pos())] = TestCallback{Method: "Test", Argument: 2}
				}
			case "Cleanup":
				contexts.Callbacks[offset(current.Pos())] = TestCallback{Method: "Cleanup", Argument: 1}
			}
		}
		return true
	})
	if !strings.HasSuffix(fset.PositionFor(file.Pos(), false).Filename, "_test.go") {
		return contexts
	}
	for _, example := range doc.Examples(file) {
		if example.Output == "" && !example.EmptyOutput {
			continue
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Body != nil && function.Name.Name == "Example"+example.Name {
				contexts.Examples[offset(function.Body.Lbrace)] = function.Name.Name
			}
		}
	}
	return contexts
}

func isTestHandle(typ types.Type) bool { return handleName(typ) != "" }

func handleName(typ types.Type) string {
	if typ == nil {
		return ""
	}
	typ = types.Unalias(typ)
	if pointer, ok := typ.(*types.Pointer); ok {
		typ = types.Unalias(pointer.Elem())
	}
	named, ok := typ.(*types.Named)
	if !ok || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != "testing" {
		return ""
	}
	switch named.Obj().Name() {
	case "T", "B", "F", "TB":
		return named.Obj().Name()
	}
	return ""
}
