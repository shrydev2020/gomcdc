// Package cli implements the gomcdc command-line workflow.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shrydev2020/gomcdc/v2/internal/analyzer"
	"github.com/shrydev2020/gomcdc/v2/internal/buildinfo"
	"github.com/shrydev2020/gomcdc/v2/internal/c0"
	"github.com/shrydev2020/gomcdc/v2/internal/c0map"
	"github.com/shrydev2020/gomcdc/v2/internal/config"
	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
	"github.com/shrydev2020/gomcdc/v2/internal/goflags"
	"github.com/shrydev2020/gomcdc/v2/internal/gotest"
	"github.com/shrydev2020/gomcdc/v2/internal/gotestargs"
	"github.com/shrydev2020/gomcdc/v2/internal/instrument"
	"github.com/shrydev2020/gomcdc/v2/internal/loader"
	"github.com/shrydev2020/gomcdc/v2/internal/mcdc"
	"github.com/shrydev2020/gomcdc/v2/internal/modulecontext"
	"github.com/shrydev2020/gomcdc/v2/internal/report"
	"github.com/shrydev2020/gomcdc/v2/internal/runtimecov"
	"github.com/shrydev2020/gomcdc/v2/internal/workspace"
)

const (
	ExitSuccess           = 0
	ExitTestsFailed       = 1
	ExitMeasurementFailed = 2
	ExitCoverageThreshold = 3
	ExitInvalidUsage      = 4
	ExitInterrupted       = 130
)

// classifyExit applies D28 precedence: usage, measurement, test, threshold.
func classifyExit(invalidUsage, measurementFailure, testsFailed, thresholdFailure bool) int {
	switch {
	case invalidUsage:
		return ExitInvalidUsage
	case measurementFailure:
		return ExitMeasurementFailed
	case testsFailed:
		return ExitTestsFailed
	case thresholdFailure:
		return ExitCoverageThreshold
	default:
		return ExitSuccess
	}
}

// Run executes the CLI with the process working directory.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	// Only measurement needs a working directory. Informational and invalid
	// top-level commands must remain usable even when no module is available.
	if len(args) == 0 || args[0] != "test" {
		return runAt(ctx, "", args, stdout, stderr)
	}
	workingDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "gomcdc: determine working directory: %v\n", err)
		return ExitMeasurementFailed
	}
	return runAt(ctx, workingDir, args, stdout, stderr)
}

func runAt(ctx context.Context, workingDir string, args []string, stdout, stderr io.Writer) int {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		writeTopUsage(stdout)
		return ExitSuccess
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, "gomcdc: a subcommand is required")
		writeTopUsage(stderr)
		return ExitInvalidUsage
	}
	if args[0] == "version" {
		if len(args) != 1 {
			fmt.Fprintln(stderr, "gomcdc: version does not accept arguments")
			return ExitInvalidUsage
		}
		fmt.Fprintf(stdout, "gomcdc %s\n", buildinfo.Version())
		return ExitSuccess
	}
	if args[0] != "test" {
		fmt.Fprintf(stderr, "gomcdc: unknown subcommand %q\n", args[0])
		writeTopUsage(stderr)
		return ExitInvalidUsage
	}

	opts, err := parseOptions(args[1:], stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitSuccess
		}
		fmt.Fprintf(stderr, "gomcdc: %v\n", err)
		return ExitInvalidUsage
	}
	if err := ctx.Err(); err != nil {
		fmt.Fprintf(stderr, "gomcdc: interrupted: %v\n", err)
		return ExitInterrupted
	}
	return runCoverage(ctx, workingDir, opts, stdout, stderr)
}

type sourceInstrumentation struct {
	loaded    loader.File
	analysis  analyzer.File
	inventory *c0.FileInventory
}

