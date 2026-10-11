package resultfile

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/report"
)

func fixture() Result {
	source := []byte("package p\n// original source: \xff\n")
	built := report.Build(report.Input{ModulePath: "example.test/p", ToolVersion: "test-version"})
	built.Packages = []report.PackageReport{{Path: "example.test/p", Files: []report.FileReport{{Path: "p.go", Functions: []report.FunctionReport{{Statements: []report.StatementReport{{Count: 1<<60 + 1}}}}}}}}
	return New(Settings{
		WorkingDirectory: ".", GoEnvironment: map[string]string{"GOVERSION": "go1.26.0", "GOOS": "linux", "GOARCH": "amd64"},
		SourceDigests: map[string]string{"p.go": Digest(source)},
	}, built, []report.SourceFileInput{{PackagePath: "example.test/p", Path: "p.go", Source: source}})
}

func TestSavedResultPreservesSourceBytesAndReport(t *testing.T) {
	t.Parallel()
	want := fixture()
	encoded, err := Encode(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Sources[0].Content, want.Sources[0].Content) {
		t.Fatal("original source bytes changed")
	}
	wantJSON, _ := report.RenderJSONReport(want.Report)
	gotJSON, _ := report.RenderJSONReport(got.Report)
	if !bytes.Equal(wantJSON, gotJSON) {
		t.Fatal("report changed during serialization")
	}
	if got.WithSources().Packages[0].Files[0].Source.Source != string(want.Sources[0].Content) {
		t.Fatal("source view did not use saved bytes")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, encoded); err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(&compact); err != nil {
		t.Fatalf("whitespace-only change rejected: %v", err)
	}
}

func TestSavedResultRejectsCorruptionAndUnsupportedFormats(t *testing.T) {
	t.Parallel()
	encoded, err := Encode(fixture())
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"truncated":      encoded[:len(encoded)/2],
		"trailing":       append(bytes.Clone(encoded), []byte("{}")...),
		"future format":  bytes.Replace(encoded, []byte(`"version": "1.0"`), []byte(`"version": "9.0"`), 1),
		"changed report": bytes.Replace(encoded, []byte("test-version"), []byte("fake-version"), 1),
		"plain report":   []byte(`{"schemaVersion":"2.0"}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(bytes.NewReader(data)); err == nil {
				t.Fatal("invalid file accepted")
			}
		})
	}
	for _, test := range []struct {
		name, want string
		mutate     func(map[string]any)
	}{
		{"missing count", "missing fields", func(v map[string]any) {
			delete(v["report"].(map[string]any)["summary"].(map[string]any)["decision"].(map[string]any), "covered")
		}},
		{"missing settings", "missing fields", func(v map[string]any) { delete(v, "settings") }},
		{"unknown property", `unknown field "invented"`, func(v map[string]any) { v["invented"] = true }},
		{"report version", "unsupported report schema", func(v map[string]any) { v["report"].(map[string]any)["schemaVersion"] = "9.0" }},
		{"missing source", "missing original source", func(v map[string]any) { v["sources"] = []any{} }},
		{"wrong source digest", "saved source digest mismatch", func(v map[string]any) { v["sources"].([]any)[0].(map[string]any)["sha256"] = strings.Repeat("0", 64) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var saved envelope
			if err := json.Unmarshal(encoded, &saved); err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			decoder := json.NewDecoder(bytes.NewReader(saved.Result))
			decoder.UseNumber()
			if err := decoder.Decode(&value); err != nil {
				t.Fatal(err)
			}
			seal := func() []byte {
				payload, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				saved.Result, saved.SHA256 = payload, Digest(payload)
				data, err := json.Marshal(saved)
				if err != nil {
					t.Fatal(err)
				}
				return data
			}
			// A valid control ensures that integer rounding or checksum handling
			// cannot make a malformed-input test pass for an unrelated reason.
			if _, err := Decode(bytes.NewReader(seal())); err != nil {
				t.Fatalf("valid control: %v", err)
			}
			test.mutate(value)
			if _, err := Decode(bytes.NewReader(seal())); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("want %q after mutation, got %v", test.want, err)
			}
		})
	}
}

func TestWriteDoesNotReplaceExistingResultOnValidationFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "result.json")
	if err := Write(path, fixture()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	invalid := fixture()
	invalid.Sources[0].Path = "../outside.go"
	if err := Write(path, invalid); err == nil {
		t.Fatal("invalid source accepted")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("existing result was overwritten")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("saved result permissions: %v %v", info, err)
	}
}

func TestSavedResultPreservesCounterPrecisionAtIntegerBoundaries(t *testing.T) {
	t.Parallel()
	for _, count := range []uint64{0, 1<<53 - 1, 1 << 53, 1<<53 + 1, math.MaxUint64} {
		t.Run(strconv.FormatUint(count, 10), func(t *testing.T) {
			original := fixture()
			original.Report.Packages[0].Files[0].Functions[0].Statements[0].Count = count
			encoded, err := Encode(original)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := Decode(bytes.NewReader(encoded))
			if err != nil {
				t.Fatal(err)
			}
			if got := decoded.Report.Packages[0].Files[0].Functions[0].Statements[0].Count; got != count {
				t.Fatalf("counter = %d, want %d", got, count)
			}
			again, err := Encode(decoded)
			if err != nil || !bytes.Equal(encoded, again) {
				t.Fatalf("round trip changed payload or checksum: %v", err)
			}
		})
	}
}

func TestFailedPublicationPreservesDestinationAndRemovesTemporaryFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	destination := filepath.Join(root, "existing-directory")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(destination, "preserved.txt")
	if err := os.WriteFile(marker, []byte("previous contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	// An occupied directory deterministically fails the final rename after the
	// temporary file was written, including when tests run as root.
	if err := Write(destination, fixture()); err == nil {
		t.Fatal("publication over a directory succeeded")
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "previous contents" {
		t.Fatalf("existing destination changed: %q %v", data, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "existing-directory" {
		t.Fatalf("temporary publication file left behind: %v %v", entries, err)
	}
}

func TestSavedResultChecksumDetectsAdjacentLargeCounters(t *testing.T) {
	t.Parallel()
	const count uint64 = 1 << 60
	original := fixture()
	original.Report.Packages[0].Files[0].Functions[0].Statements[0].Count = count
	encoded, err := Encode(original)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(bytes.NewReader(encoded)); err != nil {
		t.Fatalf("valid original: %v", err)
	}
	old, changed := []byte(strconv.FormatUint(count, 10)), []byte(strconv.FormatUint(count+1, 10))
	if bytes.Count(encoded, old) != 1 {
		t.Fatal("fixture must contain exactly one counter to alter")
	}
	// These adjacent integers share one float64 representation. The checksum
	// must still distinguish them, even though a lossy decoder would not.
	altered := bytes.Replace(encoded, old, changed, 1)
	if _, err := Decode(bytes.NewReader(altered)); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("counter corruption: %v", err)
	}
}
