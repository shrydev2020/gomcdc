// Package gotest runs one measurement-owned go test invocation.
package gotest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	cover "github.com/shrydev2020/gomcdc/internal/coverage"
	"github.com/shrydev2020/gomcdc/internal/goflags"
)

type Options struct {
	Dir      string
	Patterns []string
	Args     []string
	// CoverProfile, when non-empty, is forced in place of any user-supplied
	// -coverprofile flag. The same rule applies to -count: coverage always runs
	// exactly once with -count=1 so cached results can never satisfy the run.
	CoverProfile  string
	CoverPackages []string
	DataDirEnv    string
	DataDir       string
	Environment   map[string]string
	JSON          bool
	// DisableCover forces the AST measurement to remain free of cmd/cover
	// instrumentation, including flags inherited through GOFLAGS.
	DisableCover bool
	Output       io.Writer
}

type PackageStatus string

const (
	PackageStarted     PackageStatus = "started"
	PackagePassed      PackageStatus = "passed"
	PackageFailed      PackageStatus = "failed"
	PackageBuildFailed PackageStatus = "build-failed"
	PackageSkipped     PackageStatus = "skipped"
)

type Result struct {
	Status             cover.RunStatus
	FailureKind        cover.RunFailureKind
	Err                error
	Packages           map[string]PackageStatus
	RuntimeDiagnostics []string
}

// Run disables the Go test result cache because cached tests cannot emit
// runtime coverage observations into this run's fresh data directory.
func Run(ctx context.Context, opts Options) Result {
	goArgs, binaryArgs := splitBinaryArgs(opts.Args)
	goArgs = removeForcedFlags(goArgs)
	args := []string{"test"}
	args = append(args, opts.Patterns...)
	args = append(args, goArgs...)
	if opts.CoverProfile != "" {
		args = append(args, "-coverprofile="+opts.CoverProfile)
		if len(opts.CoverPackages) > 0 {
			args = append(args, "-coverpkg="+strings.Join(opts.CoverPackages, ","))
		}
	}
	if opts.JSON {
		args = append(args, "-json")
	}
	if opts.DisableCover {
		args = append(args, "-cover=false")
	}
	args = append(args, "-count=1")
	if len(binaryArgs) > 0 {
		args = append(args, "-args")
		args = append(args, binaryArgs...)
	}
	cmd := exec.CommandContext(ctx, "go", args...)
	configureCancellation(cmd)
	cmd.Dir = opts.Dir
	environment := append([]string(nil), os.Environ()...)
	if opts.DataDirEnv != "" {
		environment = setEnvironment(environment, opts.DataDirEnv, opts.DataDir)
	}
	for key, value := range opts.Environment {
		environment = setEnvironment(environment, key, value)
	}
	filteredGOFLAGS, filterErr := goflags.WithoutMeasurementFlags(environmentValue(environment, "GOFLAGS"))
	if filterErr != nil {
		return Result{
			Status:      cover.RunFailed,
			FailureKind: cover.RunFailureCommand,
			Err:         fmt.Errorf("parse GOFLAGS: %w", filterErr),
			Packages:    make(map[string]PackageStatus),
		}
	}
	environment = setEnvironment(environment, "GOFLAGS", filteredGOFLAGS)
	cmd.Env = environment
	if opts.Output == nil {
		opts.Output = io.Discard
	}
	// Reports may use stdout as a machine-readable stream, so all child output
	// is deliberately kept on the diagnostic stream supplied by the caller.
	events := &eventWriter{
		output:        opts.Output,
		packages:      make(map[string]PackageStatus),
		buildFailures: make(map[string]struct{}),
	}
	var plain *plainEventWriter
	if opts.JSON {
		cmd.Stdout = events
		cmd.Stderr = opts.Output
	} else {
		plain = &plainEventWriter{events: events}
		// One shared comparable writer lets os/exec serialize stdout/stderr
		// writes while preserving ordinary (non -json) test semantics.
		cmd.Stdout = plain
		cmd.Stderr = plain
	}

	err := cmd.Run()
	if opts.JSON {
		events.Flush()
	} else {
		plain.Flush()
	}
	packageStatuses := events.packages
	if err == nil {
		return Result{Status: cover.RunPassed, FailureKind: cover.RunFailureNone, Packages: packageStatuses, RuntimeDiagnostics: runtimeDiagnostics(events)}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return Result{Status: cover.RunTimeout, FailureKind: cover.RunFailureTimeout, Err: fmt.Errorf("go test timed out: %w", ctx.Err()), Packages: packageStatuses, RuntimeDiagnostics: runtimeDiagnostics(events)}
	}
	failureKind := classifyFailure(events)
	status := cover.RunFailed
	if failureKind == cover.RunFailureTimeout {
		status = cover.RunTimeout
	}
	return Result{Status: status, FailureKind: failureKind, Err: fmt.Errorf("go test failed: %w", err), Packages: packageStatuses, RuntimeDiagnostics: runtimeDiagnostics(events)}
}

