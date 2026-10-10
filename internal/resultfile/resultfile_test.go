package resultfile

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
	for name, mutate := range map[string]func(map[string]any){
		"missing count": func(v map[string]any) {
			delete(v["report"].(map[string]any)["summary"].(map[string]any)["decision"].(map[string]any), "covered")
		},
		"missing settings":    func(v map[string]any) { delete(v, "settings") },
		"unknown property":    func(v map[string]any) { v["invented"] = true },
		"report version":      func(v map[string]any) { v["report"].(map[string]any)["schemaVersion"] = "9.0" },
		"missing source":      func(v map[string]any) { v["sources"] = []any{} },
		"wrong source digest": func(v map[string]any) { v["sources"].([]any)[0].(map[string]any)["sha256"] = strings.Repeat("0", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			var saved envelope
			if err := json.Unmarshal(encoded, &saved); err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			if err := json.Unmarshal(saved.Result, &value); err != nil {
				t.Fatal(err)
			}
			mutate(value)
			saved.Result, _ = json.Marshal(value)
			canonical, _ := canonicalJSON(saved.Result)
			saved.SHA256 = Digest(canonical)
			data, _ := json.Marshal(saved)
			if _, err := Decode(bytes.NewReader(data)); err == nil {
				t.Fatal("invalid payload accepted even with updated checksum")
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
