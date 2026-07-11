// Package runtimecov injects and collects the small runtime used by
// instrumented target packages.
package runtimecov

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	cover "github.com/shrydev2020/gomcdc/internal/coverage"
)

const (
	// DataDirEnv tells the injected runtime where to create process event files.
	DataDirEnv = "GOCOVERAGE_DATA_DIR"
	// RunIDEnv scopes files from all package test processes in one CLI run.
	RunIDEnv = "GOCOVERAGE_RUN_ID"

	packageName          = "runtimecov"
	directoryPrefix      = "gocoverage_runtime_"
	eventFilePrefix      = "gocoverage-events-v1-"
	diagnosticFilePrefix = "gocoverage-diagnostics-v1-"
	eventFileSuffix      = ".jsonl"
	maxEventLine         = 16 << 20
)

// Package describes a runtime package injected into a copied target module.
type Package struct {
	Dir         string
	SourceFile  string
	RelativeDir string
	ImportPath  string
	PackageName string
}

// Diagnostic describes recoverable event-stream damage or a recorder failure.
type Diagnostic struct {
	File      string             `json:"file,omitempty"`
	Line      int                `json:"line,omitempty"`
	Message   string             `json:"message"`
	Severity  DiagnosticSeverity `json:"severity"`
	Truncated bool               `json:"truncated,omitempty"`
}

// DiagnosticSeverity separates an expected tail interruption after a failed
// process from corruption that invalidates the integrity of collected data.
type DiagnosticSeverity string

const (
	DiagnosticRecoverable DiagnosticSeverity = "recoverable-interruption"
	DiagnosticIntegrity   DiagnosticSeverity = "integrity-error"
)

// ProcessFile identifies one package/process event stream.
type ProcessFile struct {
	Path        string `json:"path"`
	RunID       string `json:"runId,omitempty"`
	PackagePath string `json:"packagePath,omitempty"`
	ProcessID   int    `json:"processId,omitempty"`
}

// Collection is the loss-preserving runtime projection. Exact duplicate
// evaluation vectors are aggregated across process streams; one representative
// EvaluationID is retained for each distinct piece of coverage evidence. Only
// completed evaluations may subsequently establish decision, condition, or
// MC/DC coverage; aborted records remain diagnostics/evidence of interruption.
type Collection struct {
	Evaluations           []cover.DecisionEvaluation              `json:"evaluations"`
	NotEvaluatedDecisions []cover.DecisionNotEvaluatedObservation `json:"notEvaluatedDecisions,omitempty"`
	Clauses               []cover.ClauseObservation               `json:"clauses"`
	Diagnostics           []Diagnostic                            `json:"diagnostics,omitempty"`
	Files                 []ProcessFile                           `json:"files,omitempty"`
}

// Inject creates a new target-local internal runtime package. Each call uses a
// fresh directory and never replaces an existing target package.
func Inject(moduleDir, modulePath string) (_ Package, err error) {
	moduleDir, err = canonicalModuleDirectory(moduleDir)
	if err != nil {
		return Package{}, err
	}
	if err := validateModulePath(modulePath); err != nil {
		return Package{}, err
	}

	internalDir := filepath.Join(moduleDir, "internal")
	if err := ensureRealDirectory(internalDir); err != nil {
		return Package{}, fmt.Errorf("prepare target internal directory: %w", err)
	}
	runtimeDir, err := os.MkdirTemp(internalDir, directoryPrefix)
	if err != nil {
		return Package{}, fmt.Errorf("create target runtime package: %w", err)
	}
	removeOnError := true
	defer func() {
		if removeOnError {
			err = errors.Join(err, os.RemoveAll(runtimeDir))
		}
	}()
	if err := os.Chmod(runtimeDir, 0o755); err != nil {
		return Package{}, fmt.Errorf("set target runtime directory mode: %w", err)
	}
	sourceFile := filepath.Join(runtimeDir, "runtime.go")
	if err := os.WriteFile(sourceFile, []byte(runtimeSource), 0o644); err != nil {
		return Package{}, fmt.Errorf("write target runtime package: %w", err)
	}
	relativeDir, err := filepath.Rel(moduleDir, runtimeDir)
	if err != nil {
		return Package{}, fmt.Errorf("resolve target runtime package path: %w", err)
	}
	relativeDir = filepath.ToSlash(relativeDir)
	removeOnError = false
	return Package{
		Dir:         runtimeDir,
		SourceFile:  sourceFile,
		RelativeDir: relativeDir,
		ImportPath:  strings.TrimSuffix(modulePath, "/") + "/" + relativeDir,
		PackageName: packageName,
	}, nil
}

