// Package resultfile stores a coverage analysis with its measurement archive.
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
	"os"
	"path/filepath"

	"github.com/shrydev2020/gomcdc/v2/internal/analysis"
	"github.com/shrydev2020/gomcdc/v2/internal/measurement"
)

const Format = "gomcdc-saved-result"
const Version = "2.0"

type Result struct {
	Measurement measurement.Archive `json:"measurement"`
	Report      analysis.Result     `json:"report"`
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

func NewMeasurement(archive measurement.Archive, built analysis.Result) Result {
	return Result{Measurement: archive, Report: built}
}

// SourceFiles returns embedded originals without consulting the checkout.
func (result Result) SourceFiles() []analysis.SourceFileInput {
	sources := make([]analysis.SourceFileInput, 0, len(result.Measurement.Plan.Sources))
	for _, source := range result.Measurement.Plan.Sources {
		sources = append(sources, analysis.SourceFileInput{PackagePath: source.PackagePath, Path: source.Path, Source: bytes.Clone(source.Content)})
	}
	return sources
}

func validate(result Result) error {
	if err := measurement.Validate(context.Background(), result.Measurement); err != nil {
		return fmt.Errorf("validate saved measurement: %w", err)
	}
	if result.Report.SchemaVersion != analysis.SchemaVersion && result.Report.SchemaVersion != analysis.OfflineSchemaVersion {
		return fmt.Errorf("unsupported report schema %q", result.Report.SchemaVersion)
	}
	if result.Report.ToolVersion == "" || result.Report.Module == "" {
		return errors.New("saved result is missing report identity")
	}
	if result.Report.Module != result.Measurement.Plan.Module {
		return errors.New("analysis module differs from measurement")
	}
	sources := make(map[string]bool)
	for _, source := range result.Measurement.Plan.Sources {
		sources[source.PackagePath+"\x00"+source.Path] = true
	}
	for _, pkg := range result.Report.Packages {
		for _, file := range pkg.Files {
			if !sources[pkg.Path+"\x00"+file.Path] {
				return fmt.Errorf("saved result is missing original source for %s/%s", pkg.Path, file.Path)
			}
		}
	}
	return nil
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
		if err != nil {
			return fmt.Errorf("read end of saved result: %w", err)
		}
		return errors.New("saved result contains trailing JSON")
	}
	return nil
}

// Encode seals the analysis and measurement archive with a corruption checksum.
func Encode(result Result) ([]byte, error) {
	if err := validate(result); err != nil {
		return nil, fmt.Errorf("validate saved result for encoding: %w", err)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal saved result payload: %w", err)
	}
	canonical, err := canonicalJSON(payload)
	if err != nil {
		return nil, fmt.Errorf("canonicalize saved result payload: %w", err)
	}
	encoded, err := json.MarshalIndent(envelope{Format: Format, Version: Version, SHA256: Digest(canonical), Result: payload}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal saved result envelope: %w", err)
	}
	return append(encoded, '\n'), nil
}

func Decode(reader io.Reader) (Result, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var saved envelope
	if err := decoder.Decode(&saved); err != nil {
		return Result{}, fmt.Errorf("read saved result: %w", err)
	}
	if err := requireEOF(decoder); err != nil {
		return Result{}, fmt.Errorf("read saved result envelope: %w", err)
	}
	if saved.Format != Format || saved.Version != Version {
		return Result{}, fmt.Errorf("unsupported saved result format/version %q/%q; use a file written by gomcdc test --save", saved.Format, saved.Version)
	}
	canonical, err := canonicalJSON(saved.Result)
	if err != nil {
		return Result{}, fmt.Errorf("canonicalize saved result payload: %w", err)
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
	// Every non-optional field must survive the typed round trip. Without this,
	// missing booleans or counts would silently become valid-looking zeroes.
	reencoded, err := json.Marshal(result)
	if err != nil {
		return Result{}, fmt.Errorf("marshal decoded saved result: %w", err)
	}
	normalized, err := canonicalJSON(reencoded)
	if err != nil {
		return Result{}, fmt.Errorf("canonicalize decoded saved result: %w", err)
	}
	if !bytes.Equal(canonical, normalized) {
		return Result{}, errors.New("saved result has missing fields or non-canonical field values")
	}
	if err := validate(result); err != nil {
		return Result{}, fmt.Errorf("validate decoded saved result: %w", err)
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
		return fmt.Errorf("save analysis %q: %w", path, err)
	}
	contents, err := Encode(result)
	if err != nil {
		return fmt.Errorf("encode analysis %q: %w", path, err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("save analysis %q: %w", path, err)
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".gomcdc-result-*")
	if err != nil {
		return fmt.Errorf("create temporary analysis for %q: %w", path, err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(contents); err != nil {
		return fmt.Errorf("write analysis %q: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync analysis %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close analysis %q: %w", path, err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("publish analysis %q: %w", path, err)
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return fmt.Errorf("publish analysis %q: %w", path, err)
	}
	return nil
}
