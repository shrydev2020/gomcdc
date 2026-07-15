//go:build !windows

package gotest

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cover "github.com/shrydev2020/gomcdc/internal/coverage"
)

func TestRunForcesFreshTestAndSeparatesOutput(t *testing.T) {
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "go"), "#!/bin/sh\nprintf 'data=%s args=%s\\n' \"$GOMCDC_DATA_DIR\" \"$*\"\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	var output bytes.Buffer
	result := Run(context.Background(), Options{
		Dir:        t.TempDir(),
		Patterns:   []string{"./..."},
		Args:       []string{"-run", "TestOne"},
		DataDirEnv: "GOMCDC_DATA_DIR",
		DataDir:    "/tmp/events with spaces",
		Output:     &output,
	})
	if result.Status != cover.RunPassed || result.Err != nil {
		t.Fatalf("Run() = %#v", result)
	}
	got := output.String()
	if !strings.Contains(got, "data=/tmp/events with spaces") {
		t.Fatalf("output does not contain environment: %q", got)
	}
	if !strings.Contains(got, "args=test ./... -run TestOne -count=1") {
		t.Fatalf("output does not contain forced fresh run: %q", got)
	}
	if strings.Contains(got, "-json") {
		t.Fatalf("plain mode unexpectedly forced JSON output: %q", got)
	}
}

func TestRunPlainOutputClassifiesBuildTestAndGoTestTimeoutFailures(t *testing.T) {
	tests := []struct {
		name        string
		script      string
		wantStatus  cover.RunStatus
		wantFailure cover.RunFailureKind
		wantPackage PackageStatus
	}{
		{
			name: "build failure",
			script: "#!/bin/sh\n" +
				"printf '# example.test/broken\\nbroken.go:3:2: undefined: missing\\nFAIL\\texample.test/broken [build failed]\\n'\n" +
				"exit 1\n",
			wantStatus:  cover.RunFailed,
			wantFailure: cover.RunFailureBuild,
			wantPackage: PackageBuildFailed,
		},
		{
			name: "setup failure",
			script: "#!/bin/sh\n" +
				"printf '# example.test/broken\\nFAIL\\texample.test/broken [setup failed]\\n'\n" +
				"exit 1\n",
			wantStatus:  cover.RunFailed,
			wantFailure: cover.RunFailureBuild,
			wantPackage: PackageBuildFailed,
		},
		{
			name: "test failure",
			script: "#!/bin/sh\n" +
				"printf '%s\\n' '--- FAIL: TestBroken (0.00s)'\n" +
				"printf 'FAIL\\texample.test/broken\\t0.001s\\n'\n" +
				"exit 1\n",
			wantStatus:  cover.RunFailed,
			wantFailure: cover.RunFailureTest,
			wantPackage: PackageFailed,
		},
		{
			name: "go test timeout",
			script: "#!/bin/sh\n" +
				"printf 'panic: test timed out after 1s\\n'\n" +
				"printf 'FAIL\\texample.test/broken\\t1.001s\\n'\n" +
				"exit 1\n",
			wantStatus:  cover.RunTimeout,
			wantFailure: cover.RunFailureTimeout,
			wantPackage: PackageFailed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bin := t.TempDir()
			writeExecutable(t, filepath.Join(bin, "go"), test.script)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			var output bytes.Buffer
			result := Run(context.Background(), Options{
				Dir:      t.TempDir(),
				Patterns: []string{"example.test/broken"},
				Output:   &output,
			})
			if result.Status != test.wantStatus || result.FailureKind != test.wantFailure || result.Err == nil {
				t.Fatalf("Run() = %#v, output=%q", result, output.String())
			}
			if got := result.Packages["example.test/broken"]; got != test.wantPackage {
				t.Fatalf("package status = %q, want %q; output=%q", got, test.wantPackage, output.String())
			}
		})
	}
}

