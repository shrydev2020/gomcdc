package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/shrydev2020/gomcdc/v2/internal/analysis"
	"github.com/shrydev2020/gomcdc/v2/internal/buildinfo"
	"github.com/shrydev2020/gomcdc/v2/internal/config"
	"github.com/shrydev2020/gomcdc/v2/internal/mcdc"
	"github.com/shrydev2020/gomcdc/v2/internal/measurement"
	"github.com/shrydev2020/gomcdc/v2/internal/requirements"
	"github.com/shrydev2020/gomcdc/v2/internal/resultfile"
)

func runRecord(ctx context.Context, dir string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(stdout, "Usage: gomcdc record --output measurement.json [measurement options] PACKAGE... [-- go test arguments]")
		fmt.Fprintln(stdout, "Execute tests once and save evidence without MC/DC analysis.\n\nMeasurement options: --coverage, --exclude, --include-tests, --timeout, --workdir, --keep-workdir.")
		return ExitSuccess
	}
	opts, err := parseOptions(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitSuccess
		}
		fmt.Fprintln(stderr, err)
		return ExitInvalidUsage
	}
	// Reject explicit analysis/presentation flags, including default values.
	// Inspect parsed flags so package names and output paths are never flags.
	for _, name := range opts.explicitFlags {
		if name == "save" || name == "format" || name == "strict" || strings.HasPrefix(name, "fail-under-") || strings.HasPrefix(name, "mcdc-masking-max-") {
			fmt.Fprintf(stderr, "gomcdc: --%s belongs to analysis, report, or require, not record\n", name)
			return ExitInvalidUsage
		}
	}
	if opts.output == "" || opts.output == "-" {
		fmt.Fprintln(stderr, "gomcdc: record requires --output to name a measurement file")
		return ExitInvalidUsage
	}
	opts.recordOnly = true
	opts.save = opts.output
	opts.output = ""
	return runCoverage(ctx, dir, opts, stdout, stderr)
}

// runMeasurementStage is strictly offline. It never starts Go or reads sources
// from the current checkout. The artifact decides its format, never its suffix.
func runMeasurementStage(ctx context.Context, dir, command string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var output string
	var expected int
	var pairs, states, solver optionalUint64
	fs.StringVar(&output, "output", "", "destination artifact (required)")
	fs.IntVar(&expected, "expect-runs", 0, "required number of unique executions; detects missing CI shards")
	if command == "analyze" {
		fs.Var(&pairs, "mcdc-masking-max-evaluation-pairs", "candidate pair limit per condition")
		fs.Var(&states, "mcdc-masking-max-search-states", "search state limit per condition")
		fs.Var(&solver, "mcdc-masking-max-solver-bytes", "solver backing-array byte limit per condition")
	}
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: gomcdc %s --output FILE [options] MEASUREMENT...\n", command)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitSuccess
		}
		return ExitInvalidUsage
	}
	if output == "" || output == "-" || fs.NArg() < 1 || expected < 0 {
		fs.Usage()
		return ExitInvalidUsage
	}
	for _, limit := range []optionalUint64{pairs, states, solver} {
		if limit.set && limit.value == 0 {
			fmt.Fprintln(stderr, "gomcdc: analysis limits must be positive")
			return ExitInvalidUsage
		}
	}
	archives := make([]measurement.Archive, 0, fs.NArg())
	for _, path := range fs.Args() {
		if ctx.Err() != nil {
			return ExitInterrupted
		}
		path = resolveOutputPath(dir, path)
		if err := checkDistinctOutput(path, options{output: output, format: "json"}, dir); err != nil {
			fmt.Fprintln(stderr, err)
			return ExitInvalidUsage
		}
		f, err := os.Open(path)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return ExitMeasurementFailed
		}
		a, err := measurement.Decode(f)
		f.Close()
		if err != nil {
			fmt.Fprintf(stderr, "gomcdc: %s: read measurement %q: %v\n", command, path, err)
			return ExitMeasurementFailed
		}
		archives = append(archives, a)
	}
	archive, err := measurement.Merge(ctx, archives, expected)
	if err == nil && command == "merge" {
		err = measurement.Write(ctx, resolveOutputPath(dir, output), archive)
	}
	if err == nil && command == "analyze" {
		if (pairs.set || states.set || solver.set) && !strings.Contains(","+strings.Join(archive.Plan.Settings.Coverage, ",")+",", ",mcdc-masking,") {
			fmt.Fprintln(stderr, "gomcdc: Masking limits require mcdc-masking")
			return ExitInvalidUsage
		}
		var built analysis.Result
		settings := analysis.NewSettings(mcdc.AnalysisBudget{MaxEvaluationPairs: pairs.value, MaxSearchStates: states.value, MaxSolverBytes: solver.value})
		built, err = analysis.FromMeasurement(ctx, archive, settings.MaskingBudget())
		if err == nil {
			built.ToolVersion = buildinfo.Version()
			err = resultfile.WriteContext(ctx, resolveOutputPath(dir, output), resultfile.NewMeasurement(archive, settings, built))
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "gomcdc: %s: %v\n", command, err)
		if ctx.Err() != nil {
			return ExitInterrupted
		}
		return ExitMeasurementFailed
	}
	fmt.Fprintf(stdout, "Saved %s (%d unique executions).\n", output, len(archive.Runs))
	return ExitSuccess
}

