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

	"github.com/shrydev2020/gomcdc/v2/internal/analysis"
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
	measurementdata "github.com/shrydev2020/gomcdc/v2/internal/measurement"
	"github.com/shrydev2020/gomcdc/v2/internal/modulecontext"
	"github.com/shrydev2020/gomcdc/v2/internal/report"
	"github.com/shrydev2020/gomcdc/v2/internal/resultfile"
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
	// Measurement and report paths need a working directory. Informational and invalid
	// top-level commands must remain usable even when no module is available.
	if len(args) == 0 || (args[0] != "test" && args[0] != "report" && args[0] != "merge" && args[0] != "reanalyze" && args[0] != "record" && args[0] != "analyze" && args[0] != "require") {
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
	if args[0] == "report" {
		return runReport(ctx, workingDir, args[1:], stdout, stderr)
	}
	if args[0] == "record" {
		return runRecord(ctx, workingDir, args[1:], stdout, stderr)
	}
	if args[0] == "analyze" {
		return runMeasurementStage(ctx, workingDir, "analyze", args[1:], stdout, stderr)
	}
	if args[0] == "require" {
		return runRequire(ctx, workingDir, args[1:], stdout, stderr)
	}
	if args[0] == "merge" {
		return runMeasurementStage(ctx, workingDir, "merge", args[1:], stdout, stderr)
	}
	if args[0] == "reanalyze" {
		return runReanalysis(ctx, workingDir, args[1:], stdout, stderr)
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
	if err := validateSaveDestination(opts, workingDir); err != nil {
		fmt.Fprintf(stderr, "gomcdc: %v\n", err)
		return ExitInvalidUsage
	}
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
	var savedSettings measurementdata.Settings
	{
		savedSettings, err = captureResultSettings(ctx, opts, loaded, work, filteredGOFLAGS)
		if err != nil {
			fmt.Fprintf(stderr, "gomcdc: save settings: %v\n", err)
			if ctx.Err() != nil {
				return ExitInterrupted
			}
			return ExitMeasurementFailed
		}
		savedSettings.ModuleConfigDigests = sourceConfiguration.ConfigurationDigests()
	}
	var sources []sourceInstrumentation
	var generatedFiles []c0map.GeneratedFile
	var ignoredCoverageFiles []string
	var reportErrors []analysis.ReportError
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
		fileAnalysis, analysisErr := analyzer.AnalyzeFile(analyzer.FileOptions{
			Path:         file.Path,
			OriginalPath: filepath.Join(sourceConfiguration.MainModuleDir(), relative),
			ModuleDir:    loaded.ModuleRoot,
			ModulePath:   loaded.ModulePath,
			PackagePath:  file.PackagePath,
		})
		fileAnalysis.TestContexts = file.TestContexts
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
			reportErrors = append(reportErrors, analysis.ReportError{
				Phase: "analysis", Code: "source-analysis-failed", Message: "source analysis did not complete",
				Package: file.PackagePath, Path: filepath.ToSlash(relative),
			})
			continue
		}
		if fileAnalysis.Generated {
			generatedFiles = append(generatedFiles, c0map.GeneratedFile{Path: filepath.ToSlash(relative)})
			continue
		}
		var inventory *c0.FileInventory
		if needsC0 {
			builtInventory, inventoryErr := c0.BuildInventory(fileAnalysis.RelativePath, fileAnalysis.Source)
			if inventoryErr != nil {
				fmt.Fprintf(stderr, "gomcdc: C0 inventory failed %q: %v\n", relative, inventoryErr)
				return ExitMeasurementFailed
			}
			inventory = &builtInventory
		}
		sources = append(sources, sourceInstrumentation{loaded: file, analysis: fileAnalysis, inventory: inventory})
	}
	if !opts.includeTests {
		for _, file := range loaded.TestFiles {
			if file.TestContexts.Empty() {
				continue
			}
			relative, err := filepath.Rel(loaded.ModuleRoot, file.Path)
			if err != nil {
				return ExitMeasurementFailed
			}
			analysis, err := analyzer.AnalyzeFile(analyzer.FileOptions{
				Path: file.Path, OriginalPath: filepath.Join(sourceConfiguration.MainModuleDir(), relative),
				ModuleDir: loaded.ModuleRoot, ModulePath: loaded.ModulePath, PackagePath: file.PackagePath,
			})
			if err != nil || analysis.Generated {
				continue
			}
			analysis.Decisions, analysis.Clauses, analysis.NoMatches = nil, nil, nil
			analysis.TestContexts = file.TestContexts
			analysis.TestContextOnly = true
			sources = append(sources, sourceInstrumentation{loaded: file, analysis: analysis})
		}
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
		reportErrors = append(reportErrors, analysis.ReportError{
			Phase: "cleanup", Code: "workspace-cleanup-failed", Message: "temporary workspace cleanup failed",
		})
	}

	input := assembleAnalysisInput(analysisAssembly{
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
	archive, archiveErr := archiveMeasurement(savedSettings, input, measurement)
	if archiveErr != nil {
		fmt.Fprintf(stderr, "gomcdc: measurement archive: %v\n", archiveErr)
		return ExitMeasurementFailed
	}
	if opts.recordOnly {
		if err := measurementdata.Write(context.Background(), resolveOutputPath(workingDir, opts.save), archive); err != nil {
			fmt.Fprintf(stderr, "gomcdc: save measurement: %v\n", err)
			return ExitMeasurementFailed
		}
		fmt.Fprintf(stdout, "Saved measurement %s (%s).\n", opts.save, savedSettings.InvocationID)
		if measurement.interrupted {
			return ExitInterrupted
		}
		return classifyExit(false, measurement.integrityFailure || finalizationErr != nil || analysisIncomplete, testRunFailed(measurement.testResult), false)
	}
	// On interruption, retain recovered evidence but let canceled analysis
	// mark unfinished obligations rather than starting a fresh solver budget.
	built, analyzeErr := analysis.FromMeasurement(ctx, archive, opts.maskingAnalysisBudget())
	if ctx.Err() != nil {
		input.Probes = measurement.probes
		built, analyzeErr = analysis.Analyze(input), nil
	}
	if analyzeErr != nil {
		fmt.Fprintf(stderr, "gomcdc: analyze measurement: %v\n", analyzeErr)
		return ExitMeasurementFailed
	}
	strictFailure := opts.strict && (built.Instrumentation.HasGaps() || summaryUnknown(built.Summary) > 0 || summaryAnalysisIncomplete(built.Summary) > 0)
	thresholds := thresholdFailures(opts, built.Summary)
	input.Results.Strict = requestedResult(opts.strict, !strictFailure)
	input.Results.Threshold = requestedResult(thresholdRequested(opts), len(thresholds) == 0)
	if strictFailure {
		input.Errors = append(input.Errors, analysis.ReportError{
			Phase: "validation", Code: "strict-coverage-gap", Message: "requested coverage contains unsupported, unknown, analysis-incomplete, or uninstrumented entities",
		})
	}
	for _, threshold := range thresholds {
		input.Errors = append(input.Errors, analysis.ReportError{
			Phase: "threshold", Code: "coverage-threshold-failed", Message: threshold,
		})
	}
	built = analysis.WithRunResultsAndErrors(built, input.Results, input.Errors)
	if opts.save != "" {
		archive.Runs[0].Results = built.Run.Results
		archive.Runs[0].Diagnostics = nil
		for _, d := range built.Errors {
			archive.Runs[0].Diagnostics = append(archive.Runs[0].Diagnostics, measurementdata.Diagnostic{Phase: d.Phase, Code: d.Code, Message: d.Message, Package: d.Package, Path: d.Path})
		}
		saved := resultfile.NewMeasurement(archive, analysis.NewSettings(opts.maskingAnalysisBudget()), built)
		if err := resultfile.Write(resolveOutputPath(workingDir, opts.save), saved); err != nil {
			fmt.Fprintf(stderr, "gomcdc: save result: %v\n", err)
			return ExitMeasurementFailed
		}
	}
	if err := writeReport(opts, built, workingDir, stdout, input.SourceFiles); err != nil {
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

func requestedResult(requested, passed bool) analysis.ResultStatus {
	if !requested {
		return analysis.ResultNotRequested
	}
	return passFailResult(passed)
}

func thresholdRequested(opts options) bool {
	for _, threshold := range thresholdSettings(opts) {
		if threshold.value.set {
			return true
		}
	}
	return false
}

func summaryAnalysisIncomplete(summary analysis.Summary) int {
	return sumEnabledSummaryField(summary, func(metric analysis.MetricSummary) int { return metric.AnalysisIncomplete })
}

func summaryUnknown(summary analysis.Summary) int {
	return sumEnabledSummaryField(summary, func(metric analysis.MetricSummary) int { return metric.Unknown })
}

func sumEnabledSummaryField(summary analysis.Summary, value func(analysis.MetricSummary) int) int {
	metrics := []analysis.MetricSummary{
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

func sourceFileInputs(sources []sourceInstrumentation) []analysis.SourceFileInput {
	result := make([]analysis.SourceFileInput, 0, len(sources))
	for _, source := range sources {
		if source.analysis.TestContextOnly {
			continue
		}
		result = append(result, analysis.SourceFileInput{
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
		if len(item.analysis.Decisions) == 0 && len(item.analysis.Clauses) == 0 && (!compilerClauseSelection || len(item.analysis.NoMatches) == 0) && item.analysis.TestContexts.Empty() {
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

func measurementRuns(name string, result *gotest.Result) []analysis.MeasurementRun {
	if result == nil {
		return nil
	}
	packages := make(map[string]string, len(result.Packages))
	for packagePath, status := range result.Packages {
		packages[packagePath] = string(status)
	}
	return []analysis.MeasurementRun{{
		Name: name,
		Run: analysis.TestRun{
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

func thresholdFailures(opts options, summary analysis.Summary) []string {
	checks := []struct {
		name      string
		threshold optionalFloat
		metric    analysis.MetricSummary
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

func metricPercentage(metric analysis.MetricSummary) float64 {
	if metric.Percentage == nil {
		return 0
	}
	return *metric.Percentage
}

func belowThreshold(metric analysis.MetricSummary, threshold float64) bool {
	if metric.Total == 0 {
		return true
	}
	// Percentage is rounded for stable display/JSON. Threshold decisions use
	// the exact rational count so display rounding can never turn a failure
	// into a pass (for example, 2/3 versus 66.669%).
	return float64(metric.Covered)*100 < threshold*float64(metric.Total)
}

func writeReport(opts options, built analysis.Result, workingDir string, stdout io.Writer, sources ...[]analysis.SourceFileInput) error {
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
		var originals []analysis.SourceFileInput
		if len(sources) > 0 {
			originals = sources[0]
		}
		if err := report.WriteHTMLWithSources(file, built, originals); err != nil {
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