func runCoverage(ctx context.Context, workingDir string, opts options, stdout, stderr io.Writer) (exitCode int) {
	excludes, err := config.CompileExcludes(opts.excludes)
	if err != nil {
		fmt.Fprintf(stderr, "gomcdc: %v\n", err)
		return ExitInvalidUsage
	}
	if conflict := measurementFlag(opts.goTestArgs); conflict != "" {
		writeMeasurementFlagError(stderr, conflict, "go test arguments")
		return ExitInvalidUsage
	}
	buildFlags := loader.BuildFlags(opts.goTestArgs)
	rawGOFLAGS := os.Getenv("GOFLAGS")
	goFlagWords, goFlagsErr := goflags.Split(rawGOFLAGS)
	if goFlagsErr != nil {
		fmt.Fprintf(stderr, "gomcdc: parse GOFLAGS: %v\n", goFlagsErr)
		return ExitInvalidUsage
	}
	if conflict := measurementGOFLAG(goFlagWords); conflict != "" {
		writeMeasurementFlagError(stderr, conflict, "GOFLAGS")
		return ExitInvalidUsage
	}
	filteredGOFLAGS, goFlagsErr := goflags.WithoutMeasurementFlags(rawGOFLAGS)
	if goFlagsErr != nil {
		fmt.Fprintf(stderr, "gomcdc: parse GOFLAGS: %v\n", goFlagsErr)
		return ExitInvalidUsage
	}
	needsC0 := opts.metrics.Enabled(config.MetricStatement) || opts.metrics.Enabled(config.MetricFunction)
	needsASTRun := needsASTRuntime(opts.metrics)
	needsCompilerSelection := opts.metrics.Enabled(config.MetricSwitchClauseSelection) || opts.metrics.Enabled(config.MetricTypeSwitchClauseSelection)
	measurementName := requestedMeasurementName(needsC0, needsASTRun)

	sourceConfiguration, err := modulecontext.Discover(ctx, modulecontext.DiscoverOptions{
		Dir: workingDir, BuildFlags: buildFlags, GOFLAGS: filteredGOFLAGS,
	})
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintf(stderr, "gomcdc: interrupted: %v\n", ctx.Err())
			return ExitInterrupted
		}
		fmt.Fprintf(stderr, "gomcdc: package load failed: %v\n", err)
		return ExitMeasurementFailed
	}
	work, err := workspace.Create(ctx, workspace.Options{
		SourceConfiguration: sourceConfiguration,
		WorkingDir:          workingDir,
		TempParent:          opts.workDirParent,
		Keep:                opts.keepWorkDir,
	})
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintf(stderr, "gomcdc: interrupted: %v\n", ctx.Err())
			return ExitInterrupted
		}
		fmt.Fprintf(stderr, "gomcdc: temporary workspace creation failed: %v\n", err)
		return ExitMeasurementFailed
	}
	measurementWork := &measurementWorkspace{measurement: measurementName, workspace: work}
	defer func() {
		if finalizationErr := measurementWork.finalize(stderr); finalizationErr != nil && exitCode != ExitInterrupted {
			exitCode = ExitMeasurementFailed
		}
	}()

	packageBuildFlags := append([]string(nil), buildFlags...)
	packageGOFLAGS := filteredGOFLAGS
	if work.ModFilePath != "" {
		packageBuildFlags = withRelocatedBuildModFile(packageBuildFlags, work.ModFilePath)
		packageGOFLAGS, err = goflags.Without(packageGOFLAGS, map[string]bool{"modfile": true})
		if err != nil {
			fmt.Fprintf(stderr, "gomcdc: relocate GOFLAGS modfile: %v\n", err)
			return ExitMeasurementFailed
		}
	}
	goWorkPath := work.GoWorkPath
	if goWorkPath == "" {
		goWorkPath = "off"
	}
	loaded, err := loader.Load(ctx, loader.Options{
		Dir:                work.WorkingDir,
		Patterns:           opts.patterns,
		BuildFlags:         packageBuildFlags,
		IncludeTests:       opts.includeTests,
		GOFLAGS:            &packageGOFLAGS,
		GOWORK:             &goWorkPath,
		ExpectedModuleRoot: work.ModuleDir,
	})
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintf(stderr, "gomcdc: interrupted: %v\n", ctx.Err())
			return ExitInterrupted
		}
		fmt.Fprintf(stderr, "gomcdc: package load failed: %v\n", err)
		return ExitMeasurementFailed
	}
	var sources []sourceInstrumentation
	var generatedFiles []c0map.GeneratedFile
	var ignoredCoverageFiles []string
	var reportErrors []report.ReportError
	analysisIncomplete := false
	analysisUnknown := 0
	for _, file := range loaded.Files {
		if err := ctx.Err(); err != nil {
			fmt.Fprintf(stderr, "gomcdc: source analysis interrupted: %v\n", err)
			return ExitInterrupted
		}
		relative, relErr := filepath.Rel(loaded.ModuleRoot, file.Path)
		if relErr != nil {
			fmt.Fprintf(stderr, "gomcdc: resolve source path %q: %v\n", file.Path, relErr)
			return ExitMeasurementFailed
		}
		if excludes.Match(relative) {
			if needsC0 && !strings.HasSuffix(relative, "_test.go") {
				ignoredCoverageFiles = append(ignoredCoverageFiles, filepath.ToSlash(relative))
			}
			continue
		}
		analysis, analysisErr := analyzer.AnalyzeFile(analyzer.FileOptions{
			Path:         file.Path,
			OriginalPath: filepath.Join(sourceConfiguration.MainModuleDir(), relative),
			ModuleDir:    loaded.ModuleRoot,
			ModulePath:   loaded.ModulePath,
			PackagePath:  file.PackagePath,
		})
		if err := ctx.Err(); err != nil {
			fmt.Fprintf(stderr, "gomcdc: source analysis interrupted: %v\n", err)
			return ExitInterrupted
		}
		if analysisErr != nil {
			// Invalid source in one package is ultimately a go test build failure.
			// Keep other packages reportable instead of failing before their tests
			// can run and emit partial evidence.
			fmt.Fprintf(stderr, "gomcdc: source analysis skipped %q: %v\n", relative, analysisErr)
			analysisIncomplete = true
			analysisUnknown++
			reportErrors = append(reportErrors, report.ReportError{
				Phase: "analysis", Code: "source-analysis-failed", Message: "source analysis did not complete",
				Package: file.PackagePath, Path: filepath.ToSlash(relative),
			})
			continue
		}
		if analysis.Generated {
			generatedFiles = append(generatedFiles, c0map.GeneratedFile{Path: filepath.ToSlash(relative)})
			continue
		}
		var inventory *c0.FileInventory
		if needsC0 && !strings.HasSuffix(analysis.RelativePath, "_test.go") {
			builtInventory, inventoryErr := c0.BuildInventory(analysis.RelativePath, analysis.Source)
			if inventoryErr != nil {
				fmt.Fprintf(stderr, "gomcdc: C0 inventory failed %q: %v\n", relative, inventoryErr)
				return ExitMeasurementFailed
			}
			inventory = &builtInventory
		}
		sources = append(sources, sourceInstrumentation{loaded: file, analysis: analysis, inventory: inventory})
	}
	analyses := make([]analyzer.File, 0, len(sources))
	for _, source := range sources {
		analyses = append(analyses, source.analysis)
	}
	if err := analyzer.DetectCollisions(analyses); err != nil {
		fmt.Fprintf(stderr, "gomcdc: source analysis failed: %v\n", err)
		return ExitMeasurementFailed
	}
	if err := ctx.Err(); err != nil {
		fmt.Fprintf(stderr, "gomcdc: interrupted before measurement: %v\n", err)
		return ExitInterrupted
	}

	var decisions []cover.DecisionMetadata
	var clauses []cover.ClauseMetadata
	var noMatches []cover.NoMatchMetadata
	for _, item := range sources {
		for _, decision := range item.analysis.Decisions {
			decisions = append(decisions, decision.Metadata)
		}
		for _, clause := range item.analysis.Clauses {
			clauses = append(clauses, clause.Metadata)
		}
		noMatches = append(noMatches, item.analysis.NoMatches...)
	}

	measurement, measurementErr := measure(measurementRequest{
		context: ctx, timeout: opts.timeout, goTestArgs: opts.goTestArgs, goFlags: filteredGOFLAGS,
		loaded: loaded, sources: sources, generated: generatedFiles, ignoredCoverageFiles: ignoredCoverageFiles,
		decisions: decisions, clauses: clauses, noMatches: noMatches,
		needsC0: needsC0, needsAST: needsASTRun, compilerClauseSelection: needsCompilerSelection,
	}, work, stderr)
	if measurementErr != nil {
		if ctx.Err() != nil {
			fmt.Fprintf(stderr, "gomcdc: interrupted: %v\n", ctx.Err())
			return ExitInterrupted
		}
		fmt.Fprintf(stderr, "gomcdc: %v\n", measurementErr)
		return ExitMeasurementFailed
	}
	finalizationErr := measurementWork.finalize(stderr)
	if finalizationErr != nil {
		reportErrors = append(reportErrors, report.ReportError{
			Phase: "cleanup", Code: "workspace-cleanup-failed", Message: "temporary workspace cleanup failed",
		})
	}

	input := assembleReportInput(reportAssembly{
		context:     ctx,
		toolVersion: buildinfo.Version(),
		loaded:      loaded, sources: sources, coverage: opts.metrics, decisions: decisions,
		maskingAnalysisBudget: opts.maskingAnalysisBudget(),
		clauses:               clauses, noMatches: noMatches,
		evidence: measurement.evidence, c0: measurement.c0, testResult: measurement.testResult, measurementName: measurement.measurementName,
		standardCoverRequested: needsC0, astRequested: needsASTRun,
		producerOutcomes:       measurement.producerOutcomes,
		instrumentationUnknown: analysisUnknown,
		results:                measurement.results(analysisIncomplete, finalizationErr),
		interrupted:            measurement.interrupted,
		errors:                 reportErrors, measurementDiagnostics: measurement.diagnostics,
	})
	built := report.Build(input)
	strictFailure := opts.strict && (built.Instrumentation.HasGaps() || summaryUnknown(built.Summary) > 0 || summaryAnalysisIncomplete(built.Summary) > 0)
	thresholds := thresholdFailures(opts, built.Summary)
	input.Results.Strict = requestedResult(opts.strict, !strictFailure)
	input.Results.Threshold = requestedResult(thresholdRequested(opts), len(thresholds) == 0)
	if strictFailure {
		input.Errors = append(input.Errors, report.ReportError{
			Phase: "validation", Code: "strict-coverage-gap", Message: "requested coverage contains unsupported, unknown, analysis-incomplete, or uninstrumented entities",
		})
	}
	for _, threshold := range thresholds {
		input.Errors = append(input.Errors, report.ReportError{
			Phase: "threshold", Code: "coverage-threshold-failed", Message: threshold,
		})
	}
	built = report.WithRunResultsAndErrors(built, input.Results, input.Errors)
	if opts.format == "html" {
		built = report.WithSourceViews(built, input.SourceFiles)
	}
	if err := writeReport(opts, built, workingDir, stdout); err != nil {
		fmt.Fprintf(stderr, "gomcdc: report generation failed: %v\n", err)
		return ExitMeasurementFailed
	}
	if measurement.interrupted {
		return ExitInterrupted
	}
	if measurement.integrityFailure {
		return classifyExit(false, true, false, false)
	}
	if finalizationErr != nil {
		return classifyExit(false, true, false, false)
	}
	if strictFailure {
		coverage := built.Instrumentation.Total
		fmt.Fprintf(
			stderr,
			"gomcdc: strict coverage failed: discovered=%d supported=%d instrumented=%d unsupported=%d unknown=%d evidence-unknown=%d analysis-incomplete=%d\n",
			coverage.Discovered,
			coverage.Supported,
			coverage.Instrumented,
			coverage.Unsupported,
			coverage.Unknown,
			summaryUnknown(built.Summary),
			summaryAnalysisIncomplete(built.Summary),
		)
		return classifyExit(false, true, false, false)
	}
	if analysisIncomplete {
		return classifyExit(false, true, false, false)
	}
	if testRunFailed(measurement.testResult) {
		fmt.Fprintf(stderr, "gomcdc: %s measurement: %v\n", measurement.measurementName, measurement.testResult.Err)
		return classifyExit(false, false, true, false)
	}
	if len(thresholds) > 0 {
		for _, failure := range thresholds {
			fmt.Fprintln(stderr, failure)
		}
		return classifyExit(false, false, false, true)
	}
	return classifyExit(false, false, false, false)
}

