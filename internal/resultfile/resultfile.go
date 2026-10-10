// Package resultfile stores completed report models and their original sources.
// Reading a saved result never runs Go, loads a checkout, or recomputes coverage.
// Analyze explicitly derives a new report from the original snapshots.
package resultfile

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
	"path"
	"path/filepath"
	"strings"

	"github.com/shrydev2020/gomcdc/v2/internal/report"
)

const Format = "gomcdc-saved-result"
const LegacyVersion = "1.0"
const Version = "1.1"

// Settings describes the measured invocation, not the machine rendering it.
// GoTestArgs and GOFLAGS describe the caller's request before path relocation.
type Settings struct {
	InvocationID        string             `json:"invocationId,omitempty"`
	WorkingDirectory    string             `json:"workingDirectory"`
	PackagePatterns     []string           `json:"packagePatterns"`
	Packages            []string           `json:"packages"`
	Coverage            []string           `json:"coverage"`
	GoTestArgs          []string           `json:"goTestArgs"`
	GOFLAGS             string             `json:"goFlags"`
	GoEnvironment       map[string]string  `json:"goEnvironment"`
	IncludeTests        bool               `json:"includeTests"`
	Excludes            []string           `json:"excludes"`
	Timeout             string             `json:"timeout"`
	Strict              bool               `json:"strict"`
	FailUnder           map[string]float64 `json:"failUnder"`
	SourceDigests       map[string]string  `json:"sourceDigests"`
	ModuleConfigDigests map[string]string  `json:"moduleConfigDigests"`
}

// Source uses base64-encoded bytes in JSON to preserve byte offsets even in
// source comments containing invalid UTF-8. Paths are module-relative.
type Source struct {
	PackagePath string `json:"packagePath"`
	Path        string `json:"path"`
	Content     []byte `json:"content"`
	SHA256      string `json:"sha256"`
}

type Result struct {
	Settings Settings      `json:"settings"`
	Report   report.Report `json:"report"`
	Sources  []Source      `json:"sources"`
	Inputs   []Snapshot    `json:"inputs,omitempty"`
}

type envelope struct {
	Format  string          `json:"format"`
	Version string          `json:"version"`
	SHA256  string          `json:"sha256"`
	Result  json.RawMessage `json:"result"`
}

func Digest(contents []byte) string {
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}

func New(settings Settings, built report.Report, sources []report.SourceFileInput) Result {
	result := Result{Settings: settings, Report: built, Sources: make([]Source, 0, len(sources))}
	for _, source := range sources {
		result.Sources = append(result.Sources, Source{
			PackagePath: source.PackagePath, Path: source.Path,
			Content: bytes.Clone(source.Source), SHA256: Digest(source.Source),
		})
	}
	return result
}

// WithSources attaches only the embedded originals, never current disk files.
func (result Result) WithSources() report.Report {
	sources := make([]report.SourceFileInput, 0, len(result.Sources))
	for _, source := range result.Sources {
		sources = append(sources, report.SourceFileInput{
			PackagePath: source.PackagePath, Path: source.Path, Source: source.Content,
		})
	}
	return report.WithSourceViews(result.Report, sources)
}

func validate(result Result) error {
	if id := result.Settings.InvocationID; id != "" {
		decoded, err := hex.DecodeString(id)
		if err != nil || len(decoded) != 16 || strings.ToLower(id) != id {
			return errors.New("invalid saved invocation identity")
		}
	}
	if result.Report.SchemaVersion != report.SchemaVersion && result.Report.SchemaVersion != report.OfflineSchemaVersion {
		return fmt.Errorf("unsupported report schema %q", result.Report.SchemaVersion)
	}
	if result.Report.ToolVersion == "" || result.Report.Module == "" {
		return errors.New("saved result is missing report identity")
	}
	if result.Settings.GoEnvironment["GOVERSION"] == "" || result.Settings.GoEnvironment["GOOS"] == "" || result.Settings.GoEnvironment["GOARCH"] == "" {
		return errors.New("saved result is missing Go measurement settings")
	}
	// A caller can run from the parent go.work directory. This is descriptive
	// metadata only and is never used as a path by the offline renderer.
	directory := result.Settings.WorkingDirectory
	if directory == "" || path.IsAbs(directory) || path.Clean(directory) != directory || strings.ContainsAny(directory, "\\\x00") {
		return errors.New("saved result has an invalid working directory")
	}
	for path, digest := range result.Settings.SourceDigests {
		if !validPath(path) || !validDigest(digest) {
			return fmt.Errorf("invalid source identity %q", path)
		}
	}
	for role, digest := range result.Settings.ModuleConfigDigests {
		switch role {
		case "go.mod", "go.work", "go.work.sum", "alternate.mod", "alternate.sum":
		default:
			return fmt.Errorf("invalid module configuration role %q", role)
		}
		if !validDigest(digest) {
			return fmt.Errorf("invalid module configuration digest for %q", role)
		}
	}
	sources := make(map[string]bool, len(result.Sources))
	for _, source := range result.Sources {
		key := source.PackagePath + "\x00" + source.Path
		if source.PackagePath == "" || !validPath(source.Path) || sources[key] {
			return fmt.Errorf("invalid or duplicate saved source %q", source.Path)
		}
		if source.SHA256 != Digest(source.Content) || result.Settings.SourceDigests[source.Path] != source.SHA256 {
			return fmt.Errorf("saved source digest mismatch for %q", source.Path)
		}
		sources[key] = true
	}
	for _, pkg := range result.Report.Packages {
		for _, file := range pkg.Files {
			if !sources[pkg.Path+"\x00"+file.Path] {
				return fmt.Errorf("saved result is missing original source for %s/%s", pkg.Path, file.Path)
			}
		}
	}
	if err := validateInputs(result); err != nil {
		return err
	}
	return nil
}

