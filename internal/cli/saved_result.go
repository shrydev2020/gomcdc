package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"

	"github.com/shrydev2020/gomcdc/v2/internal/goflags"
	"github.com/shrydev2020/gomcdc/v2/internal/loader"
	"github.com/shrydev2020/gomcdc/v2/internal/processgroup"
	"github.com/shrydev2020/gomcdc/v2/internal/resultfile"
	"github.com/shrydev2020/gomcdc/v2/internal/workspace"
)

func runReport(ctx context.Context, workingDir string, args []string, stdout, stderr io.Writer) int {
	opts := options{}
	fs := flag.NewFlagSet("gomcdc report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.format, "format", "text", "report format: text, json, or html")
	fs.StringVar(&opts.output, "output", "", "report output file, or directory for html (default: stdout)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: gomcdc report [options] <saved-result>")
		fmt.Fprintln(stderr, "Render a gomcdc test --save result without running Go or tests.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitSuccess
		}
		return ExitInvalidUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "gomcdc: report requires exactly one saved-result file; put options before the file")
		return ExitInvalidUsage
	}
	if err := validateOptions(opts); err != nil {
		fmt.Fprintf(stderr, "gomcdc: %v\n", err)
		return ExitInvalidUsage
	}
	inputPath := resolveOutputPath(workingDir, fs.Arg(0))
	if err := checkDistinctOutput(inputPath, opts, workingDir); err != nil {
		fmt.Fprintf(stderr, "gomcdc: %v\n", err)
		return ExitInvalidUsage
	}
	if ctx.Err() != nil {
		return ExitInterrupted
	}
	input, err := os.Open(inputPath)
	if err != nil {
		fmt.Fprintf(stderr, "gomcdc: read saved result: %v\n", err)
		return ExitMeasurementFailed
	}
	saved, err := resultfile.Decode(input)
	input.Close()
	if err != nil {
		fmt.Fprintf(stderr, "gomcdc: %v\n", err)
		return ExitMeasurementFailed
	}
	if ctx.Err() != nil {
		return ExitInterrupted
	}
	built := saved.Report
	if opts.format == "html" {
		built = saved.WithSources()
	}
	if err := writeReport(opts, built, workingDir, stdout); err != nil {
		fmt.Fprintf(stderr, "gomcdc: report generation failed: %v\n", err)
		return ExitMeasurementFailed
	}
	// This command reports rendering success. Original test and policy outcomes
	// remain in the saved report; they are neither rerun nor replaced.
	return ExitSuccess
}

func resolveOutputPath(workingDir, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(workingDir, path)
}

// checkDistinctOutput protects both the saved input and a newly requested save
// from being overwritten by the report renderer (including HTML's index.html).
func checkDistinctOutput(savedPath string, opts options, workingDir string) error {
	if opts.output == "" || opts.output == "-" {
		return nil
	}
	outputPath := resolveOutputPath(workingDir, opts.output)
	if opts.format == "html" {
		outputPath = filepath.Join(outputPath, "index.html")
	}
	left, err := canonicalDestination(savedPath)
	if err != nil {
		return err
	}
	right, err := canonicalDestination(outputPath)
	if err != nil {
		return err
	}
	same := left == right
	if a, err := os.Stat(savedPath); err == nil {
		if b, err := os.Stat(outputPath); err == nil && os.SameFile(a, b) {
			same = true
		}
	}
	if same {
		return errors.New("saved result and report output must be different files")
	}
	return nil
}

func canonicalDestination(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	// EvalSymlinks cannot resolve a dangling link. It may become live when
	// --save publishes its target, so compare that target before measuring.
	if info, statErr := os.Lstat(absolute); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		target, readErr := os.Readlink(absolute)
		if readErr != nil {
			return "", readErr
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(absolute), target)
		}
		return canonicalDestination(target)
	}
	parent, err := canonicalDestination(filepath.Dir(absolute))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(absolute)), nil
}

// captureResultSettings runs before source instrumentation and test execution.
// Only a fixed list of Go build environment values is read; unrelated process
// environment values (including credentials) are never archived.
func captureResultSettings(ctx context.Context, opts options, loaded loader.Result, work *workspace.Workspace, goFlags string) (resultfile.Settings, error) {
	goWork := work.GoWorkPath
	if goWork == "" {
		goWork = "off"
	}
	queryFlags, err := goflags.Without(goFlags, map[string]bool{"modfile": true})
	if err != nil {
		return resultfile.Settings{}, err
	}
	if work.ModFilePath != "" {
		queryFlags += " " + goflags.Join([]string{"-modfile=" + work.ModFilePath})
	}
	command := exec.CommandContext(ctx, "go", "env", "-json", "GOVERSION", "GOOS", "GOARCH", "CGO_ENABLED", "GOEXPERIMENT", "GOAMD64", "GOARM64", "GOARM", "GO386", "GOMIPS", "GOMIPS64", "GOPPC64", "GORISCV64")
	command.Dir = work.WorkingDir
	command.Env = append(os.Environ(), "GOWORK="+goWork, "GOFLAGS="+queryFlags)
	processgroup.ConfigureCancellation(command)
	output, err := command.Output()
	if err != nil {
		return resultfile.Settings{}, fmt.Errorf("capture Go measurement settings: %w", err)
	}
	var environment map[string]string
	if err := json.Unmarshal(output, &environment); err != nil {
		return resultfile.Settings{}, err
	}
	workingDir, err := filepath.Rel(work.ModuleDir, work.WorkingDir)
	if err != nil {
		return resultfile.Settings{}, err
	}
	arguments := opts.goTestArgs.Prefix()
	if binaryArgs, boundary := opts.goTestArgs.BinaryArgs(); boundary {
		arguments = append(arguments, "-args")
		arguments = append(arguments, binaryArgs...)
	}
	settings := resultfile.Settings{
		WorkingDirectory: filepath.ToSlash(workingDir),
		PackagePatterns:  slices.Clone(opts.patterns), Packages: slices.Clone(loaded.PackageImportSet),
		Coverage: opts.metrics.Names(), GoTestArgs: arguments, GOFLAGS: goFlags,
		GoEnvironment: environment, IncludeTests: opts.includeTests, Excludes: slices.Clone([]string(opts.excludes)),
		Timeout: opts.timeout.String(), Strict: opts.strict, FailUnder: make(map[string]float64),
		SourceDigests: make(map[string]string),
	}
	settings.InvocationID, err = newRunID()
	if err != nil {
		return resultfile.Settings{}, fmt.Errorf("identify saved invocation: %w", err)
	}
	for _, threshold := range thresholdSettings(opts) {
		if threshold.value.set {
			settings.FailUnder[threshold.name] = threshold.value.value
		}
	}
	for _, files := range [][]loader.File{loaded.Files, loaded.TestFiles} {
		for _, file := range files {
			if ctx.Err() != nil {
				return resultfile.Settings{}, ctx.Err()
			}
			path, err := filepath.Rel(work.ModuleDir, file.Path)
			if err != nil {
				return resultfile.Settings{}, err
			}
			path = filepath.ToSlash(path)
			if _, exists := settings.SourceDigests[path]; exists {
				continue
			}
			contents, err := os.ReadFile(file.Path)
			if err != nil {
				return resultfile.Settings{}, err
			}
			settings.SourceDigests[path] = resultfile.Digest(contents)
		}
	}
	return settings, nil
}

func validateSaveDestination(opts options, workingDir string) error {
	if opts.save == "" {
		return nil
	}
	return checkDistinctOutput(resolveOutputPath(workingDir, opts.save), opts, workingDir)
}