func requestedResult(requested, passed bool) report.ResultStatus {
	if !requested {
		return report.ResultNotRequested
	}
	return passFailResult(passed)
}

func thresholdRequested(opts options) bool {
	return opts.failUnderStatement.set ||
		opts.failUnderFunction.set ||
		opts.failUnderDecision.set ||
		opts.failUnderSwitchClauseBody.set ||
		opts.failUnderTypeSwitchClauseBody.set ||
		opts.failUnderSelectClauseBody.set ||
		opts.failUnderSwitchClauseSelection.set ||
		opts.failUnderTypeSwitchClauseSelection.set ||
		opts.failUnderCondition.set ||
		opts.failUnderMCDCUnique.set ||
		opts.failUnderMCDCMasking.set
}

func summaryAnalysisIncomplete(summary report.Summary) int {
	return sumEnabledSummaryField(summary, func(metric report.MetricSummary) int { return metric.AnalysisIncomplete })
}

func summaryUnknown(summary report.Summary) int {
	return sumEnabledSummaryField(summary, func(metric report.MetricSummary) int { return metric.Unknown })
}

func sumEnabledSummaryField(summary report.Summary, value func(report.MetricSummary) int) int {
	metrics := []report.MetricSummary{
		summary.Statement, summary.Function, summary.Decision,
		summary.SwitchClauseBody, summary.TypeSwitchClauseBody, summary.SelectClauseBody,
		summary.SwitchClauseSelection, summary.TypeSwitchClauseSelection,
		summary.Condition, summary.MCDCUnique, summary.MCDCMasking,
	}
	total := 0
	for _, metric := range metrics {
		if metric.Enabled {
			total += value(metric)
		}
	}
	return total
}

func sourceFileInputs(sources []sourceInstrumentation) []report.SourceFileInput {
	result := make([]report.SourceFileInput, 0, len(sources))
	for _, source := range sources {
		result = append(result, report.SourceFileInput{
			PackagePath: source.loaded.PackagePath,
			Path:        source.analysis.RelativePath,
			Source:      append([]byte(nil), source.analysis.Source...),
		})
	}
	return result
}

type packageInstrumentation struct {
	directory   string
	packageName string
	packagePath string
	testOnly    bool
	files       []instrument.FileMapping
}

