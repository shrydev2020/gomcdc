package instrument

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstrumentPackagePreservesEmbedDeclarations(t *testing.T) {
	const source = `package embedded
import _ "embed"

func pick(value string) string {
 label := value
 if len(value) <= 1024 {
  return value
 }
 return label + "..."
}

//go:embed first.txt
var first string

func enabled(a, b bool) bool {
 if a && b { return true }
 return false
}

var (
 //go:embed second.txt
 second string
)
`
	originalRoot, workspace := t.TempDir(), t.TempDir()
	original := writeFile(t, originalRoot, "embedded/source.go", source)
	copied := writeFile(t, workspace, "embedded/source.go", source)
	analyzed := analyze(t, original, originalRoot, "example.com/fixture", "example.com/fixture/embedded")
	writeFile(t, workspace, "go.mod", "module example.com/fixture\n\ngo 1.26.0\n")
	writeFile(t, workspace, "embedded/first.txt", "first payload")
	writeFile(t, workspace, "embedded/second.txt", "second payload")
	writeFile(t, workspace, "embedded/source_test.go", `package embedded
import "testing"
func TestEmbeddedContent(t *testing.T) {
 if first != "first payload" || second != "second payload" { t.Fatal(first, second) }
 if pick(first) != first || pick("x") != "x" || !enabled(true, true) || enabled(false, true) { t.Fatal("instrumented function changed") }
}
`)
	writeFile(t, workspace, "runtimecov/runtime.go", `package runtimecov
type EvaluationID = uint64
type Hooks struct{}
func NewHooks(string) Hooks { return Hooks{} }
func (Hooks) BeginInto(slot *uint64, _ uint64, _ uint16) bool { *slot = 1; return true }
func (Hooks) Condition(_ uint64, _ uint16, value bool) bool { return value }
func (Hooks) End(_ uint64, value bool) bool { return value }
func (Hooks) AbortSlots([]uint64) {}
`)
	if _, err := InstrumentPackage(PackageOptions{
		Context: t.Context(), Directory: filepath.Dir(copied), PackageName: "embedded",
		PackagePath: "example.com/fixture/embedded", RuntimeImportPath: "example.com/fixture/runtimecov",
		ActiveFiles: []string{copied}, Files: []FileMapping{{CopyPath: copied, Analysis: analyzed}},
	}); err != nil {
		t.Fatal(err)
	}
	transformed, err := os.ReadFile(copied)
	if err != nil {
		t.Fatal(err)
	}
	for _, directive := range []string{"//go:embed first.txt", "//go:embed second.txt"} {
		if strings.Count(string(transformed), directive) != 1 {
			t.Fatalf("directive count changed: %s\n%s", directive, transformed)
		}
	}
	command := exec.CommandContext(t.Context(), "go", "test", "./...")
	command.Dir = workspace
	command.Env = append(os.Environ(), "GOWORK=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("compile and run instrumented embed fixture: %v\n%s\n%s", err, output, transformed)
	}
}