func splitBinaryArgs(arguments []string) (goArguments, binaryArguments []string) {
	for index, argument := range arguments {
		if argument == "-args" || argument == "--args" {
			return append([]string(nil), arguments[:index]...), append([]string(nil), arguments[index+1:]...)
		}
	}
	return append([]string(nil), arguments...), nil
}

func removeForcedFlags(arguments []string) []string {
	result := make([]string, 0, len(arguments))
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		name := goflags.Name(argument)
		if name == "json" || name == "cover" {
			continue
		}
		if name == "count" || name == "covermode" || name == "coverprofile" || name == "coverpkg" {
			if !strings.Contains(argument, "=") && index+1 < len(arguments) {
				index++
			}
			continue
		}
		result = append(result, argument)
	}
	return result
}

func environmentValue(environment []string, key string) string {
	prefix := key + "="
	for index := len(environment) - 1; index >= 0; index-- {
		if strings.HasPrefix(environment[index], prefix) {
			return strings.TrimPrefix(environment[index], prefix)
		}
	}
	return ""
}

func setEnvironment(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

type testEvent struct {
	Action      string `json:"Action"`
	Package     string `json:"Package"`
	ImportPath  string `json:"ImportPath"`
	Test        string `json:"Test"`
	Output      string `json:"Output"`
	FailedBuild string `json:"FailedBuild"`
}

// eventWriter incrementally decodes go test -json without retaining an
// unbounded test log. Human-readable Output fields are forwarded to the
// diagnostic stream while package terminal states remain available to mark
// partial reports accurately.
type eventWriter struct {
	pending            bytes.Buffer
	output             io.Writer
	packages           map[string]PackageStatus
	buildFailures      map[string]struct{}
	testFailed         bool
	executionFailed    bool
	goTestTimedOut     bool
	runtimeDiagnostics []string
}

// plainEventWriter preserves the semantics of an ordinary go test invocation
// (notably testing.Verbose) while extracting only stable line-oriented status
// signals needed for partial reporting.
type plainEventWriter struct {
	pending bytes.Buffer
	events  *eventWriter
}

func (w *plainEventWriter) Write(data []byte) (int, error) {
	length := len(data)
	if _, err := w.pending.Write(data); err != nil {
		return 0, err
	}
	for {
		contents := w.pending.Bytes()
		newline := bytes.IndexByte(contents, '\n')
		if newline < 0 {
			break
		}
		line := append([]byte(nil), contents[:newline]...)
		w.pending.Next(newline + 1)
		w.consume(line, true)
	}
	return length, nil
}

func (w *plainEventWriter) Flush() {
	if w.pending.Len() == 0 {
		return
	}
	line := append([]byte(nil), w.pending.Bytes()...)
	w.pending.Reset()
	w.consume(line, false)
}

func (w *plainEventWriter) consume(line []byte, newline bool) {
	forward := append([]byte(nil), line...)
	if newline {
		forward = append(forward, '\n')
	}
	_, _ = w.events.output.Write(forward)
	text := strings.TrimSpace(string(line))
	w.events.inspectOutput(text)
	if strings.HasPrefix(text, "--- FAIL:") {
		w.events.testFailed = true
	}
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return
	}
	if len(fields) >= 5 && fields[1] == "coverage:" && strings.HasSuffix(fields[2], "%") && fields[3] == "of" && fields[4] == "statements" {
		// With -coverprofile/-coverpkg, Go prints packages without test files as
		// an indented coverage-only summary rather than a conventional "?" line.
		w.events.packages[fields[0]] = PackageSkipped
		return
	}
	packagePath := fields[1]
	switch fields[0] {
	case "ok":
		w.events.packages[packagePath] = PackagePassed
	case "?":
		w.events.packages[packagePath] = PackageSkipped
	case "FAIL":
		if strings.Contains(text, "[build failed]") || strings.Contains(text, "[setup failed]") {
			w.events.buildFailures[packagePath] = struct{}{}
			w.events.packages[packagePath] = PackageBuildFailed
		} else {
			w.events.executionFailed = true
			w.events.packages[packagePath] = PackageFailed
		}
	}
}

