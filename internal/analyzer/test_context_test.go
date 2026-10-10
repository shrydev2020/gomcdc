package analyzer_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/analyzer"
	"github.com/shrydev2020/gomcdc/v2/internal/loader"
)

func TestTypedTestContextsSeparateHandlesAndLookalikes(t *testing.T) {
	root := t.TempDir()
	const source = `package fixture
import tt "testing"
type Alias = tt.T
type Lookalike struct{}
func (*Lookalike) Run(string, func(*tt.T)) {}
func (*Lookalike) Cleanup(func()) {}
func Unnamed(*tt.T) {}
func Value(tt.T) {}
func Ambiguous(a, b *tt.T) {}
func Interface(t tt.TB) { t.Cleanup(func(){}) }
func BenchmarkTyped(b *tt.B) { b.Run("child", func(b *tt.B){}) }
func FuzzTyped(f *tt.F) { f.Fuzz(func(t *tt.T, input int){}) }
func TestTyped(t *tt.T) {
 t.Run("child", func(t *Alias){})
 (*tt.T).Run(t, "named", Unnamed)
 t.Cleanup(func(){})
 (*tt.T).Cleanup(t, func(){})
 (&Lookalike{}).Run("fake", Unnamed)
 (&Lookalike{}).Cleanup(func(){})
}
func ExampleTyped() {
 // Output:
}
func ExampleDocumentationOnly() {}
`
	for path, contents := range map[string]string{"go.mod": "module example.test/fixture\n\ngo 1.26.0\n", "fixture.go": "package fixture\n", "fixture_test.go": source} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := loader.Load(t.Context(), loader.Options{Dir: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Files) != 1 || len(loaded.TestFiles) != 1 {
		t.Fatalf("files=%v tests=%v", loaded.Files, loaded.TestFiles)
	}
	contexts := loaded.TestFiles[0].TestContexts
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture_test.go", source, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	wantEntries := map[string]bool{"Unnamed": true, "Value": false, "Ambiguous": false, "Interface": true, "BenchmarkTyped": true, "FuzzTyped": true, "TestTyped": true}
	for _, declaration := range file.Decls {
		fn, ok := declaration.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if want, checked := wantEntries[fn.Name.Name]; checked {
			_, got := contexts.Entries[fset.PositionFor(fn.Body.Lbrace, false).Offset]
			if got != want {
				t.Errorf("%s entry=%t, want %t", fn.Name.Name, got, want)
			}
		}
	}
	if len(contexts.Callbacks) != 5 {
		t.Fatalf("typed callbacks=%v, want Run/Cleanup only", contexts.Callbacks)
	}
	if len(contexts.Examples) != 1 {
		t.Fatalf("executed examples=%v", contexts.Examples)
	}
	if !analyzer.AnalyzeTestContexts(fset, file, nil).Empty() {
		t.Fatal("missing type evidence produced an attribution")
	}
}
