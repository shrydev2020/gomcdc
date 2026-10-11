package measurement

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/c0"
	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
	"github.com/shrydev2020/gomcdc/v2/internal/evidence"
)

func emptyArchive(t *testing.T) Archive {
	t.Helper()
	settings := Settings{WorkingDirectory: ".", Packages: []string{"example.test/empty"}, Coverage: []string{"decision"}, GoEnvironment: map[string]string{"GOVERSION": "go1.26.0", "GOOS": "linux", "GOARCH": "amd64"}}
	planSettings, err := BuildSettings(settings)
	if err != nil {
		t.Fatal(err)
	}
	settings.InvocationID = strings.Repeat("a", 32)
	return Archive{Plan: Plan{Definition: Definition, ToolVersion: "test", Module: "example.test/empty", Settings: planSettings}, Runs: []Run{{ID: settings.InvocationID, Settings: settings, Status: cover.RunPassed, FailureKind: cover.RunFailureNone, Complete: true, Results: RunResults{Test: ResultPassed, Measurement: ResultPassed, Integrity: ResultPassed, Strict: ResultNotRequested, Threshold: ResultNotRequested}, Mode: "single-run", Producers: []evidence.ProducerOutcome{{Producer: evidence.ProducerASTRuntime, Integrity: evidence.ProducerIntegrityValid, Completeness: evidence.ProducerCompletenessComplete, Mapping: evidence.ProducerMappingComplete, Usability: evidence.ProducerUsabilityAccepted}}}}}
}

func TestArchiveRejectsContradictoryFacts(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*Archive)
	}{
		{"missing producer", func(a *Archive) { a.Runs[0].Producers = nil }},
		{"invalid acceptance", func(a *Archive) { a.Runs[0].Producers[0].Mapping = evidence.ProducerMappingInvalid }},
		{"unknown status", func(a *Archive) { a.Runs[0].Results.Test = "other" }},
		{"failed complete", func(a *Archive) { a.Runs[0].Status = cover.RunFailed }},
		{"unknown probe", func(a *Archive) { a.Runs[0].Probes.Decisions = []cover.DecisionID{42} }},
		{"changed build", func(a *Archive) { a.Runs[0].Settings.GOFLAGS = "-race" }},
		{"duplicate run", func(a *Archive) { a.Runs = append(a.Runs, a.Runs[0]) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := emptyArchive(t)
			tt.mutate(&a)
			if err := Validate(t.Context(), a); err == nil {
				t.Fatal("contradictory measurement accepted")
			}
		})
	}
}

func TestArchiveRoundTripAndAtomicPublication(t *testing.T) {
	original := emptyArchive(t)
	data, err := Encode(original)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	again, err := Encode(decoded)
	if err != nil || !bytes.Equal(data, again) {
		t.Fatal("archive is not stable after decoding", err)
	}
	if _, err := Decode(bytes.NewReader(append(data, []byte("{}")...))); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	path := filepath.Join(t.TempDir(), "measurement.json")
	if err := Write(t.Context(), path, original); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := Publish(ctx, path, []byte("partial")); err == nil {
		t.Fatal("canceled publication succeeded")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, after) {
		t.Fatal("publication damaged prior archive", err)
	}
}

func TestCounterSourcesCannotNarrowTheSourcePlan(t *testing.T) {
	a := emptyArchive(t)
	source := Source{PackagePath: a.Plan.Module, Path: "p.go", Content: []byte("package empty\nfunc F(){println(1)}\n")}
	source.SHA256 = Digest(source.Content)
	a.Plan.Sources = []Source{source}
	a.Runs[0].Settings.SourceDigests = map[string]string{source.Path: source.SHA256}
	a.Runs[0].Settings.Coverage = []string{"statement"}
	var err error
	a.Plan.Settings, err = BuildSettings(a.Runs[0].Settings)
	if err != nil {
		t.Fatal(err)
	}
	a.Runs[0].Mode = "standard-cover"
	a.Runs[0].Producers[0].Producer = evidence.ProducerGoCover
	inventory, err := c0.BuildInventory(source.Path, source.Content)
	if err != nil {
		t.Fatal(err)
	}
	a.Runs[0].Cover = &c0.AcceptedCoverEvidence{Mode: c0.ModeSet, Sources: []c0.AcceptedSourceEvidence{{PackagePath: source.PackagePath, OriginalPath: source.Path, OriginalSource: source.Content, Inventory: inventory}}}
	if err := Validate(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	a.Runs[0].Cover.Sources = nil
	if err := Validate(t.Context(), a); err == nil {
		t.Fatal("missing counter source silently removed original obligations")
	}
}
