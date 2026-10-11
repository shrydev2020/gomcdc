package resultfile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sealUnchecked keeps a valid checksum so Decode reaches semantic validation.
func sealUnchecked(t *testing.T, result Result) []byte {
	t.Helper()
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	version := LegacyVersion
	if result.Settings.InvocationID != "" || len(result.Inputs) > 0 {
		version = Version
	}
	data, err := json.Marshal(envelope{Format: Format, Version: version, SHA256: Digest(canonical), Result: payload})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSavedResultValidatesMeasurementIdentityAndSourceOwnership(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, diagnostic string
		mutate           func(*Result)
	}{
		{"nonhex invocation", "invalid saved invocation identity", func(r *Result) { r.Settings.InvocationID = strings.Repeat("z", 32) }},
		{"short invocation", "invalid saved invocation identity", func(r *Result) { r.Settings.InvocationID = "aa" }},
		{"uppercase invocation", "invalid saved invocation identity", func(r *Result) { r.Settings.InvocationID = strings.Repeat("A", 32) }},
		{"missing tool", "missing report identity", func(r *Result) { r.Report.ToolVersion = "" }},
		{"missing module", "missing report identity", func(r *Result) { r.Report.Module = "" }},
		{"missing Go version", "missing Go measurement settings", func(r *Result) { delete(r.Settings.GoEnvironment, "GOVERSION") }},
		{"missing OS", "missing Go measurement settings", func(r *Result) { delete(r.Settings.GoEnvironment, "GOOS") }},
		{"missing architecture", "missing Go measurement settings", func(r *Result) { delete(r.Settings.GoEnvironment, "GOARCH") }},
		{"source traversal", "invalid source identity", func(r *Result) { r.Settings.SourceDigests["../p.go"] = Digest(nil) }},
		{"nonhex digest", "invalid source identity", func(r *Result) { r.Settings.SourceDigests["p.go"] = strings.Repeat("z", 64) }},
		{"short digest", "invalid source identity", func(r *Result) { r.Settings.SourceDigests["p.go"] = "aa" }},
		{"uppercase digest", "invalid source identity", func(r *Result) { r.Settings.SourceDigests["p.go"] = strings.Repeat("A", 64) }},
		{"configuration role", "invalid module configuration role", func(r *Result) { r.Settings.ModuleConfigDigests = map[string]string{"custom.mod": Digest(nil)} }},
		{"configuration digest", "invalid module configuration digest", func(r *Result) { r.Settings.ModuleConfigDigests = map[string]string{"go.mod": "aa"} }},
		{"source package absent", "invalid or duplicate saved source", func(r *Result) { r.Sources[0].PackagePath = "" }},
		{"source path traversal", "invalid or duplicate saved source", func(r *Result) { r.Sources[0].Path = "../p.go" }},
		{"duplicate source", "invalid or duplicate saved source", func(r *Result) { r.Sources = append(r.Sources, r.Sources[0]) }},
		{"changed bytes", "saved source digest mismatch", func(r *Result) { r.Sources[0].Content = []byte("package changed\n") }},
		{"changed manifest", "saved source digest mismatch", func(r *Result) { r.Settings.SourceDigests["p.go"] = Digest(nil) }},
		{"missing manifest entry", "saved source digest mismatch", func(r *Result) { delete(r.Settings.SourceDigests, "p.go") }},
		{"different package owner", "missing original source", func(r *Result) { r.Sources[0].PackagePath = "example.test/other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := c0Snapshot(strings.Repeat("a", 32))
			if _, err := Decode(bytes.NewReader(sealUnchecked(t, original))); err != nil {
				t.Fatalf("valid control: %v", err)
			}
			test.mutate(&original)
			_, encodeErr := Encode(original)
			_, decodeErr := Decode(bytes.NewReader(sealUnchecked(t, original)))
			for operation, err := range map[string]error{"encode": encodeErr, "decode": decodeErr} {
				if err == nil || !strings.Contains(err.Error(), test.diagnostic) {
					t.Fatalf("%s: want %q, got %v", operation, test.diagnostic, err)
				}
			}
		})
	}
}

func TestSavedWorkingDirectoryIsRelativeDescriptiveMetadata(t *testing.T) {
	t.Parallel()
	for _, directory := range []string{".", "..", "../workspace", "nested/package", "", "/absolute", "nested/../package", `nested\package`, "nul\x00path"} {
		t.Run(directory, func(t *testing.T) {
			result := fixture()
			result.Settings.WorkingDirectory = directory
			valid := directory == "." || directory == ".." || directory == "../workspace" || directory == "nested/package"
			_, encodeErr := Encode(result)
			decoded, decodeErr := Decode(bytes.NewReader(sealUnchecked(t, result)))
			if valid {
				if encodeErr != nil || decodeErr != nil || decoded.Settings.WorkingDirectory != directory {
					t.Fatalf("directory metadata round trip: %v %v", encodeErr, decodeErr)
				}
				return
			}
			for _, err := range []error{encodeErr, decodeErr} {
				if err == nil || !strings.Contains(err.Error(), "invalid working directory") {
					t.Fatalf("invalid directory: %v", err)
				}
			}
		})
	}
}

// Cancel when a completed temporary snapshot reaches the publication check.
// The filesystem condition avoids timer races and dependence on Err call counts.
type cancelAtPublication struct {
	context.Context
	cancel context.CancelFunc
	root   string
	staged []byte
	err    error
}

func (ctx *cancelAtPublication) Err() error {
	paths, err := filepath.Glob(filepath.Join(ctx.root, ".gomcdc-result-*"))
	if err != nil {
		ctx.err = err
	}
	if len(paths) == 1 {
		ctx.staged, ctx.err = os.ReadFile(paths[0])
		ctx.cancel()
	}
	return ctx.Context.Err()
}

func TestCancellationAfterStagingPreservesPublishedSnapshot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	destination := filepath.Join(root, "result.json")
	previous := fixture()
	if err := Write(destination, previous); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	replacement := c0Snapshot(strings.Repeat("a", 32))
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &cancelAtPublication{Context: parent, cancel: cancel, root: root}
	if err := WriteContext(ctx, destination, replacement); !errors.Is(err, context.Canceled) {
		t.Fatalf("publication cancellation: %v", err)
	}
	staged, err := Decode(bytes.NewReader(ctx.staged))
	if ctx.err != nil || err != nil || staged.Settings.InvocationID != replacement.Settings.InvocationID {
		t.Fatalf("cancellation must follow staging a complete replacement: %v %v", ctx.err, err)
	}
	after, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("previous snapshot changed: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "result.json" {
		t.Fatalf("staged file cleanup: %v %v", entries, err)
	}
}