func TestRunPlainModeDoesNotAddJSONFlag(t *testing.T) {
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "go"), `#!/bin/sh
for argument in "$@"; do
	case "$argument" in
		-json|-json=*)
			printf 'unexpected JSON flag: %s\n' "$argument"
			exit 97
			;;
	esac
done
printf 'ok  \texample.test/p\t0.001s\n'
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var output bytes.Buffer
	result := Run(context.Background(), Options{
		Dir:      t.TempDir(),
		Patterns: []string{"example.test/p"},
		Output:   &output,
	})
	if result.Status != cover.RunPassed || result.FailureKind != cover.RunFailureNone || result.Err != nil {
		t.Fatalf("Run() = %#v, output=%q", result, output.String())
	}
	if got := result.Packages["example.test/p"]; got != PackagePassed {
		t.Fatalf("package status = %q, want %q", got, PackagePassed)
	}
}

func TestPlainEventWriterAcceptsAlignedGoTestPrefixes(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	events := &eventWriter{
		output:        &output,
		packages:      make(map[string]PackageStatus),
		buildFailures: make(map[string]struct{}),
	}
	writer := &plainEventWriter{events: events}
	for _, line := range []string{
		"ok  \texample.test/passed\t0.001s\n",
		"?   \texample.test/skipped\t[no test files]\n",
		"\texample.test/coverage-only\t\tcoverage: 0.0% of statements\n",
		"FAIL\texample.test/broken [build failed]\n",
	} {
		if _, err := writer.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	writer.Flush()

	if got := events.packages["example.test/passed"]; got != PackagePassed {
		t.Fatalf("passed package status = %q, want %q", got, PackagePassed)
	}
	if got := events.packages["example.test/skipped"]; got != PackageSkipped {
		t.Fatalf("skipped package status = %q, want %q", got, PackageSkipped)
	}
	if got := events.packages["example.test/coverage-only"]; got != PackageSkipped {
		t.Fatalf("coverage-only package status = %q, want %q", got, PackageSkipped)
	}
	if got := events.packages["example.test/broken"]; got != PackageBuildFailed {
		t.Fatalf("broken package status = %q, want %q", got, PackageBuildFailed)
	}
}

func TestRunOverridesCountAndCoverProfile(t *testing.T) {
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "go"), "#!/bin/sh\nprintf '%s\\n' \"$*\"\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var output bytes.Buffer
	result := Run(context.Background(), Options{
		Dir:           t.TempDir(),
		Patterns:      []string{"example.test/p"},
		Args:          []string{"-count=4", "-coverprofile", "user.out", "-run", "TestX", "-args", "-custom"},
		CoverProfile:  "/tmp/tool.out",
		CoverPackages: []string{"example.test/p", "example.test/shared"},
		Environment:   map[string]string{"RUN_ID": "abc"},
		Toolexec:      "/tmp/tool exec",
		Output:        &output,
	})
	if result.Status != cover.RunPassed {
		t.Fatalf("Run() = %#v", result)
	}
	want := "test example.test/p -run TestX -coverprofile=/tmp/tool.out -coverpkg=example.test/p,example.test/shared -toolexec='/tmp/tool exec' -count=1 -args -custom"
	if got := strings.TrimSpace(output.String()); got != want {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

func TestQuoteGoCommandArgument(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		argument  string
		want      string
		wantError bool
	}{
		{name: "plain", argument: "/tmp/tool", want: "/tmp/tool"},
		{name: "space", argument: "/tmp/tool exec", want: "'/tmp/tool exec'"},
		{name: "single quote", argument: "/tmp/tool's exec", want: `"/tmp/tool's exec"`},
		{name: "both quotes", argument: `/tmp/tool's "exec`, wantError: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			got, err := quoteGoCommandArgument(test.argument)
			if (err != nil) != test.wantError || got != test.want {
				t.Fatalf("quoteGoCommandArgument(%q) = %q, %v; want %q, error=%t", test.argument, got, err, test.want, test.wantError)
			}
		})
	}
}

func TestRemoveForcedFlagsOverridesExplicitJSONValue(t *testing.T) {
	t.Parallel()
	got := removeForcedFlags([]string{"-json=false", "-run", "TestX"})
	if want := []string{"-run", "TestX"}; strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("removeForcedFlags = %q, want %q", got, want)
	}
}