func instrumentPackages(
	ctx context.Context,
	moduleDir string,
	sources []sourceInstrumentation,
	runtimeImportPath string,
	compilerClauseSelection bool,
	planCoverageCorrespondence bool,
) ([]instrument.PackageResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	groups := make(map[string]*packageInstrumentation)
	for _, item := range sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(item.analysis.Decisions) == 0 && len(item.analysis.Clauses) == 0 {
			continue
		}
		copyPath := filepath.Join(moduleDir, filepath.FromSlash(item.analysis.RelativePath))
		directory := filepath.Dir(copyPath)
		key := directory + "\x00" + item.analysis.PackageName
		group := groups[key]
		if group == nil {
			group = &packageInstrumentation{
				directory:   directory,
				packageName: item.analysis.PackageName,
				packagePath: item.loaded.PackagePath,
				testOnly:    true,
			}
			groups[key] = group
		}
		if !strings.HasSuffix(item.analysis.RelativePath, "_test.go") {
			group.testOnly = false
		}
		group.files = append(group.files, instrument.FileMapping{
			CopyPath: copyPath, Analysis: item.analysis, OriginalInventory: item.inventory,
			ExcludeFromCoveragePlan: item.inventory == nil,
		})
	}

	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	results := make([]instrument.PackageResult, 0, len(keys))
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		group := groups[key]
		activeFiles, err := goFilesInDirectory(ctx, group.directory)
		if err != nil {
			return nil, err
		}
		result, err := instrument.InstrumentPackage(instrument.PackageOptions{
			Context:                    ctx,
			Directory:                  group.directory,
			PackageName:                group.packageName,
			PackagePath:                group.packagePath,
			RuntimeImportPath:          runtimeImportPath,
			CompilerClauseSelection:    compilerClauseSelection,
			PlanCoverageCorrespondence: planCoverageCorrespondence,
			TestOnly:                   group.testOnly,
			ActiveFiles:                activeFiles,
			Files:                      group.files,
		})
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func goFilesInDirectory(ctx context.Context, directory string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read copied package directory %q: %w", directory, err)
	}
	var files []string
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		files = append(files, filepath.Join(directory, entry.Name()))
	}
	sort.Strings(files)
	return files, nil
}

func needsASTRuntime(metrics config.CoverageSet) bool {
	return metrics.Enabled(config.MetricDecision) ||
		metrics.Enabled(config.MetricSwitchClauseBody) ||
		metrics.Enabled(config.MetricTypeSwitchClauseBody) ||
		metrics.Enabled(config.MetricSelectClauseBody) ||
		metrics.Enabled(config.MetricSwitchClauseSelection) ||
		metrics.Enabled(config.MetricTypeSwitchClauseSelection) ||
		metrics.Enabled(config.MetricCondition) ||
		metrics.Enabled(config.MetricMCDCUnique) ||
		metrics.Enabled(config.MetricMCDCMasking)
}

func runGoTest(ctx context.Context, timeout time.Duration, options gotest.Options) gotest.Result {
	testContext := ctx
	cancel := func() {}
	if timeout > 0 {
		testContext, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	return gotest.Run(testContext, options)
}

func measurementRuns(name string, result *gotest.Result) []report.MeasurementRun {
	if result == nil {
		return nil
	}
	packages := make(map[string]string, len(result.Packages))
	for packagePath, status := range result.Packages {
		packages[packagePath] = string(status)
	}
	return []report.MeasurementRun{{
		Name: name,
		Run: report.TestRun{
			Status:      result.Status,
			FailureKind: result.FailureKind,
			Complete:    result.Status == cover.RunPassed,
		},
		Packages: packages,
	}}
}

func testRunFailed(result *gotest.Result) bool {
	return result != nil && result.Err != nil
}

var measurementFlags = map[string]struct{}{
	"count": {}, "cover": {}, "coverprofile": {}, "covermode": {},
	"coverpkg": {}, "json": {}, "overlay": {}, "toolexec": {},
}

func measurementFlag(arguments gotestargs.Arguments) string {
	for _, flag := range arguments.Flags() {
		name := flag.CanonicalName()
		if _, forbidden := measurementFlags[name]; forbidden {
			return name
		}
	}
	return ""
}

func measurementGOFLAG(words []string) string {
	for _, word := range words {
		name := goflags.Name(word)
		switch name {
		case "test.count":
			name = "count"
		case "test.coverprofile":
			name = "coverprofile"
		}
		if _, forbidden := measurementFlags[name]; forbidden {
			return name
		}
	}
	return ""
}

func writeMeasurementFlagError(stderr io.Writer, name, source string) {
	if name == "overlay" {
		fmt.Fprintf(stderr, "gomcdc: go test -overlay from %s is unsupported because it prevents reliable original-source mapping\n", source)
		return
	}
	fmt.Fprintf(stderr, "gomcdc: go test -%s from %s conflicts with gomcdc measurement ownership\n", name, source)
}

func collectC0(
	ctx context.Context,
	profilePath string,
	loaded loader.Result,
	sources []sourceInstrumentation,
	generated []c0map.GeneratedFile,
) (_ *c0.Report, err error) {
	profileFile, err := os.Open(profilePath)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, profileFile.Close()) }()

	profile, err := c0.ParseProfile(profileFile)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", profilePath, err)
	}
	return buildC0Report(ctx, profile, loaded, sources, generated)
}

func buildC0Report(
	ctx context.Context,
	profile c0.Profile,
	loaded loader.Result,
	sources []sourceInstrumentation,
	generated []c0map.GeneratedFile,
) (*c0.Report, error) {
	mappedSources := make([]c0map.Source, 0, len(sources))
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		mappedSources = append(mappedSources, c0map.Source{
			PackagePath:    source.loaded.PackagePath,
			RelativePath:   source.analysis.RelativePath,
			OriginalSource: append([]byte(nil), source.analysis.Source...),
		})
	}
	sourceMap, err := c0map.Build(ctx, profile, loaded.ModulePath, mappedSources, generated)
	if err != nil {
		return nil, err
	}
	analyzed, err := c0.Analyze(ctx, profile, sourceMap, c0.Options{})
	if err != nil {
		return nil, err
	}
	return &analyzed, nil
}

