package runtimecov

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	cover "github.com/shrydev2020/gomcdc/internal/coverage"
)

func TestInjectCreatesDistinctGeneratedInternalPackages(t *testing.T) {
	moduleDir := t.TempDir()
	canonicalModuleDir, err := filepath.EvalSymlinks(moduleDir)
	if err != nil {
		t.Fatal(err)
	}
	existingDir := filepath.Join(moduleDir, "internal", directoryPrefix+"existing")
	if err := os.MkdirAll(existingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(existingDir, "sentinel")
	if err := os.WriteFile(sentinel, []byte("unchanged"), 0o644); err != nil {
		t.Fatal(err)
	}

	first, err := Inject(moduleDir, "example.test/project")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Inject(moduleDir, "example.test/project")
	if err != nil {
		t.Fatal(err)
	}
	if first.Dir == second.Dir {
		t.Fatalf("two injections selected the same directory %q", first.Dir)
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "unchanged" {
		t.Fatalf("sentinel changed: %q, %v", got, err)
	}
	for _, injected := range []Package{first, second} {
		if got, want := filepath.Dir(injected.Dir), filepath.Join(canonicalModuleDir, "internal"); got != want {
			t.Errorf("runtime parent = %q, want %q", got, want)
		}
		source, err := os.ReadFile(injected.SourceFile)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := format.Source(source); err != nil {
			t.Fatalf("generated runtime is not formatted Go: %v", err)
		}
		file, err := parser.ParseFile(token.NewFileSet(), injected.SourceFile, source, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		if !ast.IsGenerated(file) {
			t.Error("injected runtime is not marked generated")
		}
		for _, signature := range []string{
			"func NewHooks(packagePath string) Hooks",
			"func BeginDecision(decisionID uint64, conditionCount uint16)",
			"func EvalCondition(evaluationID uint64, index uint16, value bool)",
			"func EndDecision(evaluationID uint64, value bool)",
			"func AbortDecision(evaluationID uint64)",
			"func (hooks Hooks) BeginInto",
			"func (hooks Hooks) AbortSlots",
		} {
			if !strings.Contains(string(source), signature) {
				t.Errorf("generated runtime lacks %q", signature)
			}
		}
	}
}

func TestInjectRejectsInvalidLocations(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct{ moduleDir, modulePath string }{
		{},
		{moduleDir: file, modulePath: "example.test/project"},
		{moduleDir: t.TempDir()},
		{moduleDir: t.TempDir(), modulePath: "example.test/project/"},
		{moduleDir: t.TempDir(), modulePath: `example.test\project`},
	}
	for _, test := range tests {
		if _, err := Inject(test.moduleDir, test.modulePath); err == nil {
			t.Errorf("Inject(%q, %q) error = nil", test.moduleDir, test.modulePath)
		}
	}
}

func TestInjectRejectsSymlinkedInternalDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation commonly requires elevated privileges")
	}
	moduleDir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(moduleDir, "internal")); err != nil {
		t.Fatal(err)
	}
	if _, err := Inject(moduleDir, "example.test/project"); err == nil {
		t.Fatal("Inject() accepted symlinked internal directory")
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("symlink target entries = %v, %v", entries, err)
	}
}