func TestRunASTMeasurementOwnsAllCoverageFlags(t *testing.T) {
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "go"), "#!/bin/sh\nprintf 'goflags=%s args=%s\\n' \"$GOFLAGS\" \"$*\"\nprintf 'ok\\texample.test/p\\t0.001s\\n'\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GOFLAGS", `-tags=integration -cover -covermode=atomic -coverprofile=bad.out -coverpkg=bad/... -count=7 -json`)
	var output bytes.Buffer
	result := Run(context.Background(), Options{
		Dir:          t.TempDir(),
		Patterns:     []string{"example.test/p"},
		Args:         []string{"-cover", "-covermode", "count", "-coverprofile=user.out", "-run", "TestX"},
		DisableCover: true,
		Output:       &output,
	})
	if result.Status != cover.RunPassed {
		t.Fatalf("Run() = %#v; output=%s", result, output.String())
	}
	text := output.String()
	if !strings.Contains(text, "goflags=-tags=integration") || !strings.Contains(text, "args=test example.test/p -run TestX -cover=false -count=1") {
		t.Fatalf("measurement flags were not owned by the tool: %q", text)
	}
	for _, forbidden := range []string{"bad.out", "bad/...", "-covermode", "-coverprofile=user.out", "-count=7", "-json"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("output retained %q: %q", forbidden, text)
		}
	}
}

func TestEventWriterStreamsOutputAndTracksPackages(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	writer := &eventWriter{output: &output, packages: make(map[string]PackageStatus)}
	first := `{"Action":"start","Package":"example.test/a"}` + "\n" +
		`{"Action":"output","Package":"example.test/a","Output":"hello\n"}` + "\n" +
		`{"Action":"output","Package":"example.test/a","Output":"gomcdc runtime diagnostic: disk unavailable\n"}` + "\n" +
		`{"Action":"pass","Package":"example.test/a"}` + "\n" +
		`{"Action":"fail","Package":"example.test/b"}`
	for _, chunk := range []string{first[:17], first[17:61], first[61:]} {
		if _, err := writer.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	writer.Flush()
	if got := output.String(); got != "hello\ngomcdc runtime diagnostic: disk unavailable\n" {
		t.Fatalf("output = %q", got)
	}
	if got := writer.packages["example.test/a"]; got != PackagePassed {
		t.Fatalf("package a = %q", got)
	}
	if got := writer.packages["example.test/b"]; got != PackageFailed {
		t.Fatalf("package b = %q", got)
	}
	if got := strings.Join(writer.runtimeDiagnostics, ","); got != "disk unavailable" {
		t.Fatalf("runtime diagnostics = %q", got)
	}
}

func TestRunReportsFailure(t *testing.T) {
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "go"), "#!/bin/sh\nexit 1\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	result := Run(context.Background(), Options{Dir: t.TempDir(), DataDirEnv: "COVER", DataDir: t.TempDir()})
	if result.Status != cover.RunFailed || result.FailureKind != cover.RunFailureCommand || result.Err == nil {
		t.Fatalf("Run() = %#v, want failed", result)
	}
}

func TestRunReportsTimeout(t *testing.T) {
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "go"), "#!/bin/sh\nwhile :; do :; done\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result := Run(ctx, Options{Dir: t.TempDir(), DataDirEnv: "COVER", DataDir: t.TempDir()})
	if result.Status != cover.RunTimeout || result.FailureKind != cover.RunFailureTimeout || result.Err == nil {
		t.Fatalf("Run() = %#v, want timeout", result)
	}
}

func TestRunReportsCallerInterruptionSeparatelyFromTimeout(t *testing.T) {
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "go"), "#!/bin/sh\nwhile :; do :; done\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	result := Run(ctx, Options{Dir: t.TempDir(), DataDirEnv: "COVER", DataDir: t.TempDir()})
	if result.Status != cover.RunFailed || result.FailureKind != cover.RunFailureInterrupted || result.Err == nil {
		t.Fatalf("Run() = %#v, want caller interruption", result)
	}
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}
