package runtimecov

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	cover "github.com/shrydev2020/gomcdc/internal/coverage"
)

type journalFileModel struct {
	suffix  string
	records []wireRecord
	tail    string
}

type journalSemantics struct {
	evaluations []string
	clauses     []string
}

// TestCollectDetailedJournalModelInvariants treats the journal as a semantic
// event model rather than asserting parser wording. Mutations encode the
// invariants the collector must preserve: independent record order is
// irrelevant, duplicate evidence cannot increase coverage, a torn tail cannot
// erase earlier evidence, an unterminated evaluation is aborted, and process
// provenance separates colliding local evaluation counters.
func TestCollectDetailedJournalModelInvariants(t *testing.T) {
	baselineFiles := journalBaselineFiles()
	baseline := collectJournalModel(t, baselineFiles)
	if len(baseline.Diagnostics) != 0 {
		t.Fatalf("baseline diagnostics = %#v", baseline.Diagnostics)
	}
	baselineSemantics := projectJournalSemantics(baseline)
	assertJournalCounts(t, baseline, 2, 0, 3)
	if baseline.Evaluations[0].EvaluationID != baseline.Evaluations[1].EvaluationID ||
		baseline.Evaluations[0].ProcessID == baseline.Evaluations[1].ProcessID {
		t.Fatalf("baseline does not exercise process-local ID collision: %#v", baseline.Evaluations)
	}

	tests := []struct {
		name            string
		files           []journalFileModel
		wantCompleted   int
		wantAborted     int
		wantClauses     int
		wantIntegrity   int
		wantRecoverable int
		wantTruncated   bool
		sameAsBaseline  bool
	}{
		{
			name:           "independent files and records reordered",
			files:          journalReorderedFiles(),
			wantCompleted:  2,
			wantClauses:    3,
			sameAsBaseline: true,
		},
		{
			name:           "duplicate terminal is diagnosed but not counted",
			files:          journalDuplicateTerminalFiles(),
			wantCompleted:  2,
			wantClauses:    3,
			wantIntegrity:  1,
			sameAsBaseline: true,
		},
		{
			name:           "duplicate clause evidence is idempotent",
			files:          journalDuplicateClauseFiles(),
			wantCompleted:  2,
			wantClauses:    3,
			sameAsBaseline: true,
		},
		{
			name:            "truncated tail preserves committed evidence",
			files:           journalTruncatedTailFiles(),
			wantCompleted:   2,
			wantClauses:     3,
			wantRecoverable: 1,
			wantTruncated:   true,
			sameAsBaseline:  true,
		},
		{
			name:          "missing terminal becomes aborted evidence",
			files:         journalMissingTerminalFiles(),
			wantCompleted: 1,
			wantAborted:   1,
			wantClauses:   3,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			collected := collectJournalModel(t, test.files)
			assertJournalCounts(t, collected, test.wantCompleted, test.wantAborted, test.wantClauses)
			integrity, recoverable, truncated := journalDiagnosticModel(collected.Diagnostics)
			if integrity != test.wantIntegrity || recoverable != test.wantRecoverable || truncated != test.wantTruncated {
				t.Errorf(
					"diagnostic model = integrity:%d recoverable:%d truncated:%t, want %d/%d/%t; diagnostics=%#v",
					integrity,
					recoverable,
					truncated,
					test.wantIntegrity,
					test.wantRecoverable,
					test.wantTruncated,
					collected.Diagnostics,
				)
			}
			if test.sameAsBaseline {
				got := projectJournalSemantics(collected)
				if !reflect.DeepEqual(got, baselineSemantics) {
					t.Errorf("semantic evidence changed\nbaseline: %#v\nmutated:  %#v", baselineSemantics, got)
				}
			}
		})
	}
}

func journalBaselineFiles() []journalFileModel {
	return []journalFileModel{
		{
			suffix: "process-a.jsonl",
			records: []wireRecord{
				journalBegin(1, 1),
				journalTerminal(1, 1),
				journalClause(1, cover.ClauseBodyExecution),
			},
		},
		{
			suffix: "process-b.jsonl",
			records: []wireRecord{
				journalEvaluation(2, 1),
				journalClause(2, cover.ClauseDirectSelection),
				{Type: "clause", RunID: "model-run", PackagePath: "example.test/model", ProcessID: 2, SwitchID: 77, Event: string(cover.ClauseNoMatchSelection)},
			},
		},
	}
}

func journalReorderedFiles() []journalFileModel {
	return []journalFileModel{
		{
			suffix: "a.jsonl",
			records: []wireRecord{
				{Type: "clause", RunID: "model-run", PackagePath: "example.test/model", ProcessID: 2, SwitchID: 77, Event: string(cover.ClauseNoMatchSelection)},
				journalClause(2, cover.ClauseDirectSelection),
				journalEvaluation(2, 1),
			},
		},
		{
			suffix: "z.jsonl",
			records: []wireRecord{
				journalClause(1, cover.ClauseBodyExecution),
				journalBegin(1, 1),
				journalTerminal(1, 1),
			},
		},
	}
}

func journalDuplicateTerminalFiles() []journalFileModel {
	files := cloneJournalFiles(journalBaselineFiles())
	files[0].records = append(files[0].records, journalTerminal(1, 1))
	return files
}

