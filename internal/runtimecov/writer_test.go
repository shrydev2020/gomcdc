package runtimecov

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
)

type writerStats struct {
	RequestedWriteCalls uint64 `json:"requestedWriteCalls"`
	RequestedWriteBytes uint64 `json:"requestedWriteBytes"`
	Compactions         uint64 `json:"compactions"`
	Syncs               uint64 `json:"syncs"`
}

type writerProbeResult struct {
	stats      writerStats
	journal    RecordedEvidence
	finalBytes int64
}

func TestInjectedRuntimeWriterAccounting(t *testing.T) {
	result := runWriterProbe(t, runtimeSourceWithWriterStats(t))
	t.Logf(
		"requested writes=%d bytes=%d compactions=%d syncs=%d final=%d",
		result.stats.RequestedWriteCalls,
		result.stats.RequestedWriteBytes,
		result.stats.Compactions,
		result.stats.Syncs,
		result.finalBytes,
	)

	if got := result.stats.Compactions; got == 0 || got > 4 {
		t.Fatalf("compactions = %d, want 1..4 adaptive checkpoints", got)
	}
	if got := result.stats.Syncs; got != 0 {
		t.Fatalf("syncs = %d, want none for process-failure durability", got)
	}
	if got := result.stats.RequestedWriteCalls; got < 20_601 || got > 20_611 {
		t.Fatalf("requested write calls = %d, want direct events plus bounded snapshot rewrites", got)
	}
	if result.stats.RequestedWriteBytes <= uint64(result.finalBytes) {
		t.Fatalf("requested write bytes = %d, final bytes = %d; accounting missed rewrites", result.stats.RequestedWriteBytes, result.finalBytes)
	}
	if result.finalBytes > 2<<20 {
		t.Fatalf("final bytes = %d, want bounded snapshot plus append tail", result.finalBytes)
	}
	assertWriterProbeEvidence(t, result.journal)
}

func TestInjectedRuntimeCompactionFailureBacksOffWithoutChangingEvidence(t *testing.T) {
	source := runtimeSourceWithWriterStats(t)
	old := "\tif err := os.Rename(temporaryPath, state.path); err != nil {\n"
	replacement := "\tif err := fmt.Errorf(\"injected rename failure\"); err != nil {\n"
	if count := strings.Count(source, old); count != 1 {
		t.Fatalf("rename failure insertion target count = %d, want 1", count)
	}
	source = strings.Replace(source, old, replacement, 1)

	result := runWriterProbe(t, source)
	if got := result.stats.Compactions; got == 0 || got > 3 {
		t.Fatalf("failed compactions = %d, want bounded retry with backoff", got)
	}
	if got := result.stats.Syncs; got != 0 {
		t.Fatalf("failed compaction syncs = %d, want none", got)
	}
	if got := result.journal.Diagnostics; len(got) != 1 || got[0].Severity != DiagnosticIntegrity || !strings.Contains(got[0].Message, "injected rename failure") {
		t.Fatalf("compaction failure diagnostics = %#v", got)
	}
	result.journal.Diagnostics = nil
	assertWriterProbeEvidence(t, result.journal)
}

func TestInjectedRuntimeFailedTerminalRemainsAbortedAcrossCompaction(t *testing.T) {
	source := runtimeSourceWithWriterStats(t)
	old := "\twritten, err := file.Write(line)\n"
	replacement := "\tif event.Type == \"terminal\" && event.EvaluationID == 1 { return 0, fmt.Errorf(\"injected terminal failure\") }\n\twritten, err := file.Write(line)\n"
	if count := strings.Count(source, old); count != 1 {
		t.Fatalf("terminal failure insertion target count = %d, want 1", count)
	}
	source = strings.Replace(source, old, replacement, 1)

	result := runWriterProbe(t, source)
	if got := result.journal.Diagnostics; len(got) != 1 || got[0].Severity != DiagnosticIntegrity || !strings.Contains(got[0].Message, "injected terminal failure") {
		t.Fatalf("terminal failure diagnostics = %#v", got)
	}
	foundAborted := false
	for _, evaluation := range result.journal.Evaluations {
		if evaluation.EvaluationID != 1 {
			continue
		}
		if evaluation.Status != cover.EvaluationAborted {
			t.Fatalf("failed terminal evaluation = %#v, want aborted", evaluation)
		}
		foundAborted = true
	}
	if !foundAborted {
		t.Fatal("failed terminal's committed begin was lost during later compaction")
	}
}