// CollectDetailed recovers every complete JSONL record. A truncated tail or
// malformed line is diagnosed while earlier and later complete records remain
// available. Begin records without a terminal record become aborted
// evaluations and can never be used as coverage evidence.
func CollectDetailed(dataDir string) (Collection, error) {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return Collection{}, fmt.Errorf("read runtime event directory %q: %w", dataDir, err)
	}
	collector := eventCollector{
		begun:                make(map[cover.EvaluationIdentity]wireRecord),
		finished:             make(map[cover.EvaluationIdentity]struct{}),
		evaluationEvidence:   make(map[evaluationEvidenceKey]int),
		notEvaluatedEvidence: make(map[cover.DecisionNotEvaluatedObservation]struct{}),
		clauseEvidence:       make(map[cover.ClauseObservation]struct{}),
	}
	for _, entry := range entries {
		path := filepath.Join(dataDir, entry.Name())
		info, infoErr := entry.Info()
		if infoErr != nil {
			return Collection{}, fmt.Errorf("inspect runtime event file %q: %w", path, infoErr)
		}
		if !info.Mode().IsRegular() || entry.Type()&os.ModeSymlink != 0 {
			collector.diagnostic(path, 0, false, "event entry is not a regular file")
			continue
		}
		if !validEventFileName(entry.Name()) {
			collector.diagnostic(path, 0, false, "unexpected event directory entry")
			continue
		}
		if err := collector.collectFile(path); err != nil {
			return Collection{}, err
		}
	}
	collector.finishAborted()
	collector.sort()
	return collector.result, nil
}

type wireRecord struct {
	Type               string   `json:"type"`
	RunID              string   `json:"runId,omitempty"`
	PackagePath        string   `json:"packagePath,omitempty"`
	ProcessID          int      `json:"processId,omitempty"`
	EvaluationID       uint64   `json:"evaluationId,omitempty"`
	DecisionID         uint64   `json:"decisionId,omitempty"`
	ConditionCount     int      `json:"conditionCount,omitempty"`
	TestID             string   `json:"testId,omitempty"`
	Conditions         []uint8  `json:"conditions,omitempty"`
	Result             bool     `json:"result,omitempty"`
	Status             uint8    `json:"status,omitempty"`
	SwitchID           uint64   `json:"switchId,omitempty"`
	ClauseID           uint64   `json:"clauseId,omitempty"`
	Event              string   `json:"event,omitempty"`
	SkippedDecisionIDs []uint64 `json:"skippedDecisionIds,omitempty"`
	Message            string   `json:"message,omitempty"`
}

type eventCollector struct {
	result               Collection
	begun                map[cover.EvaluationIdentity]wireRecord
	finished             map[cover.EvaluationIdentity]struct{}
	evaluationEvidence   map[evaluationEvidenceKey]int
	notEvaluatedEvidence map[cover.DecisionNotEvaluatedObservation]struct{}
	clauseEvidence       map[cover.ClauseObservation]struct{}
}

type evaluationEvidenceKey struct {
	RunID       string
	PackagePath string
	DecisionID  cover.DecisionID
	Conditions  string
	Result      bool
	Status      cover.EvaluationStatus
}

func (collector *eventCollector) collectFile(path string) (err error) {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open runtime event file %q: %w", path, err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	collector.result.Files = append(collector.result.Files, ProcessFile{Path: path})
	fileIndex := len(collector.result.Files) - 1
	reader := bufio.NewReaderSize(file, maxEventLine)
	lineNumber := 0
	for {
		line, readErr := reader.ReadSlice('\n')
		switch {
		case readErr == nil:
			lineNumber++
			line = line[:len(line)-1]
			if len(line) == 0 {
				collector.diagnostic(path, lineNumber, false, "empty event record")
				continue
			}
			record, decodeErr := decodeRecord(line)
			if decodeErr != nil {
				collector.diagnostic(path, lineNumber, false, decodeErr.Error())
				continue
			}
			processFile := &collector.result.Files[fileIndex]
			if processFile.RunID == "" {
				processFile.RunID = record.RunID
				processFile.PackagePath = record.PackagePath
				processFile.ProcessID = record.ProcessID
			}
			collector.consume(path, lineNumber, record)
		case errors.Is(readErr, bufio.ErrBufferFull):
			collector.diagnostic(path, lineNumber+1, true, fmt.Sprintf("event record exceeds %d bytes", maxEventLine))
			for errors.Is(readErr, bufio.ErrBufferFull) {
				_, readErr = reader.ReadSlice('\n')
			}
			if readErr == nil {
				lineNumber++
				continue
			}
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return fmt.Errorf("read runtime event file %q: %w", path, readErr)
		case errors.Is(readErr, io.EOF) && len(line) != 0:
			collector.interruption(path, lineNumber+1, true, "truncated final event record")
			return nil
		case errors.Is(readErr, io.EOF):
			if lineNumber == 0 {
				collector.interruption(path, 0, false, "empty event file")
			}
			return nil
		default:
			return fmt.Errorf("read runtime event file %q: %w", path, readErr)
		}
	}
}