func (w *eventWriter) Write(data []byte) (int, error) {
	length := len(data)
	if _, err := w.pending.Write(data); err != nil {
		return 0, err
	}
	for {
		contents := w.pending.Bytes()
		newline := bytes.IndexByte(contents, '\n')
		if newline < 0 {
			break
		}
		line := append([]byte(nil), contents[:newline]...)
		w.pending.Next(newline + 1)
		w.consume(line)
	}
	return length, nil
}

func (w *eventWriter) Flush() {
	if w.pending.Len() != 0 {
		line := append([]byte(nil), w.pending.Bytes()...)
		w.pending.Reset()
		w.consume(line)
	}
}

func (w *eventWriter) consume(line []byte) {
	var event testEvent
	if err := json.Unmarshal(line, &event); err != nil {
		_, _ = w.output.Write(append(line, '\n'))
		return
	}
	if event.Output != "" {
		_, _ = io.WriteString(w.output, event.Output)
		for _, line := range strings.Split(event.Output, "\n") {
			w.inspectOutput(line)
		}
	}
	if event.Action == "build-fail" && event.ImportPath != "" {
		w.buildFailures[event.ImportPath] = struct{}{}
		w.packages[event.ImportPath] = PackageBuildFailed
		return
	}
	if event.Test != "" {
		if event.Action == "fail" {
			w.testFailed = true
		}
		return
	}
	if event.Package == "" || event.Test != "" {
		return
	}
	switch event.Action {
	case "start":
		w.packages[event.Package] = PackageStarted
	case "pass":
		w.packages[event.Package] = PackagePassed
	case "fail":
		_, buildFailed := w.buildFailures[event.Package]
		if event.FailedBuild != "" || buildFailed {
			w.buildFailures[event.Package] = struct{}{}
			w.packages[event.Package] = PackageBuildFailed
		} else {
			w.packages[event.Package] = PackageFailed
			w.executionFailed = true
		}
	case "skip":
		w.packages[event.Package] = PackageSkipped
	}
}

func (w *eventWriter) inspectOutput(line string) {
	if strings.Contains(line, "panic: test timed out after ") || strings.Contains(line, "test timed out after ") {
		w.goTestTimedOut = true
	}
	const prefix = "gomcdc runtime diagnostic:"
	if index := strings.Index(line, prefix); index >= 0 {
		message := strings.TrimSpace(line[index+len(prefix):])
		if message != "" {
			w.runtimeDiagnostics = append(w.runtimeDiagnostics, message)
		}
	}
}

func classifyFailure(writer *eventWriter) cover.RunFailureKind {
	if writer == nil {
		return cover.RunFailureCommand
	}
	if writer.goTestTimedOut {
		return cover.RunFailureTimeout
	}
	hasBuildFailure := len(writer.buildFailures) > 0
	hasTestFailure := writer.testFailed || writer.executionFailed
	switch {
	case hasBuildFailure && hasTestFailure:
		return cover.RunFailureMixed
	case hasBuildFailure:
		return cover.RunFailureBuild
	case hasTestFailure:
		return cover.RunFailureTest
	default:
		return cover.RunFailureCommand
	}
}

func runtimeDiagnostics(writer *eventWriter) []string {
	if writer == nil {
		return nil
	}
	return append([]string(nil), writer.runtimeDiagnostics...)
}
