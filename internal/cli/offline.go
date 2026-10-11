package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/shrydev2020/gomcdc/v2/internal/analysis"
	"github.com/shrydev2020/gomcdc/v2/internal/buildinfo"
	"github.com/shrydev2020/gomcdc/v2/internal/mcdc"
	"github.com/shrydev2020/gomcdc/v2/internal/resultfile"
)

func runReanalysis(ctx context.Context, workingDir string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("reanalyze", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var output string
	var pairs, states, solverBytes optionalUint64
	fs.StringVar(&output, "output", "", "destination saved result (required; distinct from every input)")
	fs.Var(&pairs, "mcdc-masking-max-evaluation-pairs", "maximum candidate pairs per condition obligation")
	fs.Var(&states, "mcdc-masking-max-search-states", "maximum search states per condition obligation")
	fs.Var(&solverBytes, "mcdc-masking-max-solver-bytes", "maximum solver backing-array bytes per condition obligation")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: gomcdc reanalyze --output analyzed.gomcdc.json [options] INPUT")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitSuccess
		}
		return ExitInvalidUsage
	}
	if output == "" || output == "-" || fs.NArg() != 1 {
		fmt.Fprintln(stderr, "gomcdc: a destination file and the required saved inputs are needed")
		fs.Usage()
		return ExitInvalidUsage
	}
	for _, limit := range []optionalUint64{pairs, states, solverBytes} {
		if limit.set && limit.value == 0 {
			fmt.Fprintln(stderr, "gomcdc: MC/DC limits must be positive")
			return ExitInvalidUsage
		}
	}
	if err := ctx.Err(); err != nil {
		fmt.Fprintf(stderr, "gomcdc: interrupted: %v\n", err)
		return ExitInterrupted
	}
	inputPath := resolveOutputPath(workingDir, fs.Arg(0))
	if err := checkDistinctOutput(inputPath, options{output: output, format: "json"}, workingDir); err != nil {
		fmt.Fprintln(stderr, err)
		return ExitInvalidUsage
	}
	file, err := os.Open(inputPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return ExitMeasurementFailed
	}
	saved, err := resultfile.Decode(file)
	file.Close()
	if err != nil {
		fmt.Fprintf(stderr, "gomcdc: reanalyze: read analysis %q: %v\n", inputPath, err)
		return ExitMeasurementFailed
	}
	if (pairs.set || states.set || solverBytes.set) && !slices.Contains(saved.Measurement.Plan.Settings.Coverage, "mcdc-masking") {
		fmt.Fprintln(stderr, "gomcdc: Masking limits require mcdc-masking in the saved coverage selection")
		return ExitInvalidUsage
	}
	budget := mcdc.AnalysisBudget{}
	if saved.Report.MaskingAnalysisLimits != nil {
		previous := saved.Report.MaskingAnalysisLimits
		budget = mcdc.AnalysisBudget{MaxEvaluationPairs: previous.MaxEvaluationPairs, MaxSearchStates: previous.MaxSearchStates, MaxSolverBytes: previous.MaxSolverBytes}
	}
	if pairs.set {
		budget.MaxEvaluationPairs = pairs.value
	}
	if states.set {
		budget.MaxSearchStates = states.value
	}
	if solverBytes.set {
		budget.MaxSolverBytes = solverBytes.value
	}
	built, err := analysis.Reanalyze(ctx, saved.Measurement, budget)
	if err != nil {
		fmt.Fprintf(stderr, "gomcdc: reanalyze failed: %v\n", err)
		if ctx.Err() != nil {
			return ExitInterrupted
		}
		return ExitMeasurementFailed
	}
	if err := ctx.Err(); err != nil {
		fmt.Fprintf(stderr, "gomcdc: interrupted: %v\n", err)
		return ExitInterrupted
	}
	built.ToolVersion = buildinfo.Version()
	result := resultfile.NewMeasurement(saved.Measurement, built)
	if err := resultfile.WriteContext(ctx, resolveOutputPath(workingDir, output), result); err != nil {
		fmt.Fprintf(stderr, "gomcdc: save analysis: %v\n", err)
		if ctx.Err() != nil {
			return ExitInterrupted
		}
		return ExitMeasurementFailed
	}
	count := len(result.Measurement.Runs)
	fmt.Fprintf(stdout, "Saved %s (%d unique executions).\n", output, count)
	return ExitSuccess
}
