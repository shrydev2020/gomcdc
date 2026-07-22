package instrument

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/internal/analyzer"
	"github.com/shrydev2020/gomcdc/internal/c0"
	cover "github.com/shrydev2020/gomcdc/internal/coverage"
	"github.com/shrydev2020/gomcdc/internal/runtimecov"
)

func TestInstrumentationRejectsCanceledWork(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := SelectHelperName(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("SelectHelperName error = %v, want context.Canceled", err)
	}
	if _, err := InstrumentPackage(PackageOptions{Context: ctx}); !errors.Is(err, context.Canceled) {
		t.Fatalf("InstrumentPackage error = %v, want context.Canceled", err)
	}
}

func TestInstrumentFileRewritesCopiedConditionsOnly(t *testing.T) {
	t.Parallel()

	originalRoot := t.TempDir()
	copyRoot := t.TempDir()
	const source = `//go:build !windows

// Package policy keeps this comment.
package policy

func Allow(a, b bool) bool {
	if value := 1; a && b {
		return value == 1
	} else if !a {
		return false
	}
	for b {
		break
	}
	for {
		break
	}
	return false
}
`
	originalPath := writeFile(t, originalRoot, "policy/policy.go", source)
	copyPath := writeFile(t, copyRoot, "policy/policy.go", source)
	analysis := analyze(t, originalPath, originalRoot, "example.com/project", "example.com/project/policy")

	if err := InstrumentFile(copyPath, analysis, "__fixtureHit"); err != nil {
		t.Fatalf("InstrumentFile() error = %v", err)
	}
	originalAfter, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(originalAfter) != source {
		t.Fatal("original source was modified")
	}
	transformed, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(transformed)
	if !strings.Contains(text, "//go:build !windows\n\n// Package policy keeps this comment.") {
		t.Fatalf("build tag or leading comment was not preserved:\n%s", text)
	}
	if got, want := strings.Count(text, "__fixtureHit.BeginInto("), 3; got != want {
		t.Fatalf("BeginInto call count = %d, want %d\n%s", got, want, text)
	}
	if !strings.Contains(text, "if value := 1; __fixtureHit.BeginInto(") {
		t.Fatalf("if init statement changed unexpectedly:\n%s", text)
	}
	if !strings.Contains(text, "(a) == (0 == 0)") || !strings.Contains(text, "(b) == (0 == 0)") {
		t.Fatalf("condition was not normalized as required:\n%s", text)
	}
	if strings.Contains(text, "(!a) == (0 == 0)") {
		t.Fatalf("negation was recorded as part of the atom instead of the expression tree:\n%s", text)
	}
	if strings.Contains(text, "for __fixtureHit.BeginInto") && strings.Count(text, "for __fixtureHit.BeginInto") != 1 {
		t.Fatalf("unconditional for loop was instrumented:\n%s", text)
	}
	if strings.Contains(text, filepath.ToSlash(copyRoot)) {
		t.Fatalf("temporary copy path leaked into SourcePos output:\n%s", text)
	}
	if !strings.Contains(text, "//line .gomcdc/generated/") {
		t.Fatalf("synthetic statements were not mapped to a generated file:\n%s", text)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), copyPath, transformed, parser.ParseComments|parser.AllErrors); err != nil {
		t.Fatalf("transformed source does not parse: %v", err)
	}
}

func TestInstrumentFileRefusesOriginalHardlinkAndStaleCopy(t *testing.T) {
	t.Parallel()

	originalRoot := t.TempDir()
	const source = "package p\nfunc F(v bool) { if v {} }\n"
	originalPath := writeFile(t, originalRoot, "p.go", source)
	analysis := analyze(t, originalPath, originalRoot, "example.com/p", "example.com/p")

	if err := InstrumentFile(originalPath, analysis, "__hit"); err == nil || !strings.Contains(err.Error(), "original source") {
		t.Fatalf("InstrumentFile(original) error = %v", err)
	}
	hardlink := filepath.Join(t.TempDir(), "hardlink.go")
	if err := os.Link(originalPath, hardlink); err == nil {
		if err := InstrumentFile(hardlink, analysis, "__hit"); err == nil || !strings.Contains(err.Error(), "hardlink") {
			t.Fatalf("InstrumentFile(hardlink) error = %v", err)
		}
	}
	stale := writeFile(t, t.TempDir(), "p.go", source+"// changed\n")
	if err := InstrumentFile(stale, analysis, "__hit"); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("InstrumentFile(stale) error = %v", err)
	}
}

func TestSelectHelperNameScansTestsAndToleratesMalformedSyntax(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	production := writeFile(t, directory, "p.go", `package p
var __gomcdcHooks = 1
`)
	brokenTest := writeFile(t, directory, "p_test.go", `package p
func TestBroken( {
	__gomcdcHooks_1 := 2
`)
	name, err := SelectHelperName(context.Background(), []string{brokenTest, production})
	if err != nil {
		t.Fatalf("SelectHelperName(context.Background(), ) error = %v", err)
	}
	if name != "__gomcdcHooks_2" {
		t.Fatalf("helper name = %q", name)
	}
}

