package runtimecov

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
)

func TestInjectedTestContextPreservesProfilesInheritanceAndCompaction(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/context\n\ngo 1.26.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	injected, err := Inject(t.Context(), root, "example.test/context")
	if err != nil {
		t.Fatal(err)
	}
	// A small checkpoint threshold makes preservation through compaction part
	// of this provenance check rather than depending on append-tail size.
	source := strings.Replace(runtimeSource, "minimumCompactTailBytes = int64(1 << 20)", "minimumCompactTailBytes = int64(1 << 14)", 1)
	if err := os.WriteFile(injected.SourceFile, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	const testSource = `package runtimecov

import (
	"context"
	"runtime/pprof"
	"sync"
	"testing"
)

func TestProfileContext(t *testing.T) {
	user := pprof.WithLabels(context.Background(), pprof.Labels("user", "retained"))
	pprof.SetGoroutineLabels(user)
	defer pprof.SetGoroutineLabels(context.Background())
	hooks := NewHooks("example.test/context/logic")
	checkLabel := func() {
		if value, _ := pprof.Label(currentProfileContext(), "user"); value != "retained" { t.Fatalf("user profile label = %q", value) }
	}
	record := func() {
		var slot uint64
		hooks.BeginInto(&slot, 7, 1)
		hooks.Condition(slot, 0, true)
		hooks.End(slot, true)
	}
	restore := hooks.EnterTest(t)
	checkLabel()
	cleanup := hooks.Cleanup(record)
	var workers sync.WaitGroup
	workers.Go(func() { for range 200 { record() } })
	workers.Wait()
	restore()
	checkLabel()
	if currentTestID() != "unknown" { t.Fatal("test context leaked after restoration") }
	cleanup()
	if currentTestID() != "unknown" { t.Fatal("cleanup context leaked") }
	restore = hooks.EnterNamedTest("OtherTest")
	for range 200 { record() }
	restore()
	var missing *testing.T
	hooks.EnterTest(missing)()
	if currentTestID() != "unknown" { t.Fatal("nil test handle changed ownership") }
	if hooks.Test(nil) != nil || hooks.Cleanup(nil) != nil { t.Fatal("nil callback changed") }
}

`
	if err := os.WriteFile(filepath.Join(injected.Dir, "context_test.go"), []byte(testSource), 0o644); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	command := exec.CommandContext(t.Context(), "go", "test", "-race", "-count=1", "./"+filepath.ToSlash(injected.RelativeDir))
	command.Dir = root
	command.Env = environmentWith(DataDirEnv, dataDir)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("test context probe: %v\n%s", err, output)
	}
	evidence, err := CollectDetailed(t.Context(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence.Diagnostics) != 0 {
		t.Fatalf("diagnostics: %+v", evidence.Diagnostics)
	}
	names := make(map[string]bool)
	for _, evaluation := range evidence.Evaluations {
		names[evaluation.TestID] = true
	}
	if len(names) != 2 || !names["TestProfileContext"] || !names["OtherTest"] {
		t.Fatalf("test identities after compaction = %v", names)
	}
}

func TestInterruptedEvaluationRetainsTestID(t *testing.T) {
	begin := journalBegin(1, 1)
	begin.TestID = "TestInterrupted/child"
	evidence := collectJournalModel(t, []journalFileModel{{suffix: "interrupted.jsonl", records: []wireRecord{begin}}})
	if len(evidence.Evaluations) != 1 || evidence.Evaluations[0].Status != cover.EvaluationAborted || evidence.Evaluations[0].TestID != begin.TestID {
		t.Fatalf("interrupted evaluation lost test identity: %+v", evidence.Evaluations)
	}
}