func TestCollectDetailedRetainsValidEvidenceAndSynthesizesAbort(t *testing.T) {
	dataDir := t.TempDir()
	content := strings.Join([]string{
		`{"type":"begin","runId":"run-a","packagePath":"example.test/p","processId":101,"evaluationId":1,"decisionId":7,"conditionCount":2,"testId":"unknown"}`,
		`{"type":"terminal","runId":"run-a","packagePath":"example.test/p","processId":101,"evaluationId":1,"decisionId":7,"testId":"unknown","conditions":[2,0],"result":true,"status":0,"skippedDecisionIds":[8]}`,
		`{"type":"evaluation","runId":"run-a","packagePath":"example.test/p","processId":101,"evaluationId":3,"decisionId":7,"testId":"TestRepeat","conditions":[2,0],"result":true,"status":0,"skippedDecisionIds":[8]}`,
		`{"type":"begin","runId":"run-a","packagePath":"example.test/p","processId":101,"evaluationId":2,"decisionId":7,"conditionCount":2,"testId":"unknown"}`,
		`{"type":"clause","runId":"run-a","packagePath":"example.test/p","processId":101,"clauseId":55,"event":"direct-selection"}`,
		`{"type":"clause","runId":"run-a","packagePath":"example.test/p","processId":101,"clauseId":55,"event":"body-execution"}`,
		`{"type":"clause","runId":"run-a","packagePath":"example.test/p","processId":101,"switchId":77,"event":"no-match-selection"}`,
		`{"type":"terminal","unknown":1}`,
	}, "\n") + "\n" + `{"type":"terminal"`
	writeEventFile(t, dataDir, "fixture.jsonl", content)

	collected, err := CollectDetailed(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(collected.Evaluations), 2; got != want {
		t.Fatalf("evaluations = %d, want %d: %#v", got, want, collected.Evaluations)
	}
	completed := collected.Evaluations[0]
	if completed.Status != cover.EvaluationCompleted || !completed.Result || fmt.Sprint(completed.Conditions) != fmt.Sprint([]cover.ConditionState{cover.ConditionTrue, cover.ConditionNotEvaluated}) {
		t.Errorf("completed = %#v", completed)
	}
	aborted := collected.Evaluations[1]
	if aborted.Status != cover.EvaluationAborted || aborted.Result {
		t.Errorf("aborted = %#v", aborted)
	}
	if got := len(collected.Clauses); got != 3 || collected.Clauses[0].SwitchID != 77 || collected.Clauses[0].ClauseID != 0 || collected.Clauses[0].Event != cover.ClauseNoMatchSelection || collected.Clauses[1].ClauseID != 55 || collected.Clauses[1].Event != cover.ClauseBodyExecution || collected.Clauses[2].Event != cover.ClauseDirectSelection {
		t.Errorf("clauses = %#v", collected.Clauses)
	}
	if got := collected.NotEvaluatedDecisions; len(got) != 1 || got[0].DecisionID != 8 || got[0].CauseDecisionID != 7 || got[0].CauseEvaluationID != 1 {
		t.Errorf("not-evaluated decisions = %#v", got)
	}
	if got := len(collected.Diagnostics); got != 2 {
		t.Fatalf("diagnostics = %#v", collected.Diagnostics)
	}
	if collected.Diagnostics[0].Severity != DiagnosticIntegrity || collected.Diagnostics[1].Severity != DiagnosticRecoverable || !collected.Diagnostics[1].Truncated {
		t.Errorf("truncated diagnostic = %#v", collected.Diagnostics[1])
	}
}

func TestCollectMissingDirectoryIsIOError(t *testing.T) {
	_, err := CollectDetailed(filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatal("CollectDetailed() error = nil")
	}
}

func TestCollectDetailedAcceptsRecordsLargerThanSixtyFourKiB(t *testing.T) {
	dataDir := t.TempDir()
	conditions := make([]uint8, 65535)
	for index := range conditions {
		conditions[index] = uint8(cover.ConditionTrue)
	}
	begin, err := json.Marshal(wireRecord{Type: "begin", RunID: "large", PackagePath: "example.test/large", ProcessID: 7, EvaluationID: 1, DecisionID: 2, ConditionCount: len(conditions), TestID: cover.UnknownTestID})
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := json.Marshal(wireRecord{Type: "terminal", RunID: "large", PackagePath: "example.test/large", ProcessID: 7, EvaluationID: 1, DecisionID: 2, TestID: cover.UnknownTestID, Conditions: conditions, Result: true, Status: uint8(cover.EvaluationCompleted)})
	if err != nil {
		t.Fatal(err)
	}
	if len(terminal) <= 64*1024 {
		t.Fatalf("fixture record is only %d bytes", len(terminal))
	}
	writeEventFile(t, dataDir, "large.jsonl", string(begin)+"\n"+string(terminal)+"\n")
	collected, err := CollectDetailed(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(collected.Diagnostics) != 0 || len(collected.Evaluations) != 1 || len(collected.Evaluations[0].Conditions) != len(conditions) {
		t.Fatalf("large collection = evaluations:%d diagnostics:%#v", len(collected.Evaluations), collected.Diagnostics)
	}
}

func TestCollectDetailedRejectsTerminalWithoutBeginAsCoverageEvidence(t *testing.T) {
	dataDir := t.TempDir()
	writeEventFile(t, dataDir, "orphan.jsonl", `{"type":"terminal","runId":"run","packagePath":"example.test/p","processId":1,"evaluationId":9,"decisionId":7,"conditions":[2],"result":true,"status":0}`+"\n")
	collected, err := CollectDetailed(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(collected.Evaluations) != 0 {
		t.Fatalf("orphan terminal became coverage evidence: %#v", collected.Evaluations)
	}
	if len(collected.Diagnostics) != 1 || !strings.Contains(collected.Diagnostics[0].Message, "no matching begin") {
		t.Fatalf("diagnostics = %#v", collected.Diagnostics)
	}
}

func TestCollectDetailedAggregatesJournalAndCompactedEvaluationRecords(t *testing.T) {
	dataDir := t.TempDir()
	content := strings.Join([]string{
		`{"type":"begin","runId":"run","packagePath":"example.test/p","processId":1,"evaluationId":1,"decisionId":7,"conditionCount":2,"testId":"unknown"}`,
		`{"type":"terminal","runId":"run","packagePath":"example.test/p","processId":1,"evaluationId":1,"decisionId":7,"testId":"unknown","conditions":[2,1],"result":false,"status":0}`,
		`{"type":"evaluation","runId":"run","packagePath":"example.test/p","processId":2,"evaluationId":9,"decisionId":7,"testId":"unknown","conditions":[2,1],"result":false,"status":0}`,
		`{"type":"evaluation","runId":"run","packagePath":"example.test/p","processId":2,"evaluationId":10,"decisionId":7,"testId":"named-test","conditions":[2,2],"result":true,"status":0}`,
	}, "\n") + "\n"
	writeEventFile(t, dataDir, "mixed.jsonl", content)

	collected, err := CollectDetailed(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(collected.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v", collected.Diagnostics)
	}
	if got, want := len(collected.Evaluations), 2; got != want {
		t.Fatalf("evaluations = %d, want %d: %#v", got, want, collected.Evaluations)
	}
	if got := fmt.Sprint(collected.Evaluations[0].Conditions); got != fmt.Sprint([]cover.ConditionState{cover.ConditionTrue, cover.ConditionFalse}) {
		t.Errorf("first conditions = %s", got)
	}
	if !collected.Evaluations[1].Result || collected.Evaluations[1].TestID != "named-test" {
		t.Errorf("second evaluation = %#v", collected.Evaluations[1])
	}
}

func TestInjectedRuntimeConcurrentEvaluationsHaveUniqueIDsAndProvenance(t *testing.T) {
	moduleDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte("module example.test/probe\n\ngo 1.26.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	injected, err := Inject(moduleDir, "example.test/probe")
	if err != nil {
		t.Fatal(err)
	}
	probeDir := filepath.Join(moduleDir, "cmd", "probe")
	if err := os.MkdirAll(probeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	longPackage := "example.test/" + strings.Repeat("very-long-segment/", 40) + "logic"
	probeSource := fmt.Sprintf(`package main

import (
	"sync"
	runtimecov %q
)

func main() {
	hooks := runtimecov.NewHooks("example.test/probe/logic")
	var group sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			for call := 0; call < 100; call++ {
				var slot uint64
				if hooks.BeginInto(&slot, 7, 2) && hooks.End(slot,
					hooks.Condition(slot, 0, worker%%2 == 0) &&
					hooks.Condition(slot, 1, call%%2 == 0)) {}
			}
		}(worker)
	}
	group.Wait()
	var aborted uint64
	hooks.BeginInto(&aborted, 9, 1)
	hooks.Condition(aborted, 0, true)
	hooks.AbortSlots([]uint64{aborted})
	longHooks := runtimecov.NewHooks(%q)
	var longSlot uint64
	longHooks.BeginInto(&longSlot, 11, 1)
	longHooks.Condition(longSlot, 0, true)
	longHooks.End(longSlot, true)
}
`, injected.ImportPath, longPackage)
	if err := os.WriteFile(filepath.Join(probeDir, "main.go"), []byte(probeSource), 0o644); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	command := exec.Command("go", "run", "-race", "./cmd/probe")
	command.Dir = moduleDir
	command.Env = environmentWith(DataDirEnv, dataDir)
	command.Env = environmentReplace(command.Env, RunIDEnv, "run-123")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("probe failed: %v\n%s", err, output)
	}

	collected, err := CollectDetailed(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(collected.Diagnostics) != 0 {
		t.Fatalf("concurrent runtime diagnostics = %#v", collected.Diagnostics)
	}
	if got, want := len(collected.Evaluations), 5; got != want {
		t.Fatalf("evaluations = %d, want %d; diagnostics=%#v", got, want, collected.Diagnostics)
	}
	seen := make(map[cover.EvaluationIdentity]struct{})
	aborted := 0
	longSeen := false
	for _, evaluation := range collected.Evaluations {
		identity := evaluation.Identity()
		if _, exists := seen[identity]; exists {
			t.Fatalf("duplicate evaluation identity %#v", identity)
		}
		seen[identity] = struct{}{}
		if evaluation.Status == cover.EvaluationAborted {
			aborted++
		}
		if evaluation.PackagePath == longPackage {
			longSeen = true
		}
		if evaluation.RunID != "run-123" || (evaluation.PackagePath != "example.test/probe/logic" && evaluation.PackagePath != longPackage) || evaluation.ProcessID <= 0 {
			t.Errorf("provenance = %#v", identity)
		}
	}
	if aborted != 1 {
		t.Errorf("aborted evaluations = %d, want 1", aborted)
	}
	if !longSeen {
		t.Error("long package path was not retained in event provenance")
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("event files = %v, %v", entries, err)
	}
	wantPackage := base64.RawURLEncoding.EncodeToString([]byte("example.test/probe/logic"))
	wantRun := base64.RawURLEncoding.EncodeToString([]byte("run-123"))
	standardFound := false
	for _, entry := range entries {
		name := entry.Name()
		if len(name) > 255 {
			t.Errorf("event filename exceeds NAME_MAX-sized bound: %d", len(name))
		}
		if strings.Contains(name, wantPackage) && strings.Contains(name, "-pid_") && strings.Contains(name, "-run_"+wantRun+"-") {
			standardFound = true
		}
	}
	if !standardFound {
		t.Errorf("event filenames lack readable standard-package provenance: %v", entries)
	}
}

func TestInjectedRuntimeCompactsDuplicateHistoryWithoutDroppingUniqueVectorsOrActiveBegins(t *testing.T) {
	moduleDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte("module example.test/compactprobe\n\ngo 1.26.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	injected, err := Inject(moduleDir, "example.test/compactprobe")
	if err != nil {
		t.Fatal(err)
	}
	mainSource := fmt.Sprintf(`package main
import runtimecov %q
func main() {
	h := runtimecov.NewHooks("example.test/compactprobe/logic")
	for iteration := 0; iteration < 10000; iteration++ {
		var slot uint64
		h.BeginInto(&slot, 7, 1)
		h.Condition(slot, 0, true)
		h.EndSelect(slot, true, 8, 9)
	}
	for vector := 0; vector < 300; vector++ {
		var slot uint64
		h.BeginInto(&slot, 9, 9)
		for condition := 0; condition < 9; condition++ {
			h.Condition(slot, uint16(condition), vector&(1<<condition) != 0)
		}
		h.End(slot, vector&1 != 0)
	}
	var interrupted uint64
	h.BeginInto(&interrupted, 11, 1)
	h.Condition(interrupted, 0, true)
}
`, injected.ImportPath)
	if err := os.WriteFile(filepath.Join(moduleDir, "main.go"), []byte(mainSource), 0o644); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	command := exec.Command("go", "run", ".")
	command.Dir = moduleDir
	command.Env = environmentWith(DataDirEnv, dataDir)
	command.Env = environmentReplace(command.Env, RunIDEnv, "compact-run")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("compact probe failed: %v\n%s", err, output)
	}

	entries, err := os.ReadDir(dataDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("event files = %v, %v", entries, err)
	}
	contents, err := os.ReadFile(filepath.Join(dataDir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	// The compacted file contains one record per unique completed vector, any
	// active begin, and at most one compaction interval of append-only tail.
	if records := strings.Count(string(contents), "\n"); records > 301+1+256 {
		t.Fatalf("compacted event records = %d; duplicate history was not bounded", records)
	}

	collected, err := CollectDetailed(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(collected.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v", collected.Diagnostics)
	}
	if got, want := len(collected.Evaluations), 302; got != want {
		t.Fatalf("unique evaluations = %d, want %d", got, want)
	}
	byDecision := make(map[cover.DecisionID]int)
	aborted := 0
	for _, evaluation := range collected.Evaluations {
		byDecision[evaluation.DecisionID]++
		if evaluation.Status == cover.EvaluationAborted {
			aborted++
		}
	}
	if byDecision[7] != 1 || byDecision[9] != 300 || byDecision[11] != 1 || aborted != 1 {
		t.Fatalf("collected vectors = %#v, aborted=%d", byDecision, aborted)
	}
	if got := collected.NotEvaluatedDecisions; len(got) != 2 || got[0].DecisionID != 8 || got[1].DecisionID != 9 || got[0].CauseEvaluationID != got[1].CauseEvaluationID {
		t.Fatalf("compacted skip evidence = %#v", got)
	}
}

func TestInjectedRuntimeSwallowsRecorderFailuresAndPreservesValues(t *testing.T) {
	moduleDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte("module example.test/failureprobe\n\ngo 1.26.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	injected, err := Inject(moduleDir, "example.test/failureprobe")
	if err != nil {
		t.Fatal(err)
	}
	mainSource := fmt.Sprintf(`package main
import runtimecov %q
func main() {
	h := runtimecov.NewHooks("example.test/failureprobe")
	var slot uint64
	if !h.BeginInto(&slot, 1, 1) { panic("begin changed") }
	if !h.Condition(slot, 0, true) { panic("condition changed") }
	if !h.End(slot, true) { panic("end changed") }
}
`, injected.ImportPath)
	if err := os.WriteFile(filepath.Join(moduleDir, "main.go"), []byte(mainSource), 0o644); err != nil {
		t.Fatal(err)
	}
	notDirectory := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notDirectory, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "run", ".")
	command.Dir = moduleDir
	command.Env = environmentWith(DataDirEnv, notDirectory)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("recorder failure changed behavior: %v\n%s", err, output)
	} else if !strings.Contains(string(output), "gomcdc runtime diagnostic") {
		t.Fatalf("recorder failure was not detectable: %s", output)
	}
}

func writeEventFile(t *testing.T, dataDir, suffix, content string) {
	t.Helper()
	path := filepath.Join(dataDir, eventFilePrefix+suffix)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func environmentWith(key, value string) []string {
	return environmentReplace(os.Environ(), key, value)
}

func environmentReplace(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	result = append(result, prefix+value)
	sort.Strings(result)
	return result
}