func TestParseLineDirectiveHandlesOptionalColumn(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		input  string
		file   string
		line   int
		column int
	}{
		{input: "//line original.go:42", file: "original.go", line: 42},
		{input: "//line original.go:42:7", file: "original.go", line: 42, column: 7},
		{input: "//line C:/project/original.go:42", file: "C:/project/original.go", line: 42},
	} {
		file, line, column, ok := parseLineDirective(test.input)
		if !ok || file != test.file || line != test.line || column != test.column {
			t.Errorf("parseLineDirective(%q) = %q, %d, %d, %v", test.input, file, line, column, ok)
		}
	}
}

func TestNormalizeOriginalLineDirectiveUsesCopiedFileRelativeIdentity(t *testing.T) {
	t.Parallel()

	if got := normalizeOriginalLineDirective("//line logic/logic.go:42:7", "logic/logic.go"); got != "//line logic.go:42:7" {
		t.Fatalf("normalized directive = %q", got)
	}
	if got := normalizeOriginalLineDirective("//line virtual.go:42", "logic/logic.go"); got != "//line virtual.go:42" {
		t.Fatalf("unrelated directive = %q", got)
	}
}

func TestGeneratedStatementLineKindsRecognizeSplitSelector(t *testing.T) {
	t.Parallel()
	const source = `package p

func f() {
	gomcdc.
//line p.go:30
	// a source-positioned comment can be printed inside a generated selector
//line p.go:4
	SelectClause(1)
	return
}
`
	kinds, err := generatedStatementLineKinds([]byte(source), "gomcdc")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []int{4, 5, 6, 7, 8} {
		if kinds[line] != generatedLineRuntime {
			t.Errorf("line %d kind = %d, want runtime-generated", line, kinds[line])
		}
	}
	if kinds[9] != generatedLineNone {
		t.Errorf("original return line kind = %d, want none", kinds[9])
	}

	mapped, err := mapGeneratedStatements(
		[]byte(source), "gomcdc", "p.go", ".gomcdc/generated/p.go", ".gomcdc/compiler/p.go", []byte(source),
	)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := c0.BuildInventory("p.go", mapped)
	if err != nil {
		t.Fatalf("mapped source is not valid: %v\n%s", err, mapped)
	}
	generated, original := 0, 0
	for _, block := range inventory.Blocks {
		for _, unit := range block.StatementUnits {
			switch unit.ProfileFile {
			case ".gomcdc/generated/p.go":
				generated++
			case "p.go":
				original++
			}
		}
	}
	if generated != 1 || original != 1 {
		t.Fatalf("mapped statement files = generated:%d original:%d, want 1 each\n%s", generated, original, mapped)
	}
}

func TestUnusedLogicalLineRange(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		positions []logicalLine
		width     int
		want      int
		ok        bool
	}{
		{name: "after dense prefix", positions: []logicalLine{{line: 1}, {line: 2}, {line: 3}}, width: 2, want: 4, ok: true},
		{name: "first sufficient gap", positions: []logicalLine{{line: 1}, {line: 4}}, width: 2, want: 2, ok: true},
		{name: "zero width", width: 0, ok: false},
		{name: "integer space exhausted", positions: []logicalLine{{line: int(^uint(0) >> 1)}}, width: int(^uint(0) >> 1), ok: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := unusedLogicalLineRange(test.positions, test.width)
			if got != test.want || ok != test.ok {
				t.Fatalf("unusedLogicalLineRange() = (%d, %v), want (%d, %v)", got, ok, test.want, test.ok)
			}
		})
	}
}

func TestWriteBridgeCreatesGeneratedNormalAndTestOnlyFiles(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		testOnly bool
		suffix   string
		pkg      string
	}{
		{name: "production", suffix: ".go", pkg: "p"},
		{name: "external test", testOnly: true, suffix: "_test.go", pkg: "p_test"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			path, err := WriteBridge(BridgeOptions{
				Directory:         directory,
				PackageName:       test.pkg,
				PackagePath:       "example.com/p",
				RuntimeImportPath: "example.com/p/internal/runtimecov",
				HelperName:        "__hit",
				TestOnly:          test.testOnly,
			})
			if err != nil {
				t.Fatalf("WriteBridge() error = %v", err)
			}
			if !strings.HasSuffix(path, test.suffix) {
				t.Fatalf("bridge path = %q, want suffix %q", path, test.suffix)
			}
			contents, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, contents, parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}
			if !ast.IsGenerated(file) {
				t.Fatalf("bridge is not recognized as generated:\n%s", contents)
			}
			if !bytes.Contains(contents, []byte(`var __hit = __hitRuntime.NewHooks("example.com/p")`)) {
				t.Fatalf("bridge contents:\n%s", contents)
			}
		})
	}
}

