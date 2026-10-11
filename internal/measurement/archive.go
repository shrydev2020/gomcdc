// Package measurement owns source plans and execution evidence independently
// of coverage analysis and report formats.
package measurement

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/shrydev2020/gomcdc/v2/internal/analyzer"
	"github.com/shrydev2020/gomcdc/v2/internal/c0"
	"github.com/shrydev2020/gomcdc/v2/internal/config"
	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
	"github.com/shrydev2020/gomcdc/v2/internal/evidence"
	"github.com/shrydev2020/gomcdc/v2/internal/runtimecov"
)

const Format = "gomcdc-measurement"
const Version = "1.0"

// Definition identifies the existing occurrence-based, short-circuit semantics.
const Definition = "go-control-decisions/occurrences/unique-equal-state/masking-joint/v1"

// Plan is the immutable original-source authority shared by compatible runs.
type Plan struct {
	Definition     string                   `json:"definition"`
	ToolVersion    string                   `json:"toolVersion"`
	Module         string                   `json:"module"`
	Settings       Settings                 `json:"settings"`
	Sources        []Source                 `json:"sources"`
	Decisions      []cover.DecisionMetadata `json:"decisions"`
	Clauses        []cover.ClauseMetadata   `json:"clauses"`
	NoMatches      []cover.NoMatchMetadata  `json:"noMatches"`
	UnknownSources int                      `json:"unknownSources"`
}

// Probes records successful generation, not advertised support.
type Probes = cover.Probes