// acceptedRuntimeEvidence is the only runtime evidence allowed to reach report
// construction. Transport provenance has been accepted against the requested
// run and source inventory, while ClauseObservations are the provenance-free,
// idempotent domain projection used for coverage aggregation.
type acceptedRuntimeEvidence struct {
	Evaluations           []cover.DecisionEvaluation
	NotEvaluatedDecisions []cover.DecisionNotEvaluatedObservation
	ClauseObservations    []cover.ClauseObservation
	Diagnostics           []runtimecov.Diagnostic
	Files                 []runtimecov.ProcessFile
}

// runtimeAcceptanceIssues preserves producer ownership for rejected runtime
// records. Shared transport failures invalidate both runtime producers, while
// AST and compiler mapping failures invalidate only the projection that owns
// the rejected record.
type runtimeAcceptanceIssues struct {
	shared   []error
	ast      []error
	compiler []error
}

func (issues *runtimeAcceptanceIssues) addShared(err error) {
	if err != nil {
		issues.shared = append(issues.shared, err)
	}
}

func (issues *runtimeAcceptanceIssues) addAST(err error) {
	if err != nil {
		issues.ast = append(issues.ast, err)
	}
}

func (issues *runtimeAcceptanceIssues) addCompiler(err error) {
	if err != nil {
		issues.compiler = append(issues.compiler, err)
	}
}

func (issues runtimeAcceptanceIssues) err() error {
	return errors.Join(errors.Join(issues.shared...), errors.Join(issues.ast...), errors.Join(issues.compiler...))
}

func (issues runtimeAcceptanceIssues) astErr() error {
	return errors.Join(errors.Join(issues.shared...), errors.Join(issues.ast...))
}

func (issues runtimeAcceptanceIssues) compilerErr() error {
	return errors.Join(errors.Join(issues.shared...), errors.Join(issues.compiler...))
}

func (issues *runtimeAcceptanceIssues) addClause(event cover.ClauseEventKind, err error) {
	switch event {
	case cover.ClauseBodyExecution:
		issues.addAST(err)
	case cover.ClauseDirectSelection, cover.ClauseNoMatchSelection:
		issues.addCompiler(err)
	default:
		issues.addShared(err)
	}
}

func acceptRuntimeEvidence(
	ctx context.Context,
	decisions []cover.DecisionMetadata,
	clauses []cover.ClauseMetadata,
	recorded runtimecov.RecordedEvidence,
	runID string,
	noMatches []cover.NoMatchMetadata,
) (acceptedRuntimeEvidence, error) {
	accepted, issues := acceptRuntimeEvidenceByProducer(ctx, decisions, clauses, recorded, runID, noMatches)
	return accepted, issues.err()
}