func journalDuplicateClauseFiles() []journalFileModel {
	files := cloneJournalFiles(journalBaselineFiles())
	files[0].records = append(files[0].records, journalClause(1, cover.ClauseBodyExecution))
	return files
}

func journalTruncatedTailFiles() []journalFileModel {
	files := cloneJournalFiles(journalBaselineFiles())
	files[1].tail = `{"type":"terminal"`
	return files
}

func journalMissingTerminalFiles() []journalFileModel {
	files := cloneJournalFiles(journalBaselineFiles())
	files[0].records = []wireRecord{journalBegin(1, 1), journalClause(1, cover.ClauseBodyExecution)}
	return files
}

func cloneJournalFiles(files []journalFileModel) []journalFileModel {
	clone := make([]journalFileModel, len(files))
	for index, file := range files {
		clone[index] = file
		clone[index].records = append([]wireRecord(nil), file.records...)
	}
	return clone
}

func journalBegin(processID int, evaluationID uint64) wireRecord {
	return wireRecord{
		Type: "begin", RunID: "model-run", PackagePath: "example.test/model", ProcessID: processID,
		EvaluationID: evaluationID, DecisionID: 7, ConditionCount: 2, TestID: "model-test",
	}
}

func journalTerminal(processID int, evaluationID uint64) wireRecord {
	return wireRecord{
		Type: "terminal", RunID: "model-run", PackagePath: "example.test/model", ProcessID: processID,
		EvaluationID: evaluationID, DecisionID: 7, TestID: "model-test",
		Conditions: []uint8{uint8(cover.ConditionTrue), uint8(cover.ConditionNotEvaluated)}, Result: true,
		Status: uint8(cover.EvaluationCompleted),
	}
}

func journalEvaluation(processID int, evaluationID uint64) wireRecord {
	return wireRecord{
		Type: "evaluation", RunID: "model-run", PackagePath: "example.test/model", ProcessID: processID,
		EvaluationID: evaluationID, DecisionID: 7, TestID: "model-test",
		Conditions: []uint8{uint8(cover.ConditionFalse), uint8(cover.ConditionTrue)}, Result: false,
		Status: uint8(cover.EvaluationCompleted),
	}
}

func journalClause(processID int, event cover.ClauseEventKind) wireRecord {
	record := wireRecord{
		Type: "clause", RunID: "model-run", PackagePath: "example.test/model", ProcessID: processID,
		SwitchID: 77, ClauseID: 55, Event: string(event),
	}
	if event == cover.ClauseDirectSelection {
		alternative := uint16(0)
		record.AlternativeIndex = &alternative
	}
	return record
}

func collectJournalModel(t *testing.T, files []journalFileModel) RecordedEvidence {
	t.Helper()
	dataDir := t.TempDir()
	for _, file := range files {
		var lines []string
		for _, record := range file.records {
			encoded, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			lines = append(lines, string(encoded))
		}
		content := strings.Join(lines, "\n") + "\n" + file.tail
		writeEventFile(t, dataDir, file.suffix, content)
	}
	collected, err := CollectDetailed(t.Context(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	return collected
}

func projectJournalSemantics(collection RecordedEvidence) journalSemantics {
	projection := journalSemantics{}
	for _, evaluation := range collection.Evaluations {
		projection.evaluations = append(projection.evaluations, fmt.Sprintf(
			"%s|%s|%d|%d|%d|%v|%t|%d",
			evaluation.RunID,
			evaluation.PackagePath,
			evaluation.ProcessID,
			evaluation.EvaluationID,
			evaluation.DecisionID,
			evaluation.Conditions,
			evaluation.Result,
			evaluation.Status,
		))
	}
	for _, clause := range collection.ClauseEvents {
		projection.clauses = append(projection.clauses, fmt.Sprintf(
			"%d|%d|%s|%d|%t",
			clause.SwitchID,
			clause.ClauseID,
			clause.Event,
			clause.AlternativeIndex,
			clause.AlternativeKnown,
		))
	}
	sort.Strings(projection.evaluations)
	sort.Strings(projection.clauses)
	return projection
}

func assertJournalCounts(t *testing.T, collection RecordedEvidence, wantCompleted, wantAborted, wantClauses int) {
	t.Helper()
	completed, aborted := 0, 0
	for _, evaluation := range collection.Evaluations {
		switch evaluation.Status {
		case cover.EvaluationCompleted:
			completed++
		case cover.EvaluationAborted:
			aborted++
		default:
			t.Errorf("unknown evaluation status %d", evaluation.Status)
		}
	}
	if completed != wantCompleted || aborted != wantAborted || len(collection.ClauseEvents) != wantClauses {
		t.Errorf(
			"semantic counts = completed:%d aborted:%d clauses:%d, want %d/%d/%d; collection=%#v",
			completed,
			aborted,
			len(collection.ClauseEvents),
			wantCompleted,
			wantAborted,
			wantClauses,
			collection,
		)
	}
}

func journalDiagnosticModel(diagnostics []Diagnostic) (integrity, recoverable int, truncated bool) {
	for _, diagnostic := range diagnostics {
		switch diagnostic.Severity {
		case DiagnosticIntegrity:
			integrity++
		case DiagnosticRecoverable:
			recoverable++
		}
		truncated = truncated || diagnostic.Truncated
	}
	return integrity, recoverable, truncated
}