func TestInstrumentPackageRejectsMissingPackagePath(t *testing.T) {
	t.Parallel()
	_, err := InstrumentPackage(PackageOptions{
		Directory: t.TempDir(), PackageName: "p", RuntimeImportPath: "example.com/p/internal/runtimecov",
	})
	if err == nil || !strings.Contains(err.Error(), "package path is empty") {
		t.Fatalf("InstrumentPackage() error = %v", err)
	}
}

func TestWriteBridgeDoesNotOverwriteUserFileWithCandidateName(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	existing := filepath.Join(directory, "zz_gomcdc_bridge_0.go")
	const sentinel = "package p\n\nconst userOwned = true\n"
	if err := os.WriteFile(existing, []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}
	created, err := WriteBridge(BridgeOptions{
		Directory:         directory,
		PackageName:       "p",
		PackagePath:       "example.com/p",
		RuntimeImportPath: "example.com/p/internal/runtimecov",
		HelperName:        defaultHelperBase,
	})
	if err != nil {
		t.Fatalf("WriteBridge: %v", err)
	}
	if created == existing {
		t.Fatalf("bridge reused user-owned path %q", created)
	}
	contents, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != sentinel {
		t.Fatalf("user-owned bridge candidate was changed:\n%s", contents)
	}
}

func TestInstrumentPackagePreservesDefinedBoolShortCircuitOrderAndPanic(t *testing.T) {
	if runtime.GOOS == "js" {
		t.Skip("subprocess go test is unavailable")
	}

	originalRoot := t.TempDir()
	workspace := t.TempDir()
	const logicSource = `package logic

type Flag bool

type any = int
type uint64 = string
type uint8 = string

var true = Flag(false)
var calls []string

func probe(name string, value Flag) Flag {
	calls = append(calls, name)
	return value
}

func panicProbe() Flag {
	calls = append(calls, "panic")
	panic("expected")
}

func And(left, right Flag) int {
	if probe("left", left) && probe("right", right) {
		return 1
	}
	return 0
}

func Loop(value Flag) int {
	count := 0
	for probe("loop", value) {
		count++
		break
	}
	return count
}

func Panic() {
	if panicProbe() {}
}

func RecoverDirect() (recovered bool) {
	defer func() {
		if recover() != nil { recovered = 0 == 0 }
	}()
	panic("recover-direct")
}

func LabeledSwitch(value int) int {
	count := 0
	goto Choose
Choose:
	switch selected := value; selected {
	case 1:
		count++
		break Choose
	}
	return count
}

func NamedBoolSwitch(value Flag) int {
	switch marker := 1; {
	case value:
		return marker
	}
	return 0
}
`
	const testSource = `package logic

import (
	"reflect"
	"testing"
)

func TestSemantics(t *testing.T) {
	calls = nil
	if got := And(Flag(0 != 0), Flag(0 == 0)); got != 0 { t.Fatalf("And false = %d", got) }
	if want := []string{"left"}; !reflect.DeepEqual(calls, want) { t.Fatalf("calls = %v", calls) }

	calls = nil
	if got := And(Flag(0 == 0), Flag(0 != 0)); got != 0 { t.Fatalf("And true,false = %d", got) }
	if want := []string{"left", "right"}; !reflect.DeepEqual(calls, want) { t.Fatalf("calls = %v", calls) }

	calls = nil
	if got := Loop(Flag(0 == 0)); got != 1 { t.Fatalf("Loop = %d", got) }
	if want := []string{"loop"}; !reflect.DeepEqual(calls, want) { t.Fatalf("calls = %v", calls) }
}

func TestPanic(t *testing.T) {
	calls = nil
	defer func() {
		if recover() == nil { t.Fatal("Panic did not panic") }
	if want := []string{"panic"}; !reflect.DeepEqual(calls, want) { t.Fatalf("calls = %v", calls) }
	}()
	Panic()
}

func TestDirectRecover(t *testing.T) {
	if !RecoverDirect() { t.Fatal("recover stopped being a direct deferred call") }
}

func TestLabeledSwitch(t *testing.T) {
	if got := LabeledSwitch(1); got != 1 { t.Fatalf("LabeledSwitch = %d", got) }
	if got := NamedBoolSwitch(Flag(0 == 0)); got != 1 { t.Fatalf("NamedBoolSwitch = %d", got) }
}
`
	originalPath := writeFile(t, originalRoot, "logic/logic.go", logicSource)
	copyPath := writeFile(t, workspace, "logic/logic.go", logicSource)
	testPath := writeFile(t, workspace, "logic/logic_test.go", testSource)
	writeFile(t, workspace, "go.mod", "module example.com/fixture\n\ngo 1.26.0\n")
	writeFile(t, workspace, "runtimecov/runtime.go", `package runtimecov
type EvaluationID = uint64
type Hooks struct{}
func NewHooks(string) Hooks { return Hooks{} }
func (Hooks) BeginInto(slot *uint64, _ uint64, _ uint16) bool { *slot = 1; return true }
func (Hooks) Condition(_ uint64, _ uint16, value bool) bool { return value }
func (Hooks) End(_ uint64, value bool) bool { return value }
func (Hooks) EndSelect(_ uint64, value bool, _ ...uint64) bool { return value }
func (Hooks) AbortSlots([]uint64) {}
func (Hooks) SelectClause(uint64, ...uint64) {}
`)
	analysis := analyze(t, originalPath, originalRoot, "example.com/fixture", "example.com/fixture/logic")
	result, err := InstrumentPackage(PackageOptions{
		Directory:         filepath.Dir(copyPath),
		PackageName:       "logic",
		PackagePath:       "example.com/fixture/logic",
		RuntimeImportPath: "example.com/fixture/runtimecov",
		ActiveFiles:       []string{copyPath, testPath},
		Files:             []FileMapping{{CopyPath: copyPath, Analysis: analysis}},
	})
	if err != nil {
		t.Fatalf("InstrumentPackage() error = %v", err)
	}
	if !strings.HasSuffix(result.BridgePath, ".go") || strings.HasSuffix(result.BridgePath, "_test.go") {
		t.Fatalf("BridgePath = %q", result.BridgePath)
	}

	command := exec.Command("go", "test", "./...")
	command.Dir = workspace
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("instrumented fixture failed: %v\n%s", err, output)
	}
}