func acceptRuntimeEvidenceByProducer(
	ctx context.Context,
	decisions []cover.DecisionMetadata,
	clauses []cover.ClauseMetadata,
	recorded runtimecov.RecordedEvidence,
	runID string,
	noMatches []cover.NoMatchMetadata,
) (acceptedRuntimeEvidence, runtimeAcceptanceIssues) {
	var issues runtimeAcceptanceIssues
	if err := ctx.Err(); err != nil {
		issues.addShared(err)
		return acceptedRuntimeEvidence{}, issues
	}
	knownDecisions := make(map[cover.DecisionID]cover.DecisionMetadata, len(decisions))
	knownPackages := make(map[string]struct{})
	for _, decision := range decisions {
		if err := ctx.Err(); err != nil {
			issues.addShared(err)
			return acceptedRuntimeEvidence{}, issues
		}
		knownDecisions[decision.ID] = decision
		knownPackages[decision.Package] = struct{}{}
	}
	knownClauses := make(map[cover.ClauseID]cover.ClauseMetadata, len(clauses))
	for _, clause := range clauses {
		if err := ctx.Err(); err != nil {
			issues.addShared(err)
			return acceptedRuntimeEvidence{}, issues
		}
		knownClauses[clause.ID] = clause
		knownPackages[clause.Package] = struct{}{}
	}
	knownNoMatches := make(map[cover.SwitchID]cover.NoMatchMetadata)
	for _, noMatch := range noMatches {
		if err := ctx.Err(); err != nil {
			issues.addShared(err)
			return acceptedRuntimeEvidence{}, issues
		}
		knownNoMatches[noMatch.SwitchID] = noMatch
		knownPackages[noMatch.Package] = struct{}{}
	}
	switchDecisions, orderErr := conditionlessSwitchDecisionOrder(ctx, clauses)
	if orderErr != nil {
		issues.addShared(orderErr)
		return acceptedRuntimeEvidence{}, issues
	}
	accepted := acceptedRuntimeEvidence{
		Diagnostics: append([]runtimecov.Diagnostic(nil), recorded.Diagnostics...),
		Files:       append([]runtimecov.ProcessFile(nil), recorded.Files...),
	}
	for _, file := range recorded.Files {
		if err := ctx.Err(); err != nil {
			issues.addShared(err)
			return accepted, issues
		}
		if file.RunID == "" && file.PackagePath == "" && file.ProcessID == 0 {
			continue
		}
		_, knownPackage := knownPackages[file.PackagePath]
		if file.RunID != runID || !knownPackage || file.ProcessID <= 0 {
			issues.addShared(fmt.Errorf(
				"process event file %q has invalid provenance run=%q package=%q process=%d",
				filepath.Base(file.Path), file.RunID, file.PackagePath, file.ProcessID,
			))
		}
	}
	validEvaluations := make(map[cover.EvaluationIdentity]cover.DecisionEvaluation)
	validEvaluationCandidates := make([]cover.DecisionEvaluation, 0, len(recorded.Evaluations))
	type skipCauseKey struct {
		Identity   cover.EvaluationIdentity
		DecisionID cover.DecisionID
	}
	observedSkips := make(map[skipCauseKey]map[cover.DecisionID]struct{})
	candidateSkips := make(map[skipCauseKey][]cover.DecisionNotEvaluatedObservation)
	for _, evaluation := range recorded.Evaluations {
		if err := ctx.Err(); err != nil {
			issues.addShared(err)
			return accepted, issues
		}
		metadata, exists := knownDecisions[evaluation.DecisionID]
		if !exists {
			issues.addAST(fmt.Errorf("event contains unknown decision ID 0x%016x", uint64(evaluation.DecisionID)))
			continue
		}
		if evaluation.EvaluationID == 0 {
			issues.addAST(fmt.Errorf("decision 0x%016x has reserved evaluation ID zero", uint64(evaluation.DecisionID)))
			continue
		}
		if evaluation.ProcessID <= 0 {
			issues.addAST(fmt.Errorf("decision 0x%016x has invalid process provenance %d", uint64(evaluation.DecisionID), evaluation.ProcessID))
			continue
		}
		if evaluation.RunID != runID {
			issues.addAST(fmt.Errorf("decision 0x%016x belongs to unexpected run %q", uint64(evaluation.DecisionID), evaluation.RunID))
			continue
		}
		if evaluation.PackagePath != metadata.Package {
			issues.addAST(fmt.Errorf(
				"decision 0x%016x belongs to package %q, want %q",
				uint64(evaluation.DecisionID), evaluation.PackagePath, metadata.Package,
			))
			continue
		}
		if len(evaluation.Conditions) != len(metadata.Conditions) {
			issues.addAST(fmt.Errorf(
				"decision 0x%016x has %d condition states, want %d",
				uint64(evaluation.DecisionID), len(evaluation.Conditions), len(metadata.Conditions),
			))
			continue
		}
		if semanticErr := mcdc.ValidateCompletedEvaluation(metadata, evaluation); semanticErr != nil {
			issues.addAST(fmt.Errorf(
				"decision 0x%016x contains impossible completed evidence: %w",
				uint64(evaluation.DecisionID), semanticErr,
			))
			continue
		}
		validEvaluationCandidates = append(validEvaluationCandidates, evaluation)
		validEvaluations[evaluation.Identity()] = evaluation
	}
	for _, observation := range recorded.NotEvaluatedDecisions {
		if err := ctx.Err(); err != nil {
			issues.addShared(err)
			return accepted, issues
		}
		target, targetExists := knownDecisions[observation.DecisionID]
		cause, causeExists := knownDecisions[observation.CauseDecisionID]
		identity := cover.EvaluationIdentity{
			RunID:        observation.RunID,
			PackagePath:  observation.PackagePath,
			ProcessID:    observation.ProcessID,
			EvaluationID: observation.CauseEvaluationID,
		}
		causeEvaluation, evidenceExists := validEvaluations[identity]
		switch {
		case !targetExists:
			issues.addAST(fmt.Errorf("event contains unknown skipped decision ID 0x%016x", uint64(observation.DecisionID)))
			continue
		case !causeExists:
			issues.addAST(fmt.Errorf("event contains unknown skip-cause decision ID 0x%016x", uint64(observation.CauseDecisionID)))
			continue
		case observation.RunID != runID || observation.PackagePath != target.Package || cause.Package != target.Package || observation.ProcessID <= 0:
			issues.addAST(fmt.Errorf("skipped decision 0x%016x has inconsistent run or package provenance", uint64(observation.DecisionID)))
			continue
		case target.Kind != cover.DecisionSwitchCase || cause.Kind != cover.DecisionSwitchCase:
			issues.addAST(fmt.Errorf("skipped decision 0x%016x is not a conditionless-switch decision", uint64(observation.DecisionID)))
			continue
		case !evidenceExists || causeEvaluation.DecisionID != observation.CauseDecisionID || causeEvaluation.Status != cover.EvaluationCompleted || !causeEvaluation.Result:
			issues.addAST(fmt.Errorf("skipped decision 0x%016x has no completed true cause evaluation", uint64(observation.DecisionID)))
			continue
		}
		causeOrder, causeOrdered := switchDecisions[observation.CauseDecisionID]
		targetOrder, targetOrdered := switchDecisions[observation.DecisionID]
		if !causeOrdered || !targetOrdered || causeOrder.groupID != targetOrder.groupID || targetOrder.position <= causeOrder.position {
			issues.addAST(fmt.Errorf("skipped decision 0x%016x is not later in the same conditionless switch", uint64(observation.DecisionID)))
			continue
		}
		key := skipCauseKey{Identity: identity, DecisionID: observation.CauseDecisionID}
		if observedSkips[key] == nil {
			observedSkips[key] = make(map[cover.DecisionID]struct{})
		}
		observedSkips[key][observation.DecisionID] = struct{}{}
		candidateSkips[key] = append(candidateSkips[key], observation)
	}
	for _, evaluation := range validEvaluationCandidates {
		if err := ctx.Err(); err != nil {
			issues.addShared(err)
			return accepted, issues
		}
		identity := evaluation.Identity()
		order, exists := switchDecisions[evaluation.DecisionID]
		if !exists || evaluation.Status != cover.EvaluationCompleted || !evaluation.Result || len(order.suffix) == 0 {
			continue
		}
		observed := observedSkips[skipCauseKey{Identity: identity, DecisionID: evaluation.DecisionID}]
		key := skipCauseKey{Identity: identity, DecisionID: evaluation.DecisionID}
		if len(candidateSkips[key]) != len(observed) {
			issues.addAST(fmt.Errorf("conditionless-switch decision 0x%016x has duplicate skipped-decision evidence", uint64(evaluation.DecisionID)))
			continue
		}
		if len(observed) != len(order.suffix) {
			issues.addAST(fmt.Errorf(
				"conditionless-switch decision 0x%016x skipped %d decisions, want complete suffix of %d",
				uint64(evaluation.DecisionID), len(observed), len(order.suffix),
			))
			continue
		}
		complete := true
		for _, expected := range order.suffix {
			if err := ctx.Err(); err != nil {
				issues.addShared(err)
				return accepted, issues
			}
			if _, found := observed[expected]; !found {
				complete = false
				issues.addAST(fmt.Errorf(
					"conditionless-switch decision 0x%016x omitted skipped decision 0x%016x",
					uint64(evaluation.DecisionID), uint64(expected),
				))
			}
		}
		if complete {
			accepted.NotEvaluatedDecisions = append(accepted.NotEvaluatedDecisions, candidateSkips[key]...)
		}
	}
	var deduplicateErr error
	accepted.Evaluations, deduplicateErr = deduplicateAcceptedEvaluations(ctx, validEvaluationCandidates)
	if deduplicateErr != nil {
		issues.addAST(deduplicateErr)
		return accepted, issues
	}
	verifiedClauses := make(map[cover.ClauseObservation]struct{})
	for _, event := range recorded.ClauseEvents {
		if err := ctx.Err(); err != nil {
			issues.addShared(err)
			return accepted, issues
		}
		observation := cover.ClauseObservation{
			SwitchID:         event.SwitchID,
			ClauseID:         event.ClauseID,
			Event:            event.Event,
			AlternativeIndex: event.AlternativeIndex,
			AlternativeKnown: event.AlternativeKnown,
		}
		if event.RunID != runID || event.PackagePath == "" || event.ProcessID <= 0 {
			issues.addClause(observation.Event, fmt.Errorf(
				"clause event 0x%016x has invalid provenance run=%q package=%q process=%d",
				uint64(observation.ClauseID), event.RunID, event.PackagePath, event.ProcessID,
			))
			continue
		}
		if observation.Event == cover.ClauseNoMatchSelection {
			metadata, exists := knownNoMatches[observation.SwitchID]
			if !exists || observation.AlternativeKnown {
				issues.addCompiler(fmt.Errorf("event contains unknown no-match switch ID 0x%016x", uint64(observation.SwitchID)))
				continue
			}
			if event.PackagePath != metadata.Package {
				issues.addCompiler(fmt.Errorf(
					"no-match switch 0x%016x belongs to package %q, want %q",
					uint64(observation.SwitchID), event.PackagePath, metadata.Package,
				))
				continue
			}
			if _, duplicate := verifiedClauses[observation]; !duplicate {
				verifiedClauses[observation] = struct{}{}
				accepted.ClauseObservations = append(accepted.ClauseObservations, observation)
			}
			continue
		}
		metadata, exists := knownClauses[observation.ClauseID]
		if !exists {
			issues.addClause(observation.Event, fmt.Errorf("event contains unknown clause ID 0x%016x", uint64(observation.ClauseID)))
			continue
		}
		if event.PackagePath != metadata.Package {
			issues.addClause(observation.Event, fmt.Errorf(
				"clause 0x%016x belongs to package %q, want %q",
				uint64(observation.ClauseID), event.PackagePath, metadata.Package,
			))
			continue
		}
		if observation.SwitchID != 0 && observation.SwitchID != metadata.SwitchID {
			issues.addClause(observation.Event, fmt.Errorf("clause 0x%016x has inconsistent switch ID", uint64(observation.ClauseID)))
			continue
		}
		switch observation.Event {
		case cover.ClauseBodyExecution:
			if observation.AlternativeKnown {
				issues.addAST(fmt.Errorf("clause 0x%016x body event carries a case alternative", uint64(observation.ClauseID)))
				continue
			}
		case cover.ClauseDirectSelection:
			if metadata.Kind != cover.ClauseExpressionSwitch && metadata.Kind != cover.ClauseTypeSwitch {
				issues.addCompiler(fmt.Errorf("clause 0x%016x kind %q cannot carry direct-selection evidence", uint64(observation.ClauseID), metadata.Kind))
				continue
			}
			if observation.SwitchID == 0 || observation.SwitchID != metadata.SwitchID {
				issues.addCompiler(fmt.Errorf("clause 0x%016x direct selection has inconsistent switch ID", uint64(observation.ClauseID)))
				continue
			}
			alternatives := len(metadata.Expressions)
			if metadata.Kind == cover.ClauseTypeSwitch {
				alternatives = len(metadata.Types)
			}
			if metadata.Role == cover.ClauseDefault {
				if observation.AlternativeKnown {
					issues.addCompiler(fmt.Errorf("default clause 0x%016x carries a case alternative", uint64(observation.ClauseID)))
					continue
				}
			} else if !observation.AlternativeKnown || int(observation.AlternativeIndex) >= alternatives {
				issues.addCompiler(fmt.Errorf("clause 0x%016x carries an invalid case alternative", uint64(observation.ClauseID)))
				continue
			}
		default:
			issues.addShared(fmt.Errorf("clause 0x%016x has unsupported event %q", uint64(observation.ClauseID), observation.Event))
			continue
		}
		if _, duplicate := verifiedClauses[observation]; !duplicate {
			verifiedClauses[observation] = struct{}{}
			accepted.ClauseObservations = append(accepted.ClauseObservations, observation)
		}
	}
	return accepted, issues
}

