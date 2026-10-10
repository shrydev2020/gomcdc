package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/report"
	"github.com/shrydev2020/gomcdc/v2/internal/resultfile"
)

func TestSavedReportRendersOfflineWithoutRerunningTests(t *testing.T) {
	for _, test := range []struct {
		name, metrics, failure string
		exit                   int
	}{
		{"all metrics", "all", "", ExitSuccess},
		{"failed test", "statement,function,decision,condition,mcdc-masking", `t.Error("expected failure")`, ExitTestsFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			configureIntegrationEnvironment(t)
			t.Setenv("GOFLAGS", "-tags=saved_result_fixture")
			root, outputs := t.TempDir(), t.TempDir()
			marker := filepath.Join(outputs, "runs")
			t.Setenv("GOMCDC_SAVED_RESULT_TEST_MARKER", marker)
			writeIntegrationFile(t, filepath.Join(root, "go.mod"), "module example.test/saved\n\ngo 1.26\n")
			writeIntegrationFile(t, filepath.Join(root, "logic.go"), `package saved
func Choose(a, b bool) int {
 if a && b { return 1 }; switch { case a: return 2; default: return 3 }
}
`)
			writeIntegrationFile(t, filepath.Join(root, "logic_test.go"), `package saved
import ("testing"; "os")
func TestMeasure(t *testing.T) {
 f, err := os.OpenFile(os.Getenv("GOMCDC_SAVED_RESULT_TEST_MARKER"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
 if err != nil { t.Fatal(err) }; defer f.Close(); f.WriteString("x")
 Choose(true, true); Choose(true, false); Choose(false, false)
 `+test.failure+`
}
`)
			savedPath := filepath.Join(outputs, "saved.json")
			var stdout, stderr bytes.Buffer
			args := []string{"test", "--coverage=" + test.metrics, "--include-tests", "--strict", "--exclude=ignored/**", "--save=" + savedPath, "--format=html", "--output=" + filepath.Join(outputs, "original")}
			if test.failure != "" {
				args = append(args, "--fail-under-decision=100")
			}
			args = append(args, ".", "--", "-run", "TestMeasure")
			code := runAt(t.Context(), root, args, &stdout, &stderr)
			if code != test.exit {
				t.Fatalf("exit=%d want=%d\n%s", code, test.exit, stderr.String())
			}
			before, err := os.ReadFile(filepath.Join(outputs, "original", "index.html"))
			if err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(savedPath)
			if err != nil {
				t.Fatal(err)
			}
			saved, err := resultfile.Decode(file)
			file.Close()
			if err != nil {
				t.Fatal(err)
			}
			if saved.Settings.GOFLAGS != "-tags=saved_result_fixture" || !saved.Settings.IncludeTests || !saved.Settings.Strict || !reflect.DeepEqual(saved.Settings.GoTestArgs, []string{"-run", "TestMeasure"}) || len(saved.Settings.SourceDigests) != 2 {
				t.Fatalf("wrong saved settings: %+v", saved.Settings)
			}
			if test.failure != "" && (saved.Report.Run.Results.Test != report.ResultFailed || saved.Report.Run.Results.Threshold != report.ResultFailed || saved.Settings.FailUnder["decision"] != 100) {
				t.Fatalf("failed outcomes were not preserved: %+v", saved.Report.Run)
			}
			if saved.Settings.ModuleConfigDigests["go.mod"] != resultfile.Digest([]byte("module example.test/saved\n\ngo 1.26\n")) {
				t.Fatal("original module configuration was not recorded")
			}
			// Remove both the checkout and Go from PATH before using report. This
			// verifies the user's portable-report workflow, not just serialization.
			if err := os.RemoveAll(root); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", t.TempDir())
			stderr.Reset()
			code = runAt(t.Context(), outputs, []string{"report", "--format=html", "--output=offline", "saved.json"}, &stdout, &stderr)
			if code != ExitSuccess {
				t.Fatalf("offline render exit=%d: %s", code, stderr.String())
			}
			after, err := os.ReadFile(filepath.Join(outputs, "offline", "index.html"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("offline HTML differs: %v", err)
			}
			stdout.Reset()
			code = runAt(t.Context(), outputs, []string{"report", "--format=json", "saved.json"}, &stdout, &stderr)
			wantJSON, _ := report.RenderJSONReport(saved.Report)
			if code != ExitSuccess || !bytes.Equal(stdout.Bytes(), wantJSON) {
				t.Fatal("offline JSON changed the saved report")
			}
			stdout.Reset()
			code = runAt(t.Context(), outputs, []string{"report", "saved.json"}, &stdout, &stderr)
			if code != ExitSuccess || stdout.String() != report.RenderTextReport(saved.Report) {
				t.Fatal("offline text changed the saved report")
			}
			if runs, err := os.ReadFile(marker); err != nil || string(runs) != "x" {
				t.Fatalf("test ran more than once: %q %v", runs, err)
			}
		})
	}
}