type Diagnostic struct {
	Phase   string `json:"phase"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Package string `json:"package,omitempty"`
	Path    string `json:"path,omitempty"`
}

// Run retains collection provenance and the producer-owned acceptance decision.
// It contains no MC/DC result, summary, percentage, or presentation hierarchy.
type Run struct {
	ID          string                      `json:"id"`
	RuntimeID   string                      `json:"runtimeId"`
	Settings    Settings                    `json:"settings"`
	Status      cover.RunStatus             `json:"status"`
	FailureKind cover.RunFailureKind        `json:"failureKind"`
	Complete    bool                        `json:"complete"`
	Results     RunResults                  `json:"results"`
	Mode        string                      `json:"mode"`
	Executions  []MeasurementRun            `json:"executions"`
	Producers   []evidence.ProducerOutcome  `json:"producers"`
	Packages    map[string]string           `json:"packages"`
	ASTPackages map[string]string           `json:"astPackages"`
	Probes      Probes                      `json:"probes"`
	Runtime     runtimecov.RecordedEvidence `json:"runtime"`
	Cover       *c0.AcceptedCoverEvidence   `json:"cover"`
	Diagnostics []Diagnostic                `json:"diagnostics"`
}

type Archive struct {
	Plan Plan  `json:"plan"`
	Runs []Run `json:"runs"`
}

type envelope struct {
	Format      string          `json:"format"`
	Version     string          `json:"version"`
	SHA256      string          `json:"sha256"`
	Measurement json.RawMessage `json:"measurement"`
}

func Digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func validDigest(s string, size int) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == size && strings.ToLower(s) == s
}
func validPath(s string) bool {
	return s != "." && fs.ValidPath(s) && !strings.ContainsAny(s, "\\\x00")
}

// Validate verifies archive structure and accepted evidence without running Go,
// reading source files, or searching for a coverage witness.
func Validate(ctx context.Context, a Archive) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("validate measurement: %w", err)
	}
	p := a.Plan
	if p.Definition != Definition || p.Module == "" || p.ToolVersion == "" {
		return errors.New("unsupported or missing measurement definition")
	}
	if len(a.Runs) == 0 || p.UnknownSources < 0 {
		return errors.New("measurement needs execution records and a valid source inventory")
	}
	normalized, err := BuildSettings(p.Settings)
	if err != nil {
		return fmt.Errorf("plan build settings: %w", err)
	}
	if !reflect.DeepEqual(normalized, p.Settings) {
		return errors.New("plan settings are not normalized")
	}
	metrics, err := config.ParseCoverage(strings.Join(p.Settings.Coverage, ","))
	if err != nil {
		return fmt.Errorf("plan coverage selection: %w", err)
	}
	if p.Settings.GoEnvironment["GOVERSION"] == "" || p.Settings.GoEnvironment["GOOS"] == "" || p.Settings.GoEnvironment["GOARCH"] == "" {
		return errors.New("missing Go build identity")
	}
	for path, digest := range p.Settings.SourceDigests {
		if !validPath(path) || !validDigest(digest, sha256.Size) {
			return fmt.Errorf("invalid source manifest entry %q", path)
		}
	}
	sources := make(map[string]Source)
	for _, s := range p.Sources {
		key := s.PackagePath + "\x00" + s.Path
		if _, ok := sources[key]; ok {
			return fmt.Errorf("duplicate source %s/%s", s.PackagePath, s.Path)
		}
		if !slices.Contains(p.Settings.Packages, s.PackagePath) && !slices.Contains(p.Settings.Packages, strings.TrimSuffix(s.PackagePath, "_test")) {
			return fmt.Errorf("source %s belongs to unrequested package %q (requested %v)", s.Path, s.PackagePath, p.Settings.Packages)
		}
		if !validPath(s.Path) || Digest(s.Content) != s.SHA256 || p.Settings.SourceDigests[s.Path] != s.SHA256 {
			return fmt.Errorf("source digest or path mismatch for %s/%s", s.PackagePath, s.Path)
		}
		sources[key] = s
	}
	location := func(pkg string, l cover.SourceLocation) bool {
		s, ok := sources[pkg+"\x00"+l.File]
		return ok && l.StartOffset >= 0 && l.EndOffset >= l.StartOffset && l.EndOffset <= len(s.Content)
	}
	ids := make(map[uint64]bool)
	for _, d := range p.Decisions {
		if ids[uint64(d.ID)] || d.ModulePath != p.Module || !location(d.Package, d.Location) {
			return fmt.Errorf("decision %x at %s: invalid ownership", d.ID, d.Location.File)
		}
		ids[uint64(d.ID)] = true
		if uint64(d.ID) != analyzer.StableID(analyzer.CanonicalKey(d)) {
			return fmt.Errorf("decision %x at %s: identity mismatch", d.ID, d.Location.File)
		}
		atoms := make([]string, len(d.Conditions))
		for i, c := range d.Conditions {
			if int(c.Index) != i || !location(d.Package, c.Location) || uint64(c.ID) != analyzer.StableID(analyzer.ConditionCanonicalKey(d, c)) {
				return fmt.Errorf("decision %x condition %x: invalid layout", d.ID, c.ID)
			}
			atoms[i] = c.Expression
		}
		tree, err := analyzer.RestoreExpression(d.Expression, atoms)
		if err != nil {
			return fmt.Errorf("decision %x at %s: parse Boolean expression: %w", d.ID, d.Location.File, err)
		}
		if !reflect.DeepEqual(tree, d.ExpressionTree) {
			return fmt.Errorf("decision %x at %s: saved Boolean tree does not match its source expression", d.ID, d.Location.File)
		}
	}
	clauseIDs := make(map[cover.ClauseID]bool)
	switchIDs := make(map[cover.SwitchID]bool)
	for _, c := range p.Clauses {
		if clauseIDs[c.ID] || !location(c.Package, c.Location) || uint64(c.ID) != analyzer.StableID(analyzer.ClauseCanonicalKey(c)) {
			return fmt.Errorf("clause %x at %s: invalid ownership", c.ID, c.Location.File)
		}
		clauseIDs[c.ID] = true
	}
	for _, n := range p.NoMatches {
		if switchIDs[n.SwitchID] || !location(n.Package, n.Location) {
			return fmt.Errorf("switch %x at %s: invalid no-match ownership", n.SwitchID, n.Location.File)
		}
		switchIDs[n.SwitchID] = true
	}
	runIDs := make(map[string]bool)
	runtimeIDs := make(map[string]bool)
	for _, r := range a.Runs {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("validate execution %q: %w", r.ID, err)
		}
		if !validDigest(r.ID, 16) || r.ID != r.Settings.InvocationID || runIDs[r.ID] {
			return fmt.Errorf("execution %q: invalid or duplicate execution identity", r.ID)
		}
		runIDs[r.ID] = true
		if r.RuntimeID != "" {
			if !validDigest(r.RuntimeID, 16) {
				return fmt.Errorf("execution %q: invalid physical runtime identity", r.ID)
			}
			if runtimeIDs[r.RuntimeID] {
				return fmt.Errorf("execution %q: overlapping physical runs", r.ID)
			}
			runtimeIDs[r.RuntimeID] = true
		}
		settings, err := BuildSettings(r.Settings)
		if err != nil {
			return fmt.Errorf("execution %q build settings: %w", r.ID, err)
		}
		if !reflect.DeepEqual(settings, p.Settings) {
			return fmt.Errorf("execution %q: execution settings differ from plan", r.ID)
		}
		if r.Mode != "single-run" && r.Mode != "standard-cover" {
			return fmt.Errorf("execution %q: invalid measurement mode", r.ID)
		}
		if r.Status != cover.RunPassed && r.Status != cover.RunFailed && r.Status != cover.RunTimeout {
			return fmt.Errorf("execution %q: invalid execution status", r.ID)
		}
		if r.Complete && (r.Status != cover.RunPassed || r.FailureKind != cover.RunFailureNone || r.Results.Test != ResultPassed || r.Results.Measurement != ResultPassed || r.Results.Integrity != ResultPassed) {
			return fmt.Errorf("execution %q: inconsistent complete execution", r.ID)
		}
		for _, list := range [][]cover.ClauseID{r.Probes.ClauseBodies, r.Probes.ClauseSelections} {
			seen := map[cover.ClauseID]bool{}
			for _, id := range list {
				if !clauseIDs[id] || seen[id] {
					return fmt.Errorf("execution %q: invalid clause probe", r.ID)
				}
				seen[id] = true
			}
		}
		seenDec := map[cover.DecisionID]bool{}
		for _, id := range r.Probes.Decisions {
			if !ids[uint64(id)] || seenDec[id] {
				return fmt.Errorf("execution %q: invalid decision probe", r.ID)
			}
			seenDec[id] = true
		}
		seenSwitch := map[cover.SwitchID]bool{}
		for _, id := range r.Probes.NoMatches {
			if !switchIDs[id] || seenSwitch[id] {
				return fmt.Errorf("execution %q: invalid no-match probe", r.ID)
			}
			seenSwitch[id] = true
		}
		producers := map[evidence.ProducerName]bool{}
		_, issues := evidence.AcceptRuntimeByProducer(ctx, p.Decisions, p.Clauses, r.Runtime, r.RuntimeID, p.NoMatches)
		for _, outcome := range r.Producers {
			if producers[outcome.Producer] {
				return fmt.Errorf("execution %q producer %s: duplicate producer outcome", r.ID, outcome.Producer)
			}
			producers[outcome.Producer] = true
			switch outcome.Producer {
			case evidence.ProducerASTRuntime:
				if outcome.AllowsCoverage() && issues.ASTError() != nil {
					return fmt.Errorf("execution %q AST evidence: %w", r.ID, issues.ASTError())
				}
			case evidence.ProducerCompilerSelection:
				if outcome.AllowsCoverage() && issues.CompilerError() != nil {
					return fmt.Errorf("execution %q compiler evidence: %w", r.ID, issues.CompilerError())
				}
			case evidence.ProducerGoCover:
			default:
				return fmt.Errorf("execution %q producer %s: unknown evidence producer", r.ID, outcome.Producer)
			}
			switch outcome.Integrity {
			case evidence.ProducerIntegrityValid, evidence.ProducerIntegrityValidPrefix, evidence.ProducerIntegrityInvalid, evidence.ProducerIntegrityUnavailable:
			default:
				return fmt.Errorf("execution %q producer %s: invalid producer integrity", r.ID, outcome.Producer)
			}
			switch outcome.Completeness {
			case evidence.ProducerCompletenessComplete, evidence.ProducerCompletenessPartial, evidence.ProducerCompletenessUnavailable:
			default:
				return fmt.Errorf("execution %q producer %s: invalid producer completeness", r.ID, outcome.Producer)
			}
			switch outcome.Mapping {
			case evidence.ProducerMappingComplete, evidence.ProducerMappingInvalid, evidence.ProducerMappingUnavailable:
			default:
				return fmt.Errorf("execution %q producer %s: invalid producer mapping", r.ID, outcome.Producer)
			}
			if outcome.AllowsCoverage() && (outcome.Mapping != evidence.ProducerMappingComplete || (outcome.Integrity != evidence.ProducerIntegrityValid && outcome.Integrity != evidence.ProducerIntegrityValidPrefix) || outcome.Completeness == evidence.ProducerCompletenessUnavailable) {
				return fmt.Errorf("execution %q producer %s: accepted evidence has inconsistent producer outcome", r.ID, outcome.Producer)
			}
			switch outcome.Usability {
			case evidence.ProducerUsabilityAccepted, evidence.ProducerUsabilityAcceptedPartial, evidence.ProducerUsabilityRejected:
			default:
				return fmt.Errorf("execution %q producer %s: missing evidence acceptance decision", r.ID, outcome.Producer)
			}
		}
		needsAST := false
		for metric, enabled := range metrics {
			if enabled && metric != config.MetricStatement && metric != config.MetricFunction {
				needsAST = true
			}
		}
		if needsAST && !producers[evidence.ProducerASTRuntime] {
			return fmt.Errorf("execution %q: missing requested runtime producer", r.ID)
		}
		if (metrics.Enabled(config.MetricStatement) || metrics.Enabled(config.MetricFunction)) && (!producers[evidence.ProducerGoCover] || r.Cover == nil) {
			return fmt.Errorf("execution %q: missing requested counter producer or source inventory", r.ID)
		}
		if (metrics.Enabled(config.MetricSwitchClauseSelection) || metrics.Enabled(config.MetricTypeSwitchClauseSelection)) && !producers[evidence.ProducerCompilerSelection] {
			return fmt.Errorf("execution %q: missing requested selection producer", r.ID)
		}
		for _, status := range []ResultStatus{r.Results.Test, r.Results.Measurement, r.Results.Integrity, r.Results.Strict, r.Results.Threshold} {
			switch status {
			case ResultPassed, ResultFailed, ResultTimeout, ResultNotRun, ResultNotRequested:
			default:
				return fmt.Errorf("execution %q: invalid execution result", r.ID)
			}
		}
		if len(r.Runtime.Evaluations) > 0 && !producers[evidence.ProducerASTRuntime] {
			return fmt.Errorf("execution %q: runtime evidence without its producer", r.ID)
		}
		if r.Cover != nil {
			if !producers[evidence.ProducerGoCover] {
				return fmt.Errorf("execution %q: counter evidence without its producer", r.ID)
			}
			counterSources := make(map[string]bool, len(r.Cover.Sources))
			for _, cs := range r.Cover.Sources {
				key := cs.PackagePath + "\x00" + cs.OriginalPath
				counterSources[key] = true
				source, ok := sources[key]
				if !ok || !bytes.Equal(source.Content, cs.OriginalSource) {
					return fmt.Errorf("execution %q source %q: counter source differs from plan", r.ID, cs.OriginalPath)
				}
				inventory, err := c0.BuildInventory(cs.OriginalPath, source.Content)
				if err != nil {
					return fmt.Errorf("execution %q source %q: build counter inventory: %w", r.ID, cs.OriginalPath, err)
				}
				if !reflect.DeepEqual(inventory, cs.Inventory) {
					return fmt.Errorf("execution %q source %q: counter inventory differs from original source", r.ID, cs.OriginalPath)
				}
				for _, b := range cs.Blocks {
					if !b.Observed && b.Count > 0 && len(b.Statements) == 0 {
						return fmt.Errorf("execution %q: unobserved counter has a value", r.ID)
					}
					for _, s := range b.Statements {
						if !s.Observed && s.Count > 0 {
							return fmt.Errorf("execution %q: unobserved statement has a value", r.ID)
						}
					}
				}
			}
			for _, source := range p.Sources {
				key := source.PackagePath + "\x00" + source.Path
				if !counterSources[key] {
					return fmt.Errorf("execution %q: counter inventory is missing planned source %s/%s", r.ID, source.PackagePath, source.Path)
				}
			}
			if _, err := c0.ProjectAcceptedEvidence(ctx, p.Module, *r.Cover, c0.Options{}); err != nil {
				return fmt.Errorf("execution %q project counters: %w", r.ID, err)
			}
		}
	}
	return nil
}

// Merge performs an idempotent union of whole executions. An optional expected
// count detects missing CI shards; it counts unique executions, not input files.
func Merge(ctx context.Context, inputs []Archive, expected int) (Archive, error) {
	if expected < 0 {
		return Archive{}, errors.New("expected executions cannot be negative")
	}
	if len(inputs) == 0 {
		return Archive{}, errors.New("no measurement archives")
	}
	result := Archive{Plan: inputs[0].Plan}
	byID := map[string]Run{}
	for index, a := range inputs {
		if err := Validate(ctx, a); err != nil {
			return Archive{}, fmt.Errorf("merge input %d: %w", index+1, err)
		}
		if !reflect.DeepEqual(result.Plan, a.Plan) {
			return Archive{}, fmt.Errorf("merge input %d: incompatible measurement plans: source, packages, definitions, tool and build conditions must match", index+1)
		}
		for _, r := range a.Runs {
			if prior, ok := byID[r.ID]; ok && !reflect.DeepEqual(prior, r) {
				return Archive{}, fmt.Errorf("merge input %d: conflicting records of execution %q", index+1, r.ID)
			}
			byID[r.ID] = r
		}
	}
	for _, r := range byID {
		result.Runs = append(result.Runs, r)
	}
	slices.SortFunc(result.Runs, func(a, b Run) int { return strings.Compare(a.ID, b.ID) })
	if expected > 0 && len(result.Runs) != expected {
		return Archive{}, fmt.Errorf("expected %d executions, received %d", expected, len(result.Runs))
	}
	if err := Validate(ctx, result); err != nil {
		return Archive{}, fmt.Errorf("validate merged measurement: %w", err)
	}
	return result, nil
}

func canonical(data []byte) ([]byte, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	if err := eof(d); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}
func eof(d *json.Decoder) error {
	var v any
	if err := d.Decode(&v); err != io.EOF {
		if err != nil {
			return fmt.Errorf("read end of measurement: %w", err)
		}
		return errors.New("trailing JSON in measurement")
	}
	return nil
}
func Encode(a Archive) ([]byte, error) {
	if err := Validate(context.Background(), a); err != nil {
		return nil, fmt.Errorf("validate measurement for encoding: %w", err)
	}
	payload, err := json.Marshal(a)
	if err != nil {
		return nil, fmt.Errorf("marshal measurement payload: %w", err)
	}
	canonical, err := canonical(payload)
	if err != nil {
		return nil, fmt.Errorf("canonicalize measurement payload: %w", err)
	}
	out, err := json.MarshalIndent(envelope{Format, Version, Digest(canonical), payload}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal measurement envelope: %w", err)
	}
	return append(out, '\n'), nil
}
func Decode(reader io.Reader) (Archive, error) {
	d := json.NewDecoder(reader)
	d.DisallowUnknownFields()
	var e envelope
	if err := d.Decode(&e); err != nil {
		return Archive{}, fmt.Errorf("read measurement envelope: %w", err)
	}
	if err := eof(d); err != nil {
		return Archive{}, fmt.Errorf("read measurement envelope: %w", err)
	}
	if e.Format != Format || e.Version != Version {
		return Archive{}, fmt.Errorf("unsupported measurement format/version %q/%q", e.Format, e.Version)
	}
	data, err := canonical(e.Measurement)
	if err != nil {
		return Archive{}, fmt.Errorf("canonicalize measurement payload: %w", err)
	}
	if Digest(data) != e.SHA256 {
		return Archive{}, errors.New("measurement checksum mismatch")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var a Archive
	if err := d.Decode(&a); err != nil {
		return Archive{}, fmt.Errorf("decode measurement payload: %w", err)
	}
	encoded, err := json.Marshal(a)
	if err != nil {
		return Archive{}, fmt.Errorf("marshal decoded measurement: %w", err)
	}
	normalized, err := canonical(encoded)
	if err != nil {
		return Archive{}, fmt.Errorf("canonicalize decoded measurement: %w", err)
	}
	if !bytes.Equal(normalized, data) {
		return Archive{}, errors.New("measurement has missing fields or non-canonical values")
	}
	if err := Validate(context.Background(), a); err != nil {
		return Archive{}, fmt.Errorf("validate decoded measurement: %w", err)
	}
	return a, nil
}
func Write(ctx context.Context, path string, a Archive) error {
	data, err := Encode(a)
	if err != nil {
		return fmt.Errorf("encode measurement %q: %w", path, err)
	}
	return Publish(ctx, path, data)
}

// Publish atomically installs a complete artifact, preserving the previous file
// on cancellation or failure before rename.
func Publish(ctx context.Context, path string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("publish measurement %q: %w", path, err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".gomcdc-measurement-*")
	if err != nil {
		return fmt.Errorf("create temporary measurement for %q: %w", path, err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write measurement %q: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync measurement %q: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close measurement %q: %w", path, err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("publish measurement %q: %w", path, err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("publish measurement %q: %w", path, err)
	}
	return nil
}