type acceptedEvaluationKey struct {
	RunID       string
	PackagePath string
	DecisionID  cover.DecisionID
	Conditions  string
	Result      bool
	Status      cover.EvaluationStatus
}

func deduplicateAcceptedEvaluations(ctx context.Context, evaluations []cover.DecisionEvaluation) ([]cover.DecisionEvaluation, error) {
	result := make([]cover.DecisionEvaluation, 0, len(evaluations))
	seen := make(map[acceptedEvaluationKey]int, len(evaluations))
	for _, evaluation := range evaluations {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		encodedConditions := make([]byte, len(evaluation.Conditions))
		for index, condition := range evaluation.Conditions {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			encodedConditions[index] = byte(condition)
		}
		key := acceptedEvaluationKey{
			RunID: evaluation.RunID, PackagePath: evaluation.PackagePath,
			DecisionID: evaluation.DecisionID, Conditions: string(encodedConditions),
			Result: evaluation.Result, Status: evaluation.Status,
		}
		if index, duplicate := seen[key]; duplicate {
			if result[index].TestID == cover.UnknownTestID && evaluation.TestID != cover.UnknownTestID {
				result[index].TestID = evaluation.TestID
			}
			continue
		}
		seen[key] = len(result)
		result = append(result, evaluation)
	}
	return result, nil
}

type switchDecisionOrder struct {
	groupID  cover.ClauseGroupID
	position int
	suffix   []cover.DecisionID
}

func conditionlessSwitchDecisionOrder(ctx context.Context, clauses []cover.ClauseMetadata) (map[cover.DecisionID]switchDecisionOrder, error) {
	groups := make(map[cover.ClauseGroupID][]cover.ClauseMetadata)
	for _, clause := range clauses {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if clause.Kind == cover.ClauseConditionlessSwitch && clause.GroupID != 0 {
			groups[clause.GroupID] = append(groups[clause.GroupID], clause)
		}
	}
	result := make(map[cover.DecisionID]switchDecisionOrder)
	for groupID, members := range groups {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sort.Slice(members, func(i, j int) bool { return members[i].Index < members[j].Index })
		var sequence []cover.DecisionID
		for _, clause := range members {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			sequence = append(sequence, clause.DecisionIDs...)
		}
		for position, decisionID := range sequence {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			result[decisionID] = switchDecisionOrder{
				groupID:  groupID,
				position: position,
				suffix:   append([]cover.DecisionID(nil), sequence[position+1:]...),
			}
		}
	}
	return result, nil
}

