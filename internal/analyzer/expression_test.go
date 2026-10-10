package analyzer

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRestoreExpressionUsesInstrumentedOccurrenceLayout(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "logic.go")
	source := `package p
func F(a,b bool,x,y int) {
 if !((a && b) || (x&3 == y)) {}
 if a && a || false {}
 if a /* keep occurrence */ &&
    (func() bool { return b })() {}
 if !(a == b) || !!a {}
}
`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := AnalyzeFile(FileOptions{Path: path, ModuleDir: root, ModulePath: "example.test/p", PackagePath: "example.test/p"})
	if err != nil {
		t.Fatal(err)
	}
	for _, decision := range file.Decisions {
		atoms := make([]string, len(decision.Metadata.Conditions))
		for i, c := range decision.Metadata.Conditions {
			atoms[i] = c.Expression
		}
		restored, err := RestoreExpression(decision.Metadata.Expression, atoms)
		if err != nil || !reflect.DeepEqual(restored, decision.Metadata.ExpressionTree) {
			t.Fatalf("%s: tree changed: %v", decision.Metadata.Expression, err)
		}
		atoms[0] = "different"
		if _, err := RestoreExpression(decision.Metadata.Expression, atoms); err == nil {
			t.Fatal("changed occurrence accepted")
		}
	}
}