func TestInstrumentPackageTestOnlyBridgeDoesNotMixExternalPackage(t *testing.T) {
	t.Parallel()

	originalRoot := t.TempDir()
	workspace := t.TempDir()
	const source = "package p_test\nfunc helper(v bool) { if v {} }\n"
	original := writeFile(t, originalRoot, "p/p_test.go", source)
	copyPath := writeFile(t, workspace, "p/p_test.go", source)
	analysis := analyze(t, original, originalRoot, "example.com/p", "example.com/p_test")
	result, err := InstrumentPackage(PackageOptions{
		Directory:         filepath.Dir(copyPath),
		PackageName:       "p_test",
		PackagePath:       "example.com/p_test",
		RuntimeImportPath: "example.com/p/internal/runtimecov",
		TestOnly:          true,
		ActiveFiles:       []string{copyPath},
		Files:             []FileMapping{{CopyPath: copyPath, Analysis: analysis}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(result.BridgePath, "_test.go") {
		t.Fatalf("BridgePath = %q", result.BridgePath)
	}
}

func TestInstrumentPackageExposesUserLineDirectiveMapping(t *testing.T) {
	t.Parallel()
	originalRoot := t.TempDir()
	workspace := t.TempDir()
	const source = `package p

//line imaginary.go:900:7
func Check(value bool) { if value {} }
`
	original := writeFile(t, originalRoot, "p/p.go", source)
	copyPath := writeFile(t, workspace, "p/p.go", source)
	analysis := analyze(t, original, originalRoot, "example.com/p", "example.com/p")
	result, err := InstrumentPackage(PackageOptions{
		Directory:                  filepath.Dir(copyPath),
		PackageName:                "p",
		PackagePath:                "example.com/p",
		RuntimeImportPath:          "example.com/p/internal/runtimecov",
		PlanCoverageCorrespondence: true,
		ActiveFiles:                []string{copyPath},
		Files:                      []FileMapping{{CopyPath: copyPath, Analysis: analysis}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.SourceMaps) != 1 || len(result.SourceMaps[0].LineMappings) == 0 {
		t.Fatalf("line mappings = %#v", result.SourceMaps)
	}
	if len(result.CoveragePlans) != 1 {
		t.Fatalf("coverage plans = %#v", result.CoveragePlans)
	}
	if _, err := result.CoveragePlans[0].Correspondence.ProjectableRegions(); err != nil {
		t.Fatalf("line-directive correspondence is not projectable: %v", err)
	}
	mapping := result.SourceMaps[0].LineMappings[len(result.SourceMaps[0].LineMappings)-1]
	if filepath.Base(mapping.LogicalFile) != "imaginary.go" || mapping.LogicalLine < 900 || mapping.LogicalColumn != 7 {
		t.Errorf("line mapping = %#v", mapping)
	}
}

func TestInstrumentPackagePreservesSameBasenameUserLineDirective(t *testing.T) {
	t.Parallel()
	originalRoot := t.TempDir()
	workspace := t.TempDir()
	const source = `package p

//line p.go:3:7
func Check(value bool) { if value {} }
`
	original := writeFile(t, originalRoot, "p/p.go", source)
	copyPath := writeFile(t, workspace, "p/p.go", source)
	analysis := analyze(t, original, originalRoot, "example.com/p", "example.com/p")
	result, err := InstrumentPackage(PackageOptions{
		Directory:                  filepath.Dir(copyPath),
		PackageName:                "p",
		PackagePath:                "example.com/p",
		RuntimeImportPath:          "example.com/p/internal/runtimecov",
		PlanCoverageCorrespondence: true,
		ActiveFiles:                []string{copyPath},
		Files:                      []FileMapping{{CopyPath: copyPath, Analysis: analysis}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := result.CoveragePlans[0].Correspondence.ProjectableRegions(); err != nil {
		t.Fatalf("same-basename line-directive correspondence is not projectable: %v", err)
	}
	transformed, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, "p/p.go", transformed, parser.ParseComments|parser.AllErrors)
	if err != nil {
		t.Fatalf("parse transformed source: %v\n%s", err, transformed)
	}
	position := fset.PositionFor(parsed.Decls[0].Pos(), true)
	if filepath.Base(position.Filename) != "p.go" || position.Line != 3 || position.Column != 7 {
		t.Fatalf("transformed function position = %s, want basename p.go:3:7\n%s", position, transformed)
	}
}

func TestInstrumentedRuntimeKeepsVectorsIsolatedAndRecordsClauseBodies(t *testing.T) {
	if runtime.GOOS == "js" {
		t.Skip("subprocess go test is unavailable")
	}
	originalRoot := t.TempDir()
	workspace := t.TempDir()
	const source = `package logic

func Vector(a, b, c bool) bool {
	if a && (b || c) { return true }
	return false
}

func Recursive(n int) int {
	if n > 0 && Recursive(n-1) >= 0 { return n }
	return 0
}

func Concurrent(value bool) {
	if value {}
}

func explode() bool { panic("condition panic") }

func PanicDecision(value bool) {
	if value && explode() {}
}

func ExpressionSwitch(value int) {
	switch value {
	case 1:
		fallthrough
	case 2:
	}
}

func ExpressionSwitchDirect(value int) {
	switch value {
	case 1:
	case 2:
	}
}

func BooleanSwitch(a, b bool) {
	switch {
	case a:
	case b && !a:
	}
}

func TypeSwitch(value any) int {
	switch value.(type) {
	case int:
		return 1
	default:
		return 0
	}
}

func TypeNoMatch(value any) {
	switch value.(type) {
	case int:
	}
}

func Select(ch chan int) {
	select {
	case <-ch:
	default:
	}
}
`
	const tests = `package logic

import (
	"sync"
	"testing"
)

func TestCoverageFixture(t *testing.T) {
	Vector(false, true, true)
	Vector(true, true, false)
	Vector(true, false, true)
	Recursive(3)
	var group sync.WaitGroup
	for index := 0; index < 16; index++ {
		group.Add(1)
		go func(value bool) { defer group.Done(); Concurrent(value) }(index%2 == 0)
	}
	group.Wait()
	func() { defer func() { _ = recover() }(); PanicDecision(true) }()
	ExpressionSwitch(1)
	ExpressionSwitch(9)
	ExpressionSwitchDirect(2)
	BooleanSwitch(false, true)
	BooleanSwitch(true, true)
	BooleanSwitch(true, true)
	TypeSwitch(1)
	TypeSwitch("value")
	TypeNoMatch("value")
	ch := make(chan int, 1)
	Select(ch)
	ch <- 1
	Select(ch)
}
`
	originalPath := writeFile(t, originalRoot, "logic/logic.go", source)
	copyPath := writeFile(t, workspace, "logic/logic.go", source)
	testPath := writeFile(t, workspace, "logic/logic_test.go", tests)
	writeFile(t, workspace, "go.mod", "module example.com/fixture\n\ngo 1.26.0\n")
	analysis := analyze(t, originalPath, originalRoot, "example.com/fixture", "example.com/fixture/logic")
	injected, err := runtimecov.Inject(context.Background(), workspace, "example.com/fixture")
	if err != nil {
		t.Fatal(err)
	}
	result, err := InstrumentPackage(PackageOptions{
		Directory:                  filepath.Dir(copyPath),
		PackageName:                "logic",
		PackagePath:                "example.com/fixture/logic",
		RuntimeImportPath:          injected.ImportPath,
		PlanCoverageCorrespondence: true,
		ActiveFiles:                []string{copyPath, testPath},
		Files:                      []FileMapping{{CopyPath: copyPath, Analysis: analysis}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.SourceMaps) != 1 || !strings.HasPrefix(result.SourceMaps[0].GeneratedFile, ".gomcdc/generated/") || !strings.HasPrefix(result.SourceMaps[0].CompilerFile, ".gomcdc/compiler/") || len(result.SourceMaps[0].GeneratedRegions) == 0 {
		t.Fatalf("SourceMaps = %#v", result.SourceMaps)
	}
	if len(result.CoveragePlans) != 1 {
		t.Fatalf("CoveragePlans = %#v", result.CoveragePlans)
	}
	if _, err := result.CoveragePlans[0].Correspondence.ProjectableRegions(); err != nil {
		t.Fatalf("combined coverage plan is not projectable: %v", err)
	}
	if len(result.GeneratedFiles) != 1 || result.GeneratedFiles[0] != result.BridgePath {
		t.Fatalf("GeneratedFiles = %#v", result.GeneratedFiles)
	}
	dataDir := t.TempDir()
	command := exec.Command("go", "test", "./logic")
	command.Dir = workspace
	command.Env = append(os.Environ(),
		"GOWORK=off",
		runtimecov.DataDirEnv+"="+dataDir,
		runtimecov.RunIDEnv+"=fixture-run",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("instrumented fixture failed: %v\n%s", err, output)
	}
	collected, err := runtimecov.CollectDetailed(context.Background(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(collected.Diagnostics) != 0 {
		t.Fatalf("runtime diagnostics = %#v", collected.Diagnostics)
	}

	decisionByFunction := make(map[string]cover.DecisionMetadata)
	for _, decision := range analysis.Decisions {
		decisionByFunction[decision.Metadata.Function] = decision.Metadata
	}
	evaluations := make(map[cover.DecisionID][]cover.DecisionEvaluation)
	identities := make(map[cover.EvaluationIdentity]struct{})
	for _, evaluation := range collected.Evaluations {
		if _, exists := identities[evaluation.Identity()]; exists {
			t.Fatalf("duplicate evaluation identity %#v", evaluation.Identity())
		}
		identities[evaluation.Identity()] = struct{}{}
		evaluations[evaluation.DecisionID] = append(evaluations[evaluation.DecisionID], evaluation)
	}
	vectorID := decisionByFunction["Vector"].ID
	if got := len(evaluations[vectorID]); got != 3 {
		t.Fatalf("Vector evaluations = %d, want 3: %#v", got, evaluations[vectorID])
	}
	wantVectors := map[string]bool{
		fmt.Sprint([]cover.ConditionState{cover.ConditionFalse, cover.ConditionNotEvaluated, cover.ConditionNotEvaluated}): true,
		fmt.Sprint([]cover.ConditionState{cover.ConditionTrue, cover.ConditionTrue, cover.ConditionNotEvaluated}):          true,
		fmt.Sprint([]cover.ConditionState{cover.ConditionTrue, cover.ConditionFalse, cover.ConditionTrue}):                 true,
	}
	for _, evaluation := range evaluations[vectorID] {
		delete(wantVectors, fmt.Sprint(evaluation.Conditions))
	}
	if len(wantVectors) != 0 {
		t.Errorf("missing Vector evaluations: %#v", wantVectors)
	}
	panicEvaluations := evaluations[decisionByFunction["PanicDecision"].ID]
	if len(panicEvaluations) != 1 || panicEvaluations[0].Status != cover.EvaluationAborted || panicEvaluations[0].Conditions[0] != cover.ConditionTrue || panicEvaluations[0].Conditions[1] != cover.ConditionNotEvaluated {
		t.Errorf("panic evaluations = %#v", panicEvaluations)
	}
	if got := len(distinctEvaluationVectors(evaluations[decisionByFunction["Concurrent"].ID])); got != 2 {
		t.Errorf("Concurrent unique vectors = %d, want 2", got)
	}
	recursiveEvaluations := distinctEvaluationVectors(evaluations[decisionByFunction["Recursive"].ID])
	if got := len(recursiveEvaluations); got != 2 {
		t.Errorf("Recursive unique vectors = %d, want 2", got)
	} else {
		baseCases := 0
		for _, evaluation := range recursiveEvaluations {
			if len(evaluation.Conditions) != 2 {
				t.Fatalf("recursive vector was mixed/truncated: %#v", evaluation)
			}
			if evaluation.Conditions[0] == cover.ConditionFalse {
				baseCases++
				if evaluation.Conditions[1] != cover.ConditionNotEvaluated {
					t.Errorf("recursive base case = %#v", evaluation.Conditions)
				}
			}
		}
		if baseCases != 1 {
			t.Errorf("recursive base cases = %d, want 1", baseCases)
		}
	}

	directSelections := make(map[cover.ClauseID]bool)
	bodyExecutions := make(map[cover.ClauseID]bool)
	for _, observation := range collected.ClauseEvents {
		switch observation.Event {
		case cover.ClauseDirectSelection:
			directSelections[observation.ClauseID] = true
		case cover.ClauseBodyExecution:
			bodyExecutions[observation.ClauseID] = true
		}
	}
	var expressionCases []analyzer.Clause
	var directExpressionCases []analyzer.Clause
	for _, clause := range analysis.Clauses {
		switch {
		case clause.Metadata.Function == "ExpressionSwitch" && clause.Metadata.Kind == cover.ClauseExpressionSwitch && clause.Metadata.Role == cover.ClauseCase:
			expressionCases = append(expressionCases, clause)
		}
		if clause.Metadata.Function == "ExpressionSwitchDirect" && clause.Metadata.Role == cover.ClauseCase {
			directExpressionCases = append(directExpressionCases, clause)
		}
	}
	if len(expressionCases) != 2 {
		t.Fatalf("expression switch clauses = %#v", expressionCases)
	}
	if !bodyExecutions[expressionCases[0].Metadata.ID] || !bodyExecutions[expressionCases[1].Metadata.ID] || len(directSelections) != 0 {
		t.Errorf("expression switch body evidence: direct=%#v body=%#v", directSelections, bodyExecutions)
	}
	if len(directExpressionCases) != 2 || bodyExecutions[directExpressionCases[0].Metadata.ID] || !bodyExecutions[directExpressionCases[1].Metadata.ID] {
		t.Errorf("direct-B switch evidence: clauses=%#v direct=%#v body=%#v", directExpressionCases, directSelections, bodyExecutions)
	}
	var booleanCases []analyzer.Clause
	var selectClauses []analyzer.Clause
	var typeClauses []analyzer.Clause
	for _, clause := range analysis.Clauses {
		if clause.Metadata.Function == "BooleanSwitch" && clause.Metadata.Role == cover.ClauseCase {
			booleanCases = append(booleanCases, clause)
		}
		if clause.Metadata.Function == "Select" {
			selectClauses = append(selectClauses, clause)
		}
		if clause.Metadata.Function == "TypeSwitch" {
			typeClauses = append(typeClauses, clause)
		}
	}
	if len(booleanCases) != 2 || !bodyExecutions[booleanCases[0].Metadata.ID] || !bodyExecutions[booleanCases[1].Metadata.ID] {
		t.Errorf("conditionless switch bodies: clauses=%#v direct=%#v body=%#v", booleanCases, directSelections, bodyExecutions)
	}
	if len(selectClauses) != 2 || !bodyExecutions[selectClauses[0].Metadata.ID] || !bodyExecutions[selectClauses[1].Metadata.ID] {
		t.Errorf("select selections: clauses=%#v direct=%#v body=%#v", selectClauses, directSelections, bodyExecutions)
	}
	if len(typeClauses) != 2 || !bodyExecutions[typeClauses[0].Metadata.ID] || !bodyExecutions[typeClauses[1].Metadata.ID] {
		t.Errorf("type-switch bodies: clauses=%#v direct=%#v body=%#v", typeClauses, directSelections, bodyExecutions)
	}
	var booleanDecisions []cover.DecisionMetadata
	for _, decision := range analysis.Decisions {
		if decision.Metadata.Function == "BooleanSwitch" {
			booleanDecisions = append(booleanDecisions, decision.Metadata)
		}
	}
	if len(booleanDecisions) != 2 {
		t.Fatalf("conditionless switch decisions = %#v", booleanDecisions)
	}
	if got := len(distinctEvaluationVectors(evaluations[booleanDecisions[0].ID])); got != 2 {
		t.Errorf("first conditionless decision evaluations = %d, want 2", got)
	}
	if got := len(distinctEvaluationVectors(evaluations[booleanDecisions[1].ID])); got != 1 {
		t.Errorf("second conditionless decision evaluations = %d, want 1", got)
	}
	if got := len(collected.NotEvaluatedDecisions); got != 2 {
		t.Errorf("conditionless switch not-evaluated evidence = %#v", collected.NotEvaluatedDecisions)
	} else {
		for _, observation := range collected.NotEvaluatedDecisions {
			if observation.DecisionID != booleanDecisions[1].ID || observation.CauseDecisionID != booleanDecisions[0].ID {
				t.Errorf("conditionless switch not-evaluated evidence = %#v", collected.NotEvaluatedDecisions)
			}
		}
	}
}

func distinctEvaluationVectors(evaluations []cover.DecisionEvaluation) []cover.DecisionEvaluation {
	result := make([]cover.DecisionEvaluation, 0, len(evaluations))
	seen := make(map[string]struct{}, len(evaluations))
	for _, evaluation := range evaluations {
		key := fmt.Sprintf("%v:%t:%d", evaluation.Conditions, evaluation.Result, evaluation.Status)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, evaluation)
	}
	return result
}

func TestCombinedInstrumentationProducesCoverAndASTEvidenceInOneExecution(t *testing.T) {
	originalRoot := t.TempDir()
	workspace := t.TempDir()
	const source = `package logic

func Decide(left, right bool) bool {
	if left && right {
		return true
	}
	return false
}
`
	const plainSource = `package logic

func Plain() int { return 1 }
`
	const tests = `package logic

import (
	"os"
	"testing"
)

func TestDecide(t *testing.T) {
	marker := os.Getenv("GOMCDC_COMBINED_MARKER")
	file, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("run\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if !Decide(true, true) || Decide(true, false) || Plain() != 1 {
		t.Fatal("unexpected decision result")
	}
}
`
	originalPath := writeFile(t, originalRoot, "logic/logic.go", source)
	copyPath := writeFile(t, workspace, "logic/logic.go", source)
	plainOriginalPath := writeFile(t, originalRoot, "logic/plain.go", plainSource)
	plainCopyPath := writeFile(t, workspace, "logic/plain.go", plainSource)
	testPath := writeFile(t, workspace, "logic/logic_test.go", tests)
	writeFile(t, workspace, "go.mod", "module example.com/fixture\n\ngo 1.26.0\n")
	analysis := analyze(t, originalPath, originalRoot, "example.com/fixture", "example.com/fixture/logic")
	plainAnalysis := analyze(t, plainOriginalPath, originalRoot, "example.com/fixture", "example.com/fixture/logic")
	injected, err := runtimecov.Inject(t.Context(), workspace, "example.com/fixture")
	if err != nil {
		t.Fatal(err)
	}
	result, err := InstrumentPackage(PackageOptions{
		Context: t.Context(), Directory: filepath.Dir(copyPath), PackageName: "logic",
		PackagePath: "example.com/fixture/logic", RuntimeImportPath: injected.ImportPath,
		PlanCoverageCorrespondence: true,
		ActiveFiles:                []string{copyPath, plainCopyPath, testPath},
		Files: []FileMapping{
			{CopyPath: copyPath, Analysis: analysis},
			{CopyPath: plainCopyPath, Analysis: plainAnalysis},
		},
	})
	if err != nil {
		t.Fatalf("InstrumentPackage: %v", err)
	}
	if len(result.SourceMaps) != 1 || len(result.CoveragePlans) != 2 {
		t.Fatalf("instrumentation result = %#v", result)
	}
	var projectable []c0.RegionCorrespondence
	for _, plan := range result.CoveragePlans {
		if len(plan.Correspondence.Regions()) == 0 {
			t.Fatalf("combined coverage plan has no region: %#v", plan)
		}
		regions, err := plan.Correspondence.ProjectableRegions()
		if err != nil {
			t.Fatalf("combined coverage plan is not projectable: %v", err)
		}
		projectable = append(projectable, regions...)
	}
	if len(projectable) == 0 {
		t.Fatal("combined coverage plan has no projectable original region")
	}

	dataDir := t.TempDir()
	profilePath := filepath.Join(t.TempDir(), "combined.out")
	markerPath := filepath.Join(t.TempDir(), "executions.txt")
	command := exec.Command("go", "test", "-count=1", "-covermode=set", "-coverprofile="+profilePath, "-coverpkg=example.com/fixture/logic", "./logic")
	command.Dir = workspace
	command.Env = append(os.Environ(),
		"GOWORK=off",
		"GOCACHE="+filepath.Join(t.TempDir(), "go-cache"),
		"GOMCDC_COMBINED_MARKER="+markerPath,
		runtimecov.DataDirEnv+"="+dataDir,
		runtimecov.RunIDEnv+"=combined-run",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("combined go test failed: %v\n%s", err, output)
	}

	marker, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read execution marker: %v", err)
	}
	if string(marker) != "run\n" {
		t.Fatalf("test execution marker = %q, want one execution", marker)
	}

	profileContents, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("read cover profile: %v", err)
	}
	profile, parseErr := c0.ParseProfile(bytes.NewReader(profileContents))
	if parseErr != nil {
		t.Fatalf("ParseProfile: %v\n%s", parseErr, profileContents)
	}
	for _, planned := range projectable {
		matched := false
		for _, file := range profile.Files {
			profilePath := filepath.ToSlash(file.Path)
			if profilePath != planned.Region.ProfilePath && !strings.HasSuffix(profilePath, "/"+planned.Region.ProfilePath) {
				continue
			}
			for _, block := range file.Blocks {
				if block.Position == planned.Region.Range && block.Statements == planned.Region.Statements {
					matched = true
					break
				}
			}
		}
		if !matched {
			t.Fatalf("planned projectable region missing from actual profile: %#v\nprofile: %#v", planned.Region, profile)
		}
	}
	hasCoveredRegion := false
	for _, file := range profile.Files {
		for _, block := range file.Blocks {
			if block.Count > 0 {
				hasCoveredRegion = true
				break
			}
		}
	}
	if !hasCoveredRegion {
		t.Fatalf("combined cover profile has no covered region: %#v", profile)
	}
	collected, err := runtimecov.CollectDetailed(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("CollectDetailed: %v", err)
	}
	if len(collected.Evaluations) != 2 {
		t.Fatalf("AST evaluations = %d, want 2: %#v", len(collected.Evaluations), collected.Evaluations)
	}
}

func analyze(t *testing.T, path, moduleDir, modulePath, packagePath string) analyzer.File {
	t.Helper()
	file, err := analyzer.AnalyzeFile(analyzer.FileOptions{
		Path:        path,
		ModuleDir:   moduleDir,
		ModulePath:  modulePath,
		PackagePath: packagePath,
	})
	if err != nil {
		t.Fatalf("AnalyzeFile() error = %v", err)
	}
	return file
}

func writeFile(t *testing.T, root, relative, contents string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