func formatRuntimeDiagnostic(diagnostic runtimecov.Diagnostic) string {
	location := filepath.Base(diagnostic.File)
	if diagnostic.Line > 0 {
		location += fmt.Sprintf(":%d", diagnostic.Line)
	}
	severity := diagnostic.Severity
	if severity == "" {
		severity = runtimecov.DiagnosticIntegrity
	}
	if location == "." || location == "" {
		return fmt.Sprintf("%s: %s", severity, diagnostic.Message)
	}
	return fmt.Sprintf("%s: %s: %s", severity, location, diagnostic.Message)
}

func runtimeDiagnosticReportMessage(severity runtimecov.DiagnosticSeverity) string {
	switch severity {
	case runtimecov.DiagnosticRecoverable:
		return "runtime coverage event stream was interrupted"
	case runtimecov.DiagnosticIntegrity:
		return "runtime coverage event stream failed integrity validation"
	default:
		return "runtime coverage event stream reported a diagnostic"
	}
}

func runtimeDiagnosticsInvalidate(ctx context.Context, diagnostics []runtimecov.Diagnostic, testResult *gotest.Result) (bool, error) {
	if testResult != nil && len(testResult.RuntimeDiagnostics) > 0 {
		return true, nil
	}
	for _, diagnostic := range diagnostics {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if diagnostic.Severity != runtimecov.DiagnosticRecoverable {
			return true, nil
		}
	}
	return len(diagnostics) > 0 && (testResult == nil || testResult.Status == cover.RunPassed), nil
}

func thresholdFailures(opts options, summary report.Summary) []string {
	checks := []struct {
		name      string
		threshold optionalFloat
		metric    report.MetricSummary
	}{
		{"statement", opts.failUnderStatement, summary.Statement},
		{"function", opts.failUnderFunction, summary.Function},
		{"decision", opts.failUnderDecision, summary.Decision},
		{"switch-clause-body", opts.failUnderSwitchClauseBody, summary.SwitchClauseBody},
		{"type-switch-clause-body", opts.failUnderTypeSwitchClauseBody, summary.TypeSwitchClauseBody},
		{"select-clause-body", opts.failUnderSelectClauseBody, summary.SelectClauseBody},
		{"switch-clause-selection", opts.failUnderSwitchClauseSelection, summary.SwitchClauseSelection},
		{"type-switch-clause-selection", opts.failUnderTypeSwitchClauseSelection, summary.TypeSwitchClauseSelection},
		{"condition", opts.failUnderCondition, summary.Condition},
		{"mcdc-unique", opts.failUnderMCDCUnique, summary.MCDCUnique},
		{"mcdc-masking", opts.failUnderMCDCMasking, summary.MCDCMasking},
	}
	var failures []string
	for _, check := range checks {
		if check.threshold.set && belowThreshold(check.metric, check.threshold.value) {
			failures = append(failures, fmt.Sprintf(
				"gomcdc: %s %.2f%% (%d/%d) is below %.2f%%",
				check.name,
				metricPercentage(check.metric),
				check.metric.Covered,
				check.metric.Total,
				check.threshold.value,
			))
		}
	}
	return failures
}

func metricPercentage(metric report.MetricSummary) float64 {
	if metric.Percentage == nil {
		return 0
	}
	return *metric.Percentage
}

func belowThreshold(metric report.MetricSummary, threshold float64) bool {
	if metric.Total == 0 {
		return true
	}
	// Percentage is rounded for stable display/JSON. Threshold decisions use
	// the exact rational count so display rounding can never turn a failure
	// into a pass (for example, 2/3 versus 66.669%).
	return float64(metric.Covered)*100 < threshold*float64(metric.Total)
}

func writeReport(opts options, built report.Report, workingDir string, stdout io.Writer) error {
	if opts.format == "html" {
		outputDir := opts.output
		if !filepath.IsAbs(outputDir) {
			outputDir = filepath.Join(workingDir, outputDir)
		}
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			return fmt.Errorf("create HTML report directory %q: %w", outputDir, err)
		}
		outputPath := filepath.Join(outputDir, "index.html")
		file, err := os.CreateTemp(outputDir, ".index.html-*")
		if err != nil {
			return fmt.Errorf("create temporary HTML report in %q: %w", outputDir, err)
		}
		tempPath := file.Name()
		cleanup := func() {
			_ = file.Close()
			_ = os.Remove(tempPath)
		}
		if err := report.WriteHTMLReport(file, built); err != nil {
			cleanup()
			return err
		}
		if err := file.Sync(); err != nil {
			cleanup()
			return fmt.Errorf("sync HTML report %q: %w", tempPath, err)
		}
		if err := file.Close(); err != nil {
			_ = os.Remove(tempPath)
			return fmt.Errorf("close HTML report %q: %w", outputPath, err)
		}
		if err := os.Chmod(tempPath, 0o644); err != nil {
			_ = os.Remove(tempPath)
			return fmt.Errorf("set HTML report permissions %q: %w", tempPath, err)
		}
		if err := os.Rename(tempPath, outputPath); err != nil {
			_ = os.Remove(tempPath)
			return fmt.Errorf("publish HTML report %q: %w", outputPath, err)
		}
		return nil
	}
	var contents []byte
	var err error
	switch opts.format {
	case "json":
		contents, err = report.RenderJSONReport(built)
	case "text":
		contents = []byte(report.RenderTextReport(built))
	default:
		return fmt.Errorf("unsupported report format %q", opts.format)
	}
	if err != nil {
		return err
	}
	if opts.output == "" || opts.output == "-" {
		_, err = stdout.Write(contents)
		return err
	}
	outputPath := opts.output
	if !filepath.IsAbs(outputPath) {
		outputPath = filepath.Join(workingDir, outputPath)
	}
	if err := os.WriteFile(outputPath, contents, 0o644); err != nil {
		return fmt.Errorf("write %q: %w", outputPath, err)
	}
	return nil
}