func runRequire(ctx context.Context, dir string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("require", flag.ContinueOnError)
	fs.SetOutput(stderr)
	requested := requirements.Requirements{Minimum: map[string]float64{}}
	fs.BoolVar(&requested.Complete, "complete", false, "require complete measurement and analysis")
	for name := range requirements.Metrics(analysis.Summary{}) {
		fs.Func(name, "minimum coverage percentage", func(value string) error {
			n, err := strconv.ParseFloat(value, 64)
			if err == nil {
				requested.Minimum[name] = n
			}
			return err
		})
	}
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: gomcdc require [--complete] [--METRIC PERCENT] ANALYSIS")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitSuccess
		}
		return ExitInvalidUsage
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return ExitInvalidUsage
	}
	if ctx.Err() != nil {
		return ExitInterrupted
	}
	data, err := os.ReadFile(resolveOutputPath(dir, fs.Arg(0)))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return ExitMeasurementFailed
	}
	saved, err := resultfile.Decode(bytes.NewReader(data))
	if err != nil {
		fmt.Fprintf(stderr, "gomcdc: require: read analysis %q: %v\n", fs.Arg(0), err)
		return ExitMeasurementFailed
	}
	var selected config.CoverageSet
	if !requested.Complete {
		selected = make(config.CoverageSet)
		for name := range requested.Minimum {
			selected[config.Metric(name)] = true
		}
		if len(selected) == 0 {
			fmt.Fprintln(stderr, "specify at least one coverage requirement")
			return ExitInvalidUsage
		}
	}
	measured, err := config.ParseCoverage(strings.Join(saved.Measurement.Plan.Settings.Coverage, ","))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return ExitMeasurementFailed
	}
	for name := range requested.Minimum {
		if !measured.Enabled(config.Metric(name)) {
			fmt.Fprintf(stderr, "%s was not measured\n", name)
			return ExitInvalidUsage
		}
	}
	// Evidence and analysis settings are inputs; the saved report is only an
	// output snapshot. --complete needs every recorded metric, other requests
	// need only their selected metrics.
	built, err := analysis.FromMeasurementMetrics(ctx, saved.Measurement, saved.AnalysisSettings.MaskingBudget(), selected)
	if ctx.Err() != nil {
		fmt.Fprintf(stderr, "gomcdc: require: interrupted: %v\n", ctx.Err())
		return ExitInterrupted
	}
	if err != nil {
		fmt.Fprintf(stderr, "gomcdc: require: analyze measurement: %v\n", err)
		return ExitMeasurementFailed
	}
	satisfaction, err := requirements.Evaluate(built, requested)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return ExitInvalidUsage
	}
	exit := ExitSuccess
	for _, s := range satisfaction {
		status := "satisfied"
		if !s.Satisfied {
			status = "unsatisfied"
			exit = ExitCoverageThreshold
		}
		fmt.Fprintf(stdout, "%s: %s (%s)\n", s.Requirement, status, s.Reason)
	}
	return exit
}