func TestSavedResultCLIRejectsInvalidRequestsBeforeRunningGo(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	root := t.TempDir()
	writeIntegrationFile(t, filepath.Join(root, "input.json"), `{"schemaVersion":"2.0"}`)
	for _, args := range [][]string{
		{"report"}, {"report", "a", "b"}, {"report", "--format=html", "input.json"},
		{"report", "--format=lcov", "input.json"}, {"report", "--output=input.json", "input.json"},
		{"report", "--format=html", "--output=.", "index.html"},
		{"test", "--save=-", "."}, {"test", "--save=x", "--output=x", "."},
		{"test", "--save=html/index.html", "--format=html", "--output=html", "."},
	} {
		var diagnostics bytes.Buffer
		if code := runAt(t.Context(), root, args, io.Discard, &diagnostics); code != ExitInvalidUsage {
			t.Errorf("%v: exit=%d: %s", args, code, diagnostics.String())
		}
	}
	for _, input := range []string{"input.json", "missing.json"} {
		if code := runAt(t.Context(), root, []string{"report", input}, io.Discard, io.Discard); code != ExitMeasurementFailed {
			t.Errorf("%s: exit=%d", input, code)
		}
	}
	if code := runAt(t.Context(), root, []string{"report", "--help"}, io.Discard, io.Discard); code != ExitSuccess {
		t.Fatal("report help failed")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if code := runAt(ctx, root, []string{"report", "input.json"}, io.Discard, io.Discard); code != ExitInterrupted {
		t.Fatal("report ignored cancellation")
	}
}

func TestSavedReportPreservesPartialBuildFailure(t *testing.T) {
	configureIntegrationEnvironment(t)
	path := filepath.Join(t.TempDir(), "partial.json")
	built, diagnostics, code := runFixture(t, fixturePath(t, "partial"), "--save="+path, "--strict", "--format=json", "./...")
	if code != ExitMeasurementFailed || built.Run.Complete {
		t.Fatalf("exit=%d run=%+v: %s", code, built.Run, diagnostics)
	}
	t.Setenv("PATH", t.TempDir())
	var output, stderr bytes.Buffer
	if code := runAt(t.Context(), t.TempDir(), []string{"report", "--format=json", path}, &output, &stderr); code != ExitSuccess {
		t.Fatalf("partial result could not be rendered: %d %s", code, stderr.String())
	}
	want, err := report.RenderJSONReport(built)
	if err != nil || !bytes.Equal(output.Bytes(), want) {
		t.Fatalf("partial evidence changed: %v", err)
	}
}

func TestSavedReportFromWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	module := filepath.Join(root, "module")
	goWork := filepath.Join(root, "go.work")
	writeIntegrationFile(t, goWork, "go 1.26\n\nuse ./module\n")
	writeIntegrationFile(t, filepath.Join(module, "go.mod"), "module example.test/workroot\n\ngo 1.26\n")
	writeIntegrationFile(t, filepath.Join(module, "value.go"), "package value\nfunc Value() int { return 7 }\n")
	writeIntegrationFile(t, filepath.Join(module, "value_test.go"), "package value\nimport \"testing\"\nfunc TestValue(t *testing.T) { Value() }\n")
	t.Setenv("GOWORK", goWork)
	path := filepath.Join(t.TempDir(), "workspace.json")
	var stderr bytes.Buffer
	code := runAt(t.Context(), root, []string{"test", "--coverage=statement,function", "--save=" + path, "./module/..."}, io.Discard, &stderr)
	if code != ExitSuccess {
		t.Fatalf("save from workspace root: %d %s", code, stderr.String())
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	saved, err := resultfile.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Settings.WorkingDirectory != ".." || saved.Settings.ModuleConfigDigests["go.work"] != resultfile.Digest([]byte("go 1.26\n\nuse ./module\n")) {
		t.Fatalf("workspace settings lost: %+v", saved.Settings)
	}
}

func TestReportRejectsAliasedInputOutput(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	input := filepath.Join(root, "input.json")
	writeIntegrationFile(t, input, "preserve me")
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			alias := filepath.Join(root, kind)
			link := os.Symlink
			if kind == "hardlink" {
				link = os.Link
			}
			if err := link(input, alias); err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			if code := runAt(t.Context(), root, []string{"report", "--output=" + alias, input}, io.Discard, &stderr); code != ExitInvalidUsage || !strings.Contains(stderr.String(), "different files") {
				t.Fatalf("alias not rejected: %d %s", code, stderr.String())
			}
			assertIntegrationFileContents(t, input, "preserve me")
		})
	}
	t.Run("dangling output symlink", func(t *testing.T) {
		if err := os.Symlink("future.json", filepath.Join(root, "dangling")); err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		code := runAt(t.Context(), root, []string{"test", "--save=future.json", "--output=dangling", "."}, io.Discard, &stderr)
		if code != ExitInvalidUsage || !strings.Contains(stderr.String(), "different files") {
			t.Fatalf("dangling alias not rejected: %d %s", code, stderr.String())
		}
	})
}