func decodeRecord(line []byte) (wireRecord, error) {
	var record wireRecord
	decoder := json.NewDecoder(strings.NewReader(string(line)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return wireRecord{}, fmt.Errorf("decode event JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return wireRecord{}, errors.New("event record contains trailing JSON")
		}
		return wireRecord{}, fmt.Errorf("decode trailing JSON: %w", err)
	}
	if record.Type == "" {
		return wireRecord{}, errors.New("event record requires type")
	}
	return record, nil
}

func (collector *eventCollector) consume(path string, line int, record wireRecord) {
	identity := cover.EvaluationIdentity{
		RunID:        record.RunID,
		PackagePath:  record.PackagePath,
		ProcessID:    record.ProcessID,
		EvaluationID: cover.EvaluationID(record.EvaluationID),
	}
	switch record.Type {
	case "begin":
		if record.EvaluationID == 0 || record.DecisionID == 0 || record.ConditionCount < 0 || record.ConditionCount > 65535 {
			collector.diagnostic(path, line, false, "invalid begin record")
			return
		}
		if _, exists := collector.finished[identity]; exists {
			collector.diagnostic(path, line, false, "begin record follows a terminal record")
			return
		}
		if _, exists := collector.begun[identity]; exists {
			collector.diagnostic(path, line, false, "duplicate begin record")
			return
		}
		collector.begun[identity] = record
	case "terminal":
		if record.EvaluationID == 0 || record.DecisionID == 0 || record.Status > uint8(cover.EvaluationAborted) {
			collector.diagnostic(path, line, false, "invalid terminal record")
			return
		}
		if _, exists := collector.finished[identity]; exists {
			collector.diagnostic(path, line, false, "duplicate terminal record")
			return
		}
		states, valid := collector.conditionStates(path, line, "terminal", record.Conditions)
		if !valid {
			return
		}
		begin, exists := collector.begun[identity]
		if !exists {
			collector.diagnostic(path, line, false, "terminal record has no matching begin")
			return
		}
		if begin.DecisionID != record.DecisionID || begin.ConditionCount != len(states) {
			collector.diagnostic(path, line, false, "terminal record does not match begin")
			return
		}
		delete(collector.begun, identity)
		canonical := collector.addEvaluation(evaluationFromRecord(record, states))
		collector.addNotEvaluatedDecisions(path, line, record, canonical)
		collector.finished[identity] = struct{}{}
	case "evaluation":
		if record.EvaluationID == 0 || record.DecisionID == 0 || record.Status > uint8(cover.EvaluationAborted) {
			collector.diagnostic(path, line, false, "invalid self-contained evaluation record")
			return
		}
		if _, exists := collector.finished[identity]; exists {
			collector.diagnostic(path, line, false, "duplicate self-contained evaluation record")
			return
		}
		if _, exists := collector.begun[identity]; exists {
			collector.diagnostic(path, line, false, "self-contained evaluation conflicts with begin record")
			return
		}
		states, valid := collector.conditionStates(path, line, "self-contained evaluation", record.Conditions)
		if !valid {
			return
		}
		canonical := collector.addEvaluation(evaluationFromRecord(record, states))
		collector.addNotEvaluatedDecisions(path, line, record, canonical)
		collector.finished[identity] = struct{}{}
	case "clause":
		event := cover.ClauseEventKind(record.Event)
		if (event == cover.ClauseNoMatchSelection && (record.SwitchID == 0 || record.ClauseID != 0)) ||
			(event != cover.ClauseDirectSelection && event != cover.ClauseBodyExecution && event != cover.ClauseNoMatchSelection) ||
			(event != cover.ClauseNoMatchSelection && record.ClauseID == 0) {
			collector.diagnostic(path, line, false, "invalid clause record")
			return
		}
		collector.addClause(cover.ClauseObservation{
			SwitchID: cover.SwitchID(record.SwitchID),
			ClauseID: cover.ClauseID(record.ClauseID),
			Event:    event,
		})
	case "diagnostic":
		message := record.Message
		if message == "" {
			message = "runtime recorder reported an unspecified failure"
		}
		collector.diagnostic(path, line, false, message)
	default:
		collector.diagnostic(path, line, false, fmt.Sprintf("unknown event type %q", record.Type))
	}
}

func (collector *eventCollector) finishAborted() {
	for identity, begin := range collector.begun {
		collector.addEvaluation(cover.DecisionEvaluation{
			DecisionID:   cover.DecisionID(begin.DecisionID),
			EvaluationID: identity.EvaluationID,
			RunID:        identity.RunID,
			PackagePath:  identity.PackagePath,
			ProcessID:    identity.ProcessID,
			TestID:       cover.UnknownTestID,
			Conditions:   make([]cover.ConditionState, begin.ConditionCount),
			Status:       cover.EvaluationAborted,
		})
	}
}

func (collector *eventCollector) conditionStates(path string, line int, kind string, encoded []uint8) ([]cover.ConditionState, bool) {
	states := make([]cover.ConditionState, len(encoded))
	for index, state := range encoded {
		if state > uint8(cover.ConditionTrue) {
			collector.diagnostic(path, line, false, kind+" record has invalid condition state")
			return nil, false
		}
		states[index] = cover.ConditionState(state)
	}
	return states, true
}

func evaluationFromRecord(record wireRecord, states []cover.ConditionState) cover.DecisionEvaluation {
	testID := record.TestID
	if testID == "" {
		testID = cover.UnknownTestID
	}
	return cover.DecisionEvaluation{
		DecisionID:   cover.DecisionID(record.DecisionID),
		EvaluationID: cover.EvaluationID(record.EvaluationID),
		RunID:        record.RunID,
		PackagePath:  record.PackagePath,
		ProcessID:    record.ProcessID,
		TestID:       testID,
		Conditions:   states,
		Result:       record.Result,
		Status:       cover.EvaluationStatus(record.Status),
	}
}

func (collector *eventCollector) addEvaluation(evaluation cover.DecisionEvaluation) cover.DecisionEvaluation {
	encodedConditions := make([]byte, len(evaluation.Conditions))
	for index, condition := range evaluation.Conditions {
		encodedConditions[index] = byte(condition)
	}
	key := evaluationEvidenceKey{
		RunID:       evaluation.RunID,
		PackagePath: evaluation.PackagePath,
		DecisionID:  evaluation.DecisionID,
		Conditions:  string(encodedConditions),
		Result:      evaluation.Result,
		Status:      evaluation.Status,
	}
	if index, exists := collector.evaluationEvidence[key]; exists {
		if collector.result.Evaluations[index].TestID == cover.UnknownTestID && evaluation.TestID != cover.UnknownTestID {
			collector.result.Evaluations[index].TestID = evaluation.TestID
		}
		return collector.result.Evaluations[index]
	}
	collector.evaluationEvidence[key] = len(collector.result.Evaluations)
	collector.result.Evaluations = append(collector.result.Evaluations, evaluation)
	return evaluation
}

func (collector *eventCollector) addNotEvaluatedDecisions(
	path string,
	line int,
	record wireRecord,
	cause cover.DecisionEvaluation,
) {
	if len(record.SkippedDecisionIDs) == 0 {
		return
	}
	if record.Status != uint8(cover.EvaluationCompleted) || !record.Result {
		collector.diagnostic(path, line, false, "non-selecting evaluation contains skipped decisions")
		return
	}
	for _, skippedID := range record.SkippedDecisionIDs {
		if skippedID == 0 || skippedID == record.DecisionID {
			collector.diagnostic(path, line, false, "invalid skipped decision ID")
			continue
		}
		observation := cover.DecisionNotEvaluatedObservation{
			DecisionID:        cover.DecisionID(skippedID),
			CauseDecisionID:   cause.DecisionID,
			CauseEvaluationID: cause.EvaluationID,
			RunID:             cause.RunID,
			PackagePath:       cause.PackagePath,
			ProcessID:         cause.ProcessID,
		}
		if _, exists := collector.notEvaluatedEvidence[observation]; exists {
			continue
		}
		collector.notEvaluatedEvidence[observation] = struct{}{}
		collector.result.NotEvaluatedDecisions = append(collector.result.NotEvaluatedDecisions, observation)
	}
}

func (collector *eventCollector) addClause(observation cover.ClauseObservation) {
	if _, exists := collector.clauseEvidence[observation]; exists {
		return
	}
	collector.clauseEvidence[observation] = struct{}{}
	collector.result.Clauses = append(collector.result.Clauses, observation)
}

func (collector *eventCollector) diagnostic(path string, line int, truncated bool, message string) {
	collector.result.Diagnostics = append(collector.result.Diagnostics, Diagnostic{
		File: path, Line: line, Message: message, Severity: DiagnosticIntegrity, Truncated: truncated,
	})
}

func (collector *eventCollector) interruption(path string, line int, truncated bool, message string) {
	collector.result.Diagnostics = append(collector.result.Diagnostics, Diagnostic{
		File: path, Line: line, Message: message, Severity: DiagnosticRecoverable, Truncated: truncated,
	})
}

func (collector *eventCollector) sort() {
	sort.Slice(collector.result.Evaluations, func(i, j int) bool {
		left := collector.result.Evaluations[i]
		right := collector.result.Evaluations[j]
		if left.RunID != right.RunID {
			return left.RunID < right.RunID
		}
		if left.PackagePath != right.PackagePath {
			return left.PackagePath < right.PackagePath
		}
		if left.ProcessID != right.ProcessID {
			return left.ProcessID < right.ProcessID
		}
		return left.EvaluationID < right.EvaluationID
	})
	sort.Slice(collector.result.Clauses, func(i, j int) bool {
		left := collector.result.Clauses[i]
		right := collector.result.Clauses[j]
		if left.ClauseID != right.ClauseID {
			return left.ClauseID < right.ClauseID
		}
		return left.Event < right.Event
	})
	sort.Slice(collector.result.NotEvaluatedDecisions, func(i, j int) bool {
		left := collector.result.NotEvaluatedDecisions[i]
		right := collector.result.NotEvaluatedDecisions[j]
		if left.DecisionID != right.DecisionID {
			return left.DecisionID < right.DecisionID
		}
		if left.RunID != right.RunID {
			return left.RunID < right.RunID
		}
		if left.PackagePath != right.PackagePath {
			return left.PackagePath < right.PackagePath
		}
		if left.ProcessID != right.ProcessID {
			return left.ProcessID < right.ProcessID
		}
		return left.CauseEvaluationID < right.CauseEvaluationID
	})
	sort.Slice(collector.result.Diagnostics, func(i, j int) bool {
		left := collector.result.Diagnostics[i]
		right := collector.result.Diagnostics[j]
		if left.File != right.File {
			return left.File < right.File
		}
		if left.Line != right.Line {
			return left.Line < right.Line
		}
		if left.Severity != right.Severity {
			return left.Severity < right.Severity
		}
		return left.Message < right.Message
	})
}

func canonicalModuleDirectory(path string) (string, error) {
	if path == "" {
		return "", errors.New("module directory is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve module directory %q: %w", path, err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve module directory %q: %w", path, err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("inspect module directory %q: %w", path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("module directory %q is not a directory", path)
	}
	return filepath.Clean(canonical), nil
}

func validateModulePath(path string) error {
	if path == "" {
		return errors.New("module path is required")
	}
	if strings.TrimSpace(path) != path || strings.HasSuffix(path, "/") || strings.Contains(path, "\\") {
		return fmt.Errorf("invalid module path %q", path)
	}
	return nil
}

func ensureRealDirectory(path string) error {
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%q is a symbolic link", path)
		}
		if !info.IsDir() {
			return fmt.Errorf("%q is not a directory", path)
		}
		return nil
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("inspect %q: %w", path, err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("create %q: %w", path, err)
		}
		return ensureRealDirectory(path)
	}
	return nil
}