func runtimeSourceWithWriterStats(t testing.TB) string {
	t.Helper()
	source := runtimeSource
	replaceOnce := func(old, replacement string) {
		t.Helper()
		if count := strings.Count(source, old); count != 1 {
			t.Fatalf("runtime writer accounting insertion target count = %d, want 1 for %q", count, old)
		}
		source = strings.Replace(source, old, replacement, 1)
	}

	replaceOnce(
		"func NewHooks(packagePath string) Hooks { return Hooks{packagePath: packagePath} }",
		`var (
	testRequestedWriteCalls atomic.Uint64
	testRequestedWriteBytes atomic.Uint64
	testCompactions atomic.Uint64
	testSyncs atomic.Uint64
)

type WriterStatsForTesting struct {
	RequestedWriteCalls uint64 `+"`json:\"requestedWriteCalls\"`"+`
	RequestedWriteBytes uint64 `+"`json:\"requestedWriteBytes\"`"+`
	Compactions uint64 `+"`json:\"compactions\"`"+`
	Syncs uint64 `+"`json:\"syncs\"`"+`
}

func SnapshotWriterStatsForTesting() WriterStatsForTesting {
	return WriterStatsForTesting{
		RequestedWriteCalls: testRequestedWriteCalls.Load(),
		RequestedWriteBytes: testRequestedWriteBytes.Load(),
		Compactions: testCompactions.Load(),
		Syncs: testSyncs.Load(),
	}
}

func NewHooks(packagePath string) Hooks { return Hooks{packagePath: packagePath} }`,
	)
	replaceOnce(
		"func writeRecord(file *os.File, event record) (int64, error) {\n\tline, err := json.Marshal(event)\n\tif err != nil { return 0, fmt.Errorf(\"encode event: %w\", err) }\n\tline = append(line, '\\n')\n",
		"func writeRecord(file *os.File, event record) (int64, error) {\n\tline, err := json.Marshal(event)\n\tif err != nil { return 0, fmt.Errorf(\"encode event: %w\", err) }\n\tline = append(line, '\\n')\n\ttestRequestedWriteCalls.Add(1)\n\ttestRequestedWriteBytes.Add(uint64(len(line)))\n",
	)
	replaceOnce(
		"func compactLocked(state *writerState) {\n",
		"func compactLocked(state *writerState) {\n\ttestCompactions.Add(1)\n",
	)
	if strings.Contains(source, "\tif err := temporary.Sync(); err != nil {\n") {
		replaceOnce(
			"\tif err := temporary.Sync(); err != nil {\n",
			"\ttestSyncs.Add(1)\n\tif err := temporary.Sync(); err != nil {\n",
		)
	}
	return source
}

func runWriterProbe(t testing.TB, source string) writerProbeResult {
	t.Helper()
	moduleDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte("module example.test/writerprobe\n\ngo 1.26.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	injected, err := injectRuntimeSource(t.Context(), moduleDir, "example.test/writerprobe", source)
	if err != nil {
		t.Fatal(err)
	}
	mainSource := fmt.Sprintf(`package main

import (
	"encoding/json"
	"os"
	runtimecov %q
)

func main() {
	hooks := runtimecov.NewHooks("example.test/writerprobe/logic")
	for iteration := 0; iteration < 10000; iteration++ {
		var slot uint64
		hooks.BeginInto(&slot, 7, 1)
		hooks.Condition(slot, 0, true)
		hooks.EndSelect(slot, true, 8, 9)
	}
	for vector := 0; vector < 300; vector++ {
		var slot uint64
		hooks.BeginInto(&slot, 9, 9)
		for condition := 0; condition < 9; condition++ {
			hooks.Condition(slot, uint16(condition), vector&(1<<condition) != 0)
		}
		hooks.End(slot, vector&1 != 0)
	}
	var interrupted uint64
	hooks.BeginInto(&interrupted, 11, 1)
	hooks.Condition(interrupted, 0, true)
	if err := json.NewEncoder(os.Stdout).Encode(runtimecov.SnapshotWriterStatsForTesting()); err != nil {
		panic(err)
	}
}
`, injected.ImportPath)
	if err := os.WriteFile(filepath.Join(moduleDir, "main.go"), []byte(mainSource), 0o644); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	command := exec.Command("go", "run", ".")
	command.Dir = moduleDir
	command.Env = environmentWith(DataDirEnv, dataDir)
	command.Env = environmentReplace(command.Env, RunIDEnv, "writer-run")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("writer probe failed: %v\n%s", err, output)
	}
	var stats writerStats
	if err := json.Unmarshal(output, &stats); err != nil {
		t.Fatalf("decode writer stats %q: %v", output, err)
	}

	journal, err := CollectDetailed(t.Context(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	var finalBytes int64
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().IsRegular() {
			finalBytes += info.Size()
		}
	}
	return writerProbeResult{stats: stats, journal: journal, finalBytes: finalBytes}
}

func assertWriterProbeEvidence(t testing.TB, collected RecordedEvidence) {
	t.Helper()
	if len(collected.Diagnostics) != 0 {
		t.Fatalf("writer probe diagnostics = %#v", collected.Diagnostics)
	}
	semanticVectors := make(map[string]cover.DecisionID)
	aborted := 0
	for _, evaluation := range collected.Evaluations {
		key := fmt.Sprintf("%d:%v:%t:%d", evaluation.DecisionID, evaluation.Conditions, evaluation.Result, evaluation.Status)
		semanticVectors[key] = evaluation.DecisionID
		if evaluation.Status == cover.EvaluationAborted {
			aborted++
		}
	}
	byDecision := make(map[cover.DecisionID]int)
	for _, decisionID := range semanticVectors {
		byDecision[decisionID]++
	}
	if got, want := len(semanticVectors), 302; got != want || byDecision[7] != 1 || byDecision[9] != 300 || byDecision[11] != 1 || aborted != 1 {
		t.Fatalf("writer probe semantic vectors = %d/%#v, aborted=%d, want 302 with 1/300/1 and one abort", got, byDecision, aborted)
	}
	skipped := make(map[cover.DecisionID]struct{})
	for _, observation := range collected.NotEvaluatedDecisions {
		if observation.CauseDecisionID != 7 {
			t.Fatalf("writer probe skip cause = %#v, want decision 7", observation)
		}
		skipped[observation.DecisionID] = struct{}{}
	}
	if _, found := skipped[8]; !found {
		t.Fatalf("writer probe skip evidence lacks decision 8: %#v", collected.NotEvaluatedDecisions)
	}
	if _, found := skipped[9]; !found || len(skipped) != 2 {
		t.Fatalf("writer probe skip targets = %#v, want decisions 8 and 9", skipped)
	}
}
