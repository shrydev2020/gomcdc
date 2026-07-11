package analyzer

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	cover "github.com/shrydev2020/gomcdc/internal/coverage"
)

func TestAnalyzeFileDiscoversOnlyPhaseOneDecisions(t *testing.T) {
	t.Parallel()

	moduleDir := t.TempDir()
	path := writeSource(t, moduleDir, "policy/policy.go", `package policy

func Allow(a, b bool) bool {
	if value := 1; a && b {
		return value == 1
	} else if !a {
		return false
	}
	for a {
		break
	}
	for {
		break
	}
	for range []int{1} {
	}
	return false
}

`)

	file, err := AnalyzeFile(FileOptions{
		Path:        path,
		ModuleDir:   moduleDir,
		ModulePath:  "example.com/project",
		PackagePath: "example.com/project/policy",
	})
	if err != nil {
		t.Fatalf("AnalyzeFile() error = %v", err)
	}
	if file.RelativePath != "policy/policy.go" {
		t.Fatalf("RelativePath = %q", file.RelativePath)
	}
	if file.PackageName != "policy" {
		t.Fatalf("PackageName = %q", file.PackageName)
	}
	if file.Generated {
		t.Fatal("Generated = true")
	}
	if got, want := len(file.Decisions), 3; got != want {
		t.Fatalf("len(Decisions) = %d, want %d", got, want)
	}

	wantKinds := []cover.DecisionKind{cover.DecisionIf, cover.DecisionIf, cover.DecisionFor}
	wantExpressions := []string{"a && b", "!a", "a"}
	for index, decision := range file.Decisions {
		if decision.Metadata.Kind != wantKinds[index] {
			t.Errorf("decision[%d].Kind = %q, want %q", index, decision.Metadata.Kind, wantKinds[index])
		}
		if decision.Metadata.Expression != wantExpressions[index] {
			t.Errorf("decision[%d].Expression = %q, want %q", index, decision.Metadata.Expression, wantExpressions[index])
		}
		if decision.Metadata.Function != "Allow" {
			t.Errorf("decision[%d].Function = %q", index, decision.Metadata.Function)
		}
		if decision.Metadata.ID == 0 {
			t.Errorf("decision[%d].ID = 0", index)
		}
		if !strings.HasPrefix(decision.CanonicalKey, StableIDVersion+"\x00") {
			t.Errorf("decision[%d].CanonicalKey lacks version: %q", index, decision.CanonicalKey)
		}
		if decision.Condition.Start >= decision.Condition.End {
			t.Errorf("decision[%d].Condition = %#v", index, decision.Condition)
		}
	}
}

