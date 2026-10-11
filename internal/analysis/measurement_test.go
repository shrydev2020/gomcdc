package analysis

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/analyzer"
	"github.com/shrydev2020/gomcdc/v2/internal/c0"
	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
	"github.com/shrydev2020/gomcdc/v2/internal/evidence"
	"github.com/shrydev2020/gomcdc/v2/internal/mcdc"
	"github.com/shrydev2020/gomcdc/v2/internal/measurement"
	"github.com/shrydev2020/gomcdc/v2/internal/runtimecov"
)

func TestNativeMergeUsesOriginalStatementUnits(t *testing.T) {
	const module = "example.test/partition"
	source := []byte("package partition\nfunc F(first, second func()) {first(); second()}\n")
	inventory, err := c0.BuildInventory("p.go", source)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Blocks) != 1 || inventory.Blocks[0].Statements != 2 {
		t.Fatalf("unexpected fixture inventory: %+v", inventory)
	}
	settings := measurement.Settings{WorkingDirectory: ".", Packages: []string{module}, Coverage: []string{"statement", "function"}, GoEnvironment: map[string]string{"GOVERSION": "go1.26.0", "GOOS": "linux", "GOARCH": "amd64"}, SourceDigests: map[string]string{"p.go": measurement.Digest(source)}}
	normalized, err := measurement.BuildSettings(settings)
	if err != nil {
		t.Fatal(err)
	}
	plan := measurement.Plan{Definition: measurement.Definition, ToolVersion: "test", Module: module, Settings: normalized, Sources: []measurement.Source{{PackagePath: module, Path: "p.go", Content: source, SHA256: measurement.Digest(source)}}}
	run := func(id string, secondCount uint64, failed bool) measurement.Run {
		s := settings
		s.InvocationID = strings.Repeat(id, 32)
		r := measurement.Run{
			ID: s.InvocationID, Settings: s, Status: cover.RunPassed,
			FailureKind: cover.RunFailureNone, Complete: true, Mode: "standard-cover",
			Results: measurement.RunResults{
				Test: measurement.ResultPassed, Measurement: measurement.ResultPassed,
				Integrity: measurement.ResultPassed, Strict: measurement.ResultNotRequested,
				Threshold: measurement.ResultNotRequested,
			},
			Producers: []evidence.ProducerOutcome{{
				Producer: evidence.ProducerGoCover, Integrity: evidence.ProducerIntegrityValid,
				Mapping: evidence.ProducerMappingComplete, Completeness: evidence.ProducerCompletenessComplete,
				Usability: evidence.ProducerUsabilityAccepted,
			}},
			Cover: &c0.AcceptedCoverEvidence{
				Mode: c0.ModeCount,
				Sources: []c0.AcceptedSourceEvidence{{
					PackagePath: module, OriginalPath: "p.go", OriginalSource: source, Inventory: inventory,
					Blocks: []c0.AcceptedBlockEvidence{{
						BlockIndex: 0, Count: 1, Observed: true,
						Statements: []c0.AcceptedStatementEvidence{
							{Count: 1, Observed: true}, {Count: secondCount, Observed: true},
						},
					}},
				}},
			},
		}
		if failed {
			r.Status = cover.RunFailed
			r.FailureKind = cover.RunFailureTest
			r.Complete = false
			r.Results.Test = measurement.ResultFailed
			r.Producers[0].Completeness = evidence.ProducerCompletenessPartial
			r.Producers[0].Usability = evidence.ProducerUsabilityAcceptedPartial
		}
		return r
	}
	first, second := run("a", 0, true), run("b", 1, false)
	var baseline Result
	for _, runs := range [][]measurement.Run{{first, second}, {second, first}} {
		result, err := FromMeasurement(t.Context(), measurement.Archive{Plan: plan, Runs: runs}, mcdc.AnalysisBudget{})
		if err != nil {
			t.Fatal(err)
		}
		if result.Summary.Statement.Total != 2 || result.Summary.Statement.Covered != 2 || result.Run.Results.Test != ResultFailed {
			t.Fatalf("wrong union or provenance: %+v", result)
		}
		statements := result.Packages[0].Files[0].Functions[0].Statements
		if len(statements) != 2 || statements[0].Count != 2 || statements[1].Count != 1 {
			t.Fatalf("counters crossed statement boundaries: %+v", statements)
		}
		if baseline.Module != "" && !reflect.DeepEqual(result.Summary, baseline.Summary) {
			t.Fatal("input order changed coverage")
		}
		baseline = result
	}
	// A block with no observations still owns both original obligations.
	missing := run("c", 0, true)
	missing.Cover.Sources[0].Blocks = nil
	result, err := FromMeasurement(t.Context(), measurement.Archive{Plan: plan, Runs: []measurement.Run{first, missing}}, mcdc.AnalysisBudget{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.Statement.Total != 2 || result.Summary.Statement.Covered != 1 {
		t.Fatalf("missing observations changed denominator: %+v", result.Summary.Statement)
	}
	overflow := run("d", math.MaxUint64, false)
	if _, err := FromMeasurement(t.Context(), measurement.Archive{Plan: plan, Runs: []measurement.Run{second, overflow}}, mcdc.AnalysisBudget{}); err == nil || !strings.Contains(err.Error(), "counter overflow") {
		t.Fatalf("overflow accepted: %v", err)
	}

}

func decisionArchive(t *testing.T) measurement.Archive {
	t.Helper()
	root := t.TempDir()
	source := []byte("package p\nfunc F(a bool) bool { if a {return true}; return false }\n")
	path := filepath.Join(root, "p.go")
	if err := os.WriteFile(path, source, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := analyzer.AnalyzeFile(analyzer.FileOptions{Path: path, ModuleDir: root, ModulePath: "example.test/p", PackagePath: "example.test/p"})
	if err != nil {
		t.Fatal(err)
	}
	d := file.Decisions[0].Metadata
	settings := measurement.Settings{WorkingDirectory: ".", Packages: []string{d.Package}, Coverage: []string{"decision", "condition", "mcdc-unique", "mcdc-masking"}, GoEnvironment: map[string]string{"GOVERSION": "go1.26.0", "GOOS": "linux", "GOARCH": "amd64"}, SourceDigests: map[string]string{"p.go": measurement.Digest(source)}}
	normalized, err := measurement.BuildSettings(settings)
	if err != nil {
		t.Fatal(err)
	}
	archive := measurement.Archive{Plan: measurement.Plan{Definition: measurement.Definition, ToolVersion: "test", Module: d.Package, Settings: normalized, Sources: []measurement.Source{{PackagePath: d.Package, Path: "p.go", Content: source, SHA256: measurement.Digest(source)}}, Decisions: []cover.DecisionMetadata{d}}}
	for i, id := range []string{"a", "b", "c"} {
		settings := settings
		settings.InvocationID = strings.Repeat(id, 32)
		value := i == 0
		state := cover.ConditionFalse
		if value {
			state = cover.ConditionTrue
		}
		archive.Runs = append(archive.Runs, measurement.Run{ID: settings.InvocationID, RuntimeID: settings.InvocationID, Settings: settings, Status: cover.RunPassed, FailureKind: cover.RunFailureNone, Complete: true, Mode: "single-run", Results: measurement.RunResults{Test: measurement.ResultPassed, Measurement: measurement.ResultPassed, Integrity: measurement.ResultPassed, Strict: measurement.ResultNotRequested, Threshold: measurement.ResultNotRequested}, Packages: map[string]string{d.Package: "passed"}, Probes: measurement.Probes{Decisions: []cover.DecisionID{d.ID}}, Producers: []evidence.ProducerOutcome{{Producer: evidence.ProducerASTRuntime, Integrity: evidence.ProducerIntegrityValid, Completeness: evidence.ProducerCompletenessComplete, Mapping: evidence.ProducerMappingComplete, Usability: evidence.ProducerUsabilityAccepted}}, Runtime: runtimecov.RecordedEvidence{Evaluations: []cover.DecisionEvaluation{{DecisionID: d.ID, EvaluationID: 1, RunID: settings.InvocationID, PackagePath: d.Package, ProcessID: 1, TestID: "TestF", Conditions: []cover.ConditionState{state}, Result: value, Status: cover.EvaluationCompleted}}}})
	}
	return archive
}

func TestMergeUsesAcceptedEvidenceAndRetainsFailures(t *testing.T) {
	for _, partialValue := range []bool{false, true} {
		archive := decisionArchive(t)
		partial := &archive.Runs[1]
		partial.Complete = false
		partial.Status = cover.RunFailed
		partial.FailureKind = cover.RunFailureInterrupted
		partial.Results.Test = measurement.ResultFailed
		partial.Producers[0].Completeness = evidence.ProducerCompletenessPartial
		partial.Producers[0].Usability = evidence.ProducerUsabilityAcceptedPartial
		partial.Runtime.Evaluations[0].Result = partialValue
		if partialValue {
			partial.Runtime.Evaluations[0].Conditions[0] = cover.ConditionTrue
		}
		rejected := &archive.Runs[2]
		rejected.Complete = false
		rejected.Status = cover.RunFailed
		rejected.FailureKind = cover.RunFailureCommand
		rejected.Results.Integrity = measurement.ResultFailed
		rejected.Producers[0].Integrity = evidence.ProducerIntegrityInvalid
		rejected.Producers[0].Usability = evidence.ProducerUsabilityRejected
		before, err := json.Marshal(archive)
		if err != nil {
			t.Fatal(err)
		}
		for _, order := range [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
			reordered := archive
			reordered.Runs = []measurement.Run{archive.Runs[order[0]], archive.Runs[order[1]], archive.Runs[order[2]]}
			result, err := FromMeasurement(t.Context(), reordered, mcdc.AnalysisBudget{})
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if partialValue {
				want = 0
			}
			if result.Summary.MCDCMasking.Covered != want || result.Summary.MCDCUnique.Covered != want || result.Run.Complete || result.Run.Results.Test != ResultFailed || result.Run.Results.Integrity != ResultFailed {
				t.Fatalf("wrong union for %v: %+v", order, result)
			}
			decision := result.Packages[0].Files[0].Functions[0].Decisions[0]
			for _, e := range decision.Evaluations {
				if e.RunID == rejected.ID {
					t.Fatal("rejected evaluation used")
				}
			}
			if len(result.ContributingRuns) != 3 {
				t.Fatal("execution provenance lost")
			}
		}
		after, _ := json.Marshal(archive)
		if string(before) != string(after) {
			t.Fatal("analysis mutated evidence")
		}
	}
}

func TestMissingProbesRemainUnknownAfterMerging(t *testing.T) {
	archive := decisionArchive(t)
	for i := range archive.Runs {
		archive.Runs[i].Probes = measurement.Probes{}
	}
	result, err := FromMeasurement(t.Context(), archive, mcdc.AnalysisBudget{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.Decision.Unknown != 2 || result.Summary.MCDCMasking.Covered != 0 || result.Instrumentation.Total.Unknown == 0 {
		t.Fatalf("missing probes became measured: %+v", result.Summary)
	}
}