func validEventFileName(name string) bool {
	return (strings.HasPrefix(name, eventFilePrefix) || strings.HasPrefix(name, diagnosticFilePrefix)) &&
		strings.HasSuffix(name, eventFileSuffix)
}

const runtimeSource = `// Code generated by gocoverage. DO NOT EDIT.

package runtimecov

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
)

const (
	dataDirEnv = "` + DataDirEnv + `"
	runIDEnv = "` + RunIDEnv + `"
	eventPrefix = "` + eventFilePrefix + `"
	diagnosticPrefix = "` + diagnosticFilePrefix + `"
	eventSuffix = "` + eventFileSuffix + `"
	conditionNotEvaluated = uint8(0)
	conditionFalse = uint8(1)
	conditionTrue = uint8(2)
	statusCompleted = uint8(0)
	statusAborted = uint8(1)
	compactAfterEvents = 256
)

type Hooks struct { packagePath string }

// Exported aliases let generated same-package bridges name storage types
// without relying on shadowable predeclared identifiers in user packages.
type EvaluationID = uint64

type evaluation struct {
	packagePath string
	decisionID uint64
	conditions []uint8
	testID string
}

type writerState struct {
	packagePath string
	path string
	file *os.File
	eventsSinceCompact int
	evaluations map[string]record
	clauses map[string]record
}

type record struct {
	Type string ` + "`json:\"type\"`" + `
	RunID string ` + "`json:\"runId,omitempty\"`" + `
	PackagePath string ` + "`json:\"packagePath,omitempty\"`" + `
	ProcessID int ` + "`json:\"processId,omitempty\"`" + `
	EvaluationID uint64 ` + "`json:\"evaluationId,omitempty\"`" + `
	DecisionID uint64 ` + "`json:\"decisionId,omitempty\"`" + `
	ConditionCount int ` + "`json:\"conditionCount,omitempty\"`" + `
	TestID string ` + "`json:\"testId,omitempty\"`" + `
	Conditions []uint8 ` + "`json:\"conditions,omitempty\"`" + `
	Result bool ` + "`json:\"result,omitempty\"`" + `
	Status uint8 ` + "`json:\"status,omitempty\"`" + `
	SwitchID uint64 ` + "`json:\"switchId,omitempty\"`" + `
	ClauseID uint64 ` + "`json:\"clauseId,omitempty\"`" + `
	Event string ` + "`json:\"event,omitempty\"`" + `
	SkippedDecisionIDs []uint64 ` + "`json:\"skippedDecisionIds,omitempty\"`" + `
	Message string ` + "`json:\"message,omitempty\"`" + `
}

var (
	dataDir = os.Getenv(dataDirEnv)
	runID = initializeRunID()
	processID = os.Getpid()
	nextEvaluation atomic.Uint64
	recordMu sync.Mutex
	active = make(map[uint64]*evaluation)
	writers = make(map[string]*writerState)
	seenClauses = make(map[string]struct{})
	diagnosticFile *os.File
	diagnosticSeen = make(map[string]struct{})
)

func NewHooks(packagePath string) Hooks { return Hooks{packagePath: packagePath} }

func BeginDecision(decisionID uint64, conditionCount uint16) (evaluationID uint64) {
	defer func() { _ = recover() }()
	NewHooks("unknown").BeginInto(&evaluationID, decisionID, conditionCount)
	return evaluationID
}

func EvalCondition(evaluationID uint64, index uint16, value bool) (result bool) {
	result = value
	defer func() { _ = recover() }()
	return NewHooks("unknown").Condition(evaluationID, index, value)
}

func EndDecision(evaluationID uint64, value bool) (result bool) {
	result = value
	defer func() { _ = recover() }()
	return NewHooks("unknown").End(evaluationID, value)
}

func AbortDecision(evaluationID uint64) {
	defer func() { _ = recover() }()
	abort(evaluationID)
}

func (hooks Hooks) BeginInto(slot *uint64, decisionID uint64, conditionCount uint16) (proceed bool) {
	proceed = 0 == 0
	defer func() { _ = recover() }()
	if slot == nil { return proceed }
	*slot = 0
	id := nextEvaluation.Add(1)
	if id == 0 { id = nextEvaluation.Add(1) }
	item := &evaluation{packagePath: hooks.packagePath, decisionID: decisionID, conditions: make([]uint8, int(conditionCount)), testID: "unknown"}
	recordMu.Lock()
	defer recordMu.Unlock()
	active[id] = item
	*slot = id
	writeLocked(hooks.packagePath, record{Type:"begin", EvaluationID:id, DecisionID:decisionID, ConditionCount:int(conditionCount), TestID:item.testID})
	return proceed
}

func (hooks Hooks) Condition(evaluationID uint64, index uint16, value bool) (result bool) {
	result = value
	defer func() { _ = recover() }()
	recordMu.Lock()
	defer recordMu.Unlock()
	item := active[evaluationID]
	if item == nil || int(index) >= len(item.conditions) {
		diagnosticLocked(hooks.packagePath, "condition referenced an unknown evaluation or index")
		return result
	}
	if value { item.conditions[index] = conditionTrue } else { item.conditions[index] = conditionFalse }
	return result
}

func (hooks Hooks) End(evaluationID uint64, value bool) (result bool) {
	result = value
	defer func() { _ = recover() }()
	finish(evaluationID, value, statusCompleted, nil)
	return result
}

func (hooks Hooks) EndSelect(evaluationID uint64, value bool, skippedDecisionIDs ...uint64) (result bool) {
	result = value
	defer func() { _ = recover() }()
	if !value { skippedDecisionIDs = nil }
	finish(evaluationID, value, statusCompleted, skippedDecisionIDs)
	return result
}

func (hooks Hooks) AbortSlots(slots []uint64) {
	defer func() { _ = recover() }()
	for _, id := range slots { abort(id) }
}

func (hooks Hooks) SelectClause(clauseID uint64, switchIDs ...uint64) {
	defer func() { _ = recover() }()
	var switchID uint64
	if len(switchIDs) > 0 {
		switchID = switchIDs[0]
	}
	recordClause(hooks.packagePath, switchID, clauseID, "body-execution")
}

func (hooks Hooks) NoMatch(switchID uint64) {
	defer func() { _ = recover() }()
	recordClause(hooks.packagePath, switchID, 0, "no-match-selection")
}

func finish(id uint64, result bool, status uint8, skippedDecisionIDs []uint64) {
	recordMu.Lock()
	defer recordMu.Unlock()
	item := active[id]
	if item == nil { diagnosticLocked("unknown", "terminal referenced an unknown evaluation"); return }
	delete(active, id)
	writeLocked(item.packagePath, record{Type:"terminal", EvaluationID:id, DecisionID:item.decisionID, TestID:item.testID, Conditions:append([]uint8(nil), item.conditions...), Result:result, Status:status, SkippedDecisionIDs:append([]uint64(nil), skippedDecisionIDs...)})
}

func abort(id uint64) {
	if id == 0 { return }
	recordMu.Lock()
	defer recordMu.Unlock()
	item := active[id]
	if item == nil { return }
	delete(active, id)
	writeLocked(item.packagePath, record{Type:"terminal", EvaluationID:id, DecisionID:item.decisionID, TestID:item.testID, Conditions:append([]uint8(nil), item.conditions...), Status:statusAborted})
}

func recordClause(packagePath string, switchID, clauseID uint64, event string) {
	recordMu.Lock()
	defer recordMu.Unlock()
	key := packagePath + "\x00" + strconv.FormatUint(switchID, 10) + "\x00" + strconv.FormatUint(clauseID, 10) + "\x00" + event
	if _, exists := seenClauses[key]; exists { return }
	seenClauses[key] = struct{}{}
	writeLocked(packagePath, record{Type:"clause", SwitchID: switchID, ClauseID:clauseID, Event:event})
}

func writeLocked(packagePath string, event record) {
	if dataDir == "" { return }
	state := writers[packagePath]
	if state == nil {
		encodedPackage := filenameComponent(packagePath)
		encodedRun := filenameComponent(runID)
		pattern := eventPrefix + encodedPackage + "-pid_" + strconv.Itoa(processID) + "-run_" + encodedRun + "-*" + eventSuffix
		created, err := os.CreateTemp(dataDir, pattern)
		if err != nil { diagnosticLocked(packagePath, "create event file: " + err.Error()); return }
		state = &writerState{
			packagePath: packagePath,
			path: created.Name(),
			file: created,
			evaluations: make(map[string]record),
			clauses: make(map[string]record),
		}
		writers[packagePath] = state
	} else if state.file == nil {
		reopened, err := os.OpenFile(state.path, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil { diagnosticLocked(packagePath, "reopen event file: " + err.Error()); return }
		state.file = reopened
	}
	event.RunID = runID
	event.PackagePath = packagePath
	event.ProcessID = processID
	switch event.Type {
	case "terminal":
		snapshot := event
		snapshot.Type = "evaluation"
		key := evaluationRecordKey(snapshot)
		if _, exists := state.evaluations[key]; !exists { state.evaluations[key] = snapshot }
	case "clause":
		key := strconv.FormatUint(event.SwitchID, 10) + "\x00" + strconv.FormatUint(event.ClauseID, 10) + "\x00" + event.Event
		if _, exists := state.clauses[key]; !exists { state.clauses[key] = event }
	}
	if err := writeRecord(state.file, event); err != nil {
		diagnosticLocked(packagePath, "write event file: " + err.Error())
		return
	}
	state.eventsSinceCompact++
	if state.eventsSinceCompact >= compactAfterEvents { compactLocked(state) }
}

func evaluationRecordKey(event record) string {
	key := strconv.FormatUint(event.DecisionID, 10) + "\x00" +
		strconv.Itoa(len(event.Conditions)) + "\x00" + string(event.Conditions) + "\x00" +
		strconv.FormatBool(event.Result) + "\x00" + strconv.Itoa(int(event.Status)) + "\x00" +
		strconv.FormatUint(event.ClauseID, 10)
	for _, decisionID := range event.SkippedDecisionIDs {
		key += "\x00" + strconv.FormatUint(decisionID, 10)
	}
	return key
}

func writeRecord(file *os.File, event record) error {
	line, err := json.Marshal(event)
	if err != nil { return fmt.Errorf("encode event: %w", err) }
	line = append(line, '\n')
	written, err := file.Write(line)
	if err != nil { return err }
	if written != len(line) { return fmt.Errorf("short write: wrote %d of %d bytes", written, len(line)) }
	return nil
}

func compactLocked(state *writerState) {
	state.eventsSinceCompact = 0
	temporary, err := os.CreateTemp(dataDir, "." + filepath.Base(state.path) + ".compact-*")
	if err != nil { diagnosticLocked(state.packagePath, "create compacted event file: " + err.Error()); return }
	temporaryPath := temporary.Name()
	keepTemporary := true
	defer func() {
		if keepTemporary { _ = os.Remove(temporaryPath) }
	}()

	evaluationKeys := make([]string, 0, len(state.evaluations))
	for key := range state.evaluations { evaluationKeys = append(evaluationKeys, key) }
	sort.Strings(evaluationKeys)
	for _, key := range evaluationKeys {
		if err := writeRecord(temporary, state.evaluations[key]); err != nil {
			_ = temporary.Close()
			diagnosticLocked(state.packagePath, "write compacted evaluation: " + err.Error())
			return
		}
	}

	activeIDs := make([]uint64, 0, len(active))
	for id, item := range active {
		if item.packagePath == state.packagePath { activeIDs = append(activeIDs, id) }
	}
	sort.Slice(activeIDs, func(i, j int) bool { return activeIDs[i] < activeIDs[j] })
	for _, id := range activeIDs {
		item := active[id]
		event := record{
			Type: "begin", RunID: runID, PackagePath: state.packagePath, ProcessID: processID,
			EvaluationID: id, DecisionID: item.decisionID, ConditionCount: len(item.conditions), TestID: item.testID,
		}
		if err := writeRecord(temporary, event); err != nil {
			_ = temporary.Close()
			diagnosticLocked(state.packagePath, "write compacted active evaluation: " + err.Error())
			return
		}
	}

	clauseKeys := make([]string, 0, len(state.clauses))
	for key := range state.clauses { clauseKeys = append(clauseKeys, key) }
	sort.Strings(clauseKeys)
	for _, key := range clauseKeys {
		if err := writeRecord(temporary, state.clauses[key]); err != nil {
			_ = temporary.Close()
			diagnosticLocked(state.packagePath, "write compacted clause: " + err.Error())
			return
		}
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		diagnosticLocked(state.packagePath, "sync compacted event file: " + err.Error())
		return
	}
	if err := temporary.Close(); err != nil {
		diagnosticLocked(state.packagePath, "close compacted event file: " + err.Error())
		return
	}
	if err := os.Rename(temporaryPath, state.path); err != nil {
		diagnosticLocked(state.packagePath, "replace event file with compacted snapshot: " + err.Error())
		return
	}
	keepTemporary = false
	reopened, err := os.OpenFile(state.path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		_ = state.file.Close()
		state.file = nil
		diagnosticLocked(state.packagePath, "reopen compacted event file: " + err.Error())
		return
	}
	old := state.file
	state.file = reopened
	if err := old.Close(); err != nil { diagnosticLocked(state.packagePath, "close replaced event file: " + err.Error()) }
}

func diagnosticLocked(packagePath, message string) {
	key := packagePath + "\x00" + message
	if _, exists := diagnosticSeen[key]; exists { return }
	diagnosticSeen[key] = struct{}{}
	if dataDir != "" && diagnosticFile == nil {
		encodedRun := filenameComponent(runID)
		pattern := diagnosticPrefix + "pid_" + strconv.Itoa(processID) + "-run_" + encodedRun + "-*" + eventSuffix
		file, err := os.CreateTemp(dataDir, pattern)
		if err == nil { diagnosticFile = file }
	}
	event := record{Type:"diagnostic", RunID:runID, PackagePath:packagePath, ProcessID:processID, Message:message}
	if diagnosticFile != nil {
		if line, err := json.Marshal(event); err == nil {
			line = append(line, '\n')
			if written, err := diagnosticFile.Write(line); err == nil && written == len(line) { return }
		}
	}
	fmt.Fprintln(os.Stderr, "gocoverage runtime diagnostic:", message)
}

func initializeRunID() string {
	if value := os.Getenv(runIDEnv); value != "" { return value }
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err == nil { return hex.EncodeToString(bytes[:]) }
	return "standalone-" + strconv.Itoa(os.Getpid())
}

func filenameComponent(value string) string {
	encoded := base64.RawURLEncoding.EncodeToString([]byte(value))
	if len(encoded) <= 80 { return encoded }
	digest := sha256.Sum256([]byte(value))
	return encoded[:40] + "_" + hex.EncodeToString(digest[:12])
}
`