func TestAnalyzeFileBuildsConditionTreesAndSeparateClauseMetadata(t *testing.T) {
	t.Parallel()

	moduleDir := t.TempDir()
	path := writeSource(t, moduleDir, "logic.go", `package logic

func Evaluate(a, b, c bool, value any, ch chan int) {
	if a && (b || c) {}
	if !a {}
	if !(a && b) {}
	switch {
	case a, b && c:
	default:
	}
	switch {
	case c:
	}
	switch value {
	case 1, 2:
		fallthrough
	case 3:
	}
	switch typed := value.(type) {
	case int:
		_ = typed
	}
	select {
	case <-ch:
	default:
	}
}
`)

	file, err := AnalyzeFile(FileOptions{
		Path:        path,
		ModuleDir:   moduleDir,
		ModulePath:  "example.com/logic",
		PackagePath: "example.com/logic",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(file.Decisions), 6; got != want {
		t.Fatalf("len(Decisions) = %d, want %d", got, want)
	}

	first := file.Decisions[0]
	if got, want := len(first.Conditions), 3; got != want {
		t.Fatalf("first condition count = %d, want %d", got, want)
	}
	for index, expression := range []string{"a", "b", "c"} {
		condition := first.Conditions[index]
		if condition.Metadata.Index != uint16(index) || condition.Metadata.Expression != expression {
			t.Errorf("condition[%d] = %#v", index, condition.Metadata)
		}
		if condition.Span.Start >= condition.Span.End {
			t.Errorf("condition[%d] span = %#v", index, condition.Span)
		}
	}
	tree := first.Metadata.ExpressionTree
	if tree == nil || tree.Kind != cover.BooleanExpressionAnd || tree.Right == nil || tree.Right.Kind != cover.BooleanExpressionOr {
		t.Fatalf("first expression tree = %#v", tree)
	}
	if got := file.Decisions[1].Conditions[0].Metadata.Expression; got != "!a" {
		t.Errorf("simple negation atom = %q, want !a", got)
	}
	compoundNot := file.Decisions[2].Metadata.ExpressionTree
	if compoundNot == nil || compoundNot.Kind != cover.BooleanExpressionNot || compoundNot.Left == nil || compoundNot.Left.Kind != cover.BooleanExpressionAnd {
		t.Fatalf("compound negation tree = %#v", compoundNot)
	}

	var conditionless *Clause
	counts := make(map[cover.ClauseKind]int)
	roles := make(map[cover.ClauseRole]int)
	conditionlessGroups := make(map[cover.ClauseGroupID]struct{})
	for index := range file.Clauses {
		clause := &file.Clauses[index]
		if clause.Metadata.GroupID == 0 {
			t.Errorf("clause has zero group ID: %#v", clause.Metadata)
		}
		counts[clause.Metadata.Kind]++
		roles[clause.Metadata.Role]++
		if clause.Metadata.Kind == cover.ClauseConditionlessSwitch && clause.Metadata.Role == cover.ClauseCase {
			conditionlessGroups[clause.Metadata.GroupID] = struct{}{}
			if conditionless == nil || len(clause.DecisionIDs) > len(conditionless.DecisionIDs) {
				conditionless = clause
			}
		}
	}
	if got, want := len(file.Clauses), 8; got != want {
		t.Fatalf("len(Clauses) = %d, want %d: %#v", got, want, counts)
	}
	if counts[cover.ClauseConditionlessSwitch] != 3 || counts[cover.ClauseExpressionSwitch] != 2 || counts[cover.ClauseTypeSwitch] != 1 || counts[cover.ClauseSelect] != 2 {
		t.Errorf("clause kind counts = %#v", counts)
	}
	if got, want := len(file.NoMatches), 2; got != want {
		t.Errorf("no-match selection obligations = %d, want %d", got, want)
	}
	if conditionless == nil || len(conditionless.DecisionIDs) != 2 || len(conditionless.Metadata.DecisionIDs) != 2 {
		t.Fatalf("conditionless clause = %#v", conditionless)
	}
	if len(conditionlessGroups) != 2 {
		t.Errorf("conditionless switch groups = %#v, want 2 distinct constructs", conditionlessGroups)
	}
	if got := len(file.Decisions[4].Conditions); got != 2 {
		t.Errorf("b && c condition count = %d, want 2", got)
	}
}

func TestAnalyzeFileUsesPhysicalPositionsAndNamesMethodsAndLiterals(t *testing.T) {
	t.Parallel()

	moduleDir := t.TempDir()
	path := writeSource(t, moduleDir, "value.go", `package sample

type Value struct{}

//line imaginary.go:900
func (*Value) Check(a bool) {
	if a {}
	_ = func(b bool) bool {
		if b { return true }
		return false
	}
}
`)
	file, err := AnalyzeFile(FileOptions{
		Path:        path,
		ModuleDir:   moduleDir,
		ModulePath:  "example.com/sample",
		PackagePath: "example.com/sample",
	})
	if err != nil {
		t.Fatalf("AnalyzeFile() error = %v", err)
	}
	if got, want := len(file.Decisions), 2; got != want {
		t.Fatalf("len(Decisions) = %d, want %d", got, want)
	}
	if file.Decisions[0].Metadata.Location.Start.Line >= 900 {
		t.Fatalf("Start.Line = %d; //line-adjusted position was used", file.Decisions[0].Metadata.Location.Start.Line)
	}
	if len(file.LineMappings) == 0 {
		t.Fatal("user //line directive was not exposed for C0 reverse mapping")
	}
	lastMapping := file.LineMappings[len(file.LineMappings)-1]
	if filepath.Base(lastMapping.LogicalFile) != "imaginary.go" || lastMapping.LogicalLine < 900 {
		t.Errorf("last line mapping = %#v", lastMapping)
	}
	if file.Decisions[0].Metadata.Function != "*Value.Check" {
		t.Errorf("method Function = %q", file.Decisions[0].Metadata.Function)
	}
	if !strings.HasPrefix(file.Decisions[1].Metadata.Function, "*Value.Check.func@") {
		t.Errorf("literal Function = %q", file.Decisions[1].Metadata.Function)
	}
}

func TestStableIDDoesNotDependOnAbsoluteWorkspaceOrAnalysisOrder(t *testing.T) {
	t.Parallel()

	const source = `package p
func F(value bool) { if value {} }
`
	options := make([]FileOptions, 0, 2)
	for range 2 {
		moduleDir := t.TempDir()
		path := writeSource(t, moduleDir, "internal/p/p.go", source)
		options = append(options, FileOptions{
			Path:        path,
			ModuleDir:   moduleDir,
			ModulePath:  "example.com/stable",
			PackagePath: "example.com/stable/internal/p",
		})
	}
	first, err := AnalyzeFile(options[0])
	if err != nil {
		t.Fatal(err)
	}
	second, err := AnalyzeFile(options[1])
	if err != nil {
		t.Fatal(err)
	}
	if first.Decisions[0].Metadata.ID != second.Decisions[0].Metadata.ID {
		t.Fatalf("IDs differ: %016x != %016x", first.Decisions[0].Metadata.ID, second.Decisions[0].Metadata.ID)
	}
	files, err := AnalyzeFiles([]FileOptions{options[1], options[0]})
	if err != nil {
		t.Fatal(err)
	}
	if files[0].Decisions[0].Metadata.ID != files[1].Decisions[0].Metadata.ID {
		t.Fatal("AnalyzeFiles result depends on order")
	}
}

func TestGeneratedFileRemainsInCoverageAndIdentifiersAreAvailable(t *testing.T) {
	t.Parallel()

	moduleDir := t.TempDir()
	path := writeSource(t, moduleDir, "generated.go", `// Code generated by fixture. DO NOT EDIT.

package p

func __gomcdcHooks(value bool) {
	if value {}
}
`)
	file, err := AnalyzeFile(FileOptions{
		Path:        path,
		ModuleDir:   moduleDir,
		ModulePath:  "example.com/p",
		PackagePath: "example.com/p",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !file.Generated {
		t.Fatal("Generated = false")
	}
	if len(file.Decisions) != 1 {
		t.Fatalf("len(Decisions) = %d, want 1", len(file.Decisions))
	}
	if !slices.Contains(file.Identifiers, "__gomcdcHooks") {
		t.Fatalf("Identifiers = %v", file.Identifiers)
	}
}

func TestDetectCollisions(t *testing.T) {
	t.Parallel()

	files := []File{
		{Decisions: []Decision{{Metadata: cover.DecisionMetadata{ID: 7}, CanonicalKey: "first"}}},
		{Decisions: []Decision{{Metadata: cover.DecisionMetadata{ID: 7}, CanonicalKey: "second"}}},
	}
	err := DetectCollisions(files)
	var collision *CollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("DetectCollisions() error = %v, want CollisionError", err)
	}
	if collision.ID != 7 || collision.FirstKey != "first" || collision.SecondKey != "second" {
		t.Fatalf("collision = %#v", collision)
	}
}

func TestAnalyzeFileRejectsSourceOutsideModule(t *testing.T) {
	t.Parallel()

	moduleDir := t.TempDir()
	outsideDir := t.TempDir()
	path := writeSource(t, outsideDir, "p.go", "package p\n")
	_, err := AnalyzeFile(FileOptions{
		Path:        path,
		ModuleDir:   moduleDir,
		ModulePath:  "example.com/p",
		PackagePath: "example.com/p",
	})
	if err == nil || !strings.Contains(err.Error(), "outside module") {
		t.Fatalf("AnalyzeFile() error = %v", err)
	}
}

func writeSource(t *testing.T, root, relative, source string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