func validPath(path string) bool {
	return path != "." && fs.ValidPath(path) && !strings.ContainsAny(path, "\\\x00") && !filepath.IsAbs(path)
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

// canonicalJSON ignores object key order and insignificant whitespace while
// retaining integers exactly, including counters larger than 2^53.
func canonicalJSON(data []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := requireEOF(decoder); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("saved result contains trailing or invalid JSON")
	}
	return nil
}

// Encode seals a report snapshot. The checksum detects accidental corruption;
// it is not a signature or a claim of authenticity for untrusted input.
func Encode(result Result) ([]byte, error) {
	if err := validate(result); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	canonical, err := canonicalJSON(payload)
	if err != nil {
		return nil, err
	}
	version := LegacyVersion
	if len(result.Inputs) > 0 || result.Settings.InvocationID != "" {
		version = Version
	}
	encoded, err := json.MarshalIndent(envelope{Format: Format, Version: version, SHA256: Digest(canonical), Result: payload}, "", "  ")
	return append(encoded, '\n'), err
}

func Decode(reader io.Reader) (Result, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var saved envelope
	if err := decoder.Decode(&saved); err != nil {
		return Result{}, fmt.Errorf("read saved result: %w", err)
	}
	if err := requireEOF(decoder); err != nil {
		return Result{}, err
	}
	if saved.Format != Format || (saved.Version != LegacyVersion && saved.Version != Version) {
		return Result{}, fmt.Errorf("unsupported saved result format/version %q/%q; use a file written by gomcdc test --save", saved.Format, saved.Version)
	}
	canonical, err := canonicalJSON(saved.Result)
	if err != nil {
		return Result{}, err
	}
	if Digest(canonical) != saved.SHA256 {
		return Result{}, errors.New("saved result checksum mismatch")
	}
	decoder = json.NewDecoder(bytes.NewReader(saved.Result))
	decoder.DisallowUnknownFields()
	var result Result
	if err := decoder.Decode(&result); err != nil {
		return Result{}, fmt.Errorf("decode saved result: %w", err)
	}
	if (saved.Version == Version) != (len(result.Inputs) > 0 || result.Settings.InvocationID != "") {
		return Result{}, errors.New("saved result version does not match its analysis inputs")
	}
	// Every non-optional field must survive the typed round trip. Without this,
	// missing booleans or counts would silently become valid-looking zeroes.
	reencoded, err := json.Marshal(result)
	if err != nil {
		return Result{}, err
	}
	normalized, err := canonicalJSON(reencoded)
	if err != nil || !bytes.Equal(canonical, normalized) {
		return Result{}, errors.New("saved result has missing fields or non-canonical field values")
	}
	if err := validate(result); err != nil {
		return Result{}, err
	}
	return result, nil
}

// Write publishes a complete, private snapshot with an atomic rename. An error
// before publication leaves an existing saved result untouched.
func Write(path string, result Result) error {
	return WriteContext(context.Background(), path, result)
}

// WriteContext checks cancellation before publishing. Measurement uses Write
// so a recovered partial snapshot can still be saved after an interrupted test.
func WriteContext(ctx context.Context, path string, result Result) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	contents, err := Encode(result)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".gomcdc-result-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(contents); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
