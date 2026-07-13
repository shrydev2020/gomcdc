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

	"github.com/shrydev2020/gomcdc/internal/analyzer"
	"github.com/shrydev2020/gomcdc/internal/c0"
	"github.com/shrydev2020/gomcdc/internal/c0map"
	"github.com/shrydev2020/gomcdc/internal/config"
	cover "github.com/shrydev2020/gomcdc/internal/coverage"
	"github.com/shrydev2020/gomcdc/internal/goflags"
	"github.com/shrydev2020/gomcdc/internal/gotest"
	"github.com/shrydev2020/gomcdc/internal/instrument"
	"github.com/shrydev2020/gomcdc/internal/loader"
	"github.com/shrydev2020/gomcdc/internal/mcdc"
	"github.com/shrydev2020/gomcdc/internal/report"
	"github.com/shrydev2020/gomcdc/internal/runtimecov"
)

const (
	ExitSuccess           = 0
	ExitTestsFailed       = 1
	ExitMeasurementFailed = 2
	ExitCoverageThreshold = 3
	ExitInvalidUsage      = 4
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
	return runCoverage(ctx, workingDir, opts, stdout, stderr)
}

type sourceInstrumentation struct {
	loaded   loader.File
	analysis analyzer.File
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
	buildFlags, err := loader.BuildFlags(opts.goTestArgs)
	if err != nil {
		fmt.Fprintf(stderr, "gomcdc: %v\n", err)
		return ExitInvalidUsage
	}
	rawGOFLAGS := os.Getenv("GOFLAGS")
	goFlagWords, goFlagsErr := goflags.Split(rawGOFLAGS)
	if goFlagsErr != nil {
		fmt.Fprintf(stderr, "gomcdc: parse GOFLAGS: %v\n", goFlagsErr)
		return ExitInvalidUsage
	}
	if conflict := measurementFlag(goFlagWords); conflict != "" {
		writeMeasurementFlagError(stderr, conflict, "GOFLAGS")
		return ExitInvalidUsage
	}
	filteredGOFLAGS, goFlagsErr := goflags.WithoutMeasurementFlags(rawGOFLAGS)
	if goFlagsErr != nil {
		fmt.Fprintf(stderr, "gomcdc: parse GOFLAGS: %v\n", goFlagsErr)
		return ExitInvalidUsage
	}
	loaded, err := loader.Load(ctx, loader.Options{
		Dir:          workingDir,
		Patterns:     opts.patterns,
		BuildFlags:   buildFlags,
		IncludeTests: opts.includeTests,
		GOFLAGS:      &filteredGOFLAGS,
	})
	if err != nil {
		fmt.Fprintf(stderr, "gomcdc: package load failed: %v\n", err)
		return ExitMeasurementFailed
	}

	var sources []sourceInstrumentation
	var generatedFiles []c0map.GeneratedFile
	var reportErrors []report.ReportError
	analysisIncomplete := false
	analysisUnknown := 0
	for _, file := range loaded.Files {
		relative, relErr := filepath.Rel(loaded.ModuleRoot, file.Path)
		if relErr != nil {
			fmt.Fprintf(stderr, "gomcdc: resolve source path %q: %v\n", file.Path, relErr)
			return ExitMeasurementFailed
		}
		if excludes.Match(relative) {
			continue
		}
		analysis, analysisErr := analyzer.AnalyzeFile(analyzer.FileOptions{
			Path:        file.Path,
			ModuleDir:   loaded.ModuleRoot,
			ModulePath:  loaded.ModulePath,
			PackagePath: file.PackagePath,
		})
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
		sources = append(sources, sourceInstrumentation{loaded: file, analysis: analysis})
	}
	analyses := make([]analyzer.File, 0, len(sources))
	for _, source := range sources {
		analyses = append(analyses, source.analysis)
	}
	if err := analyzer.DetectCollisions(analyses); err != nil {
		fmt.Fprintf(stderr, "gomcdc: source analysis failed: %v\n", err)
		return ExitMeasurementFailed
	}

	needsC0 := opts.metrics.Enabled(config.MetricStatement) || opts.metrics.Enabled(config.MetricFunction)
	needsASTRun := needsASTRuntime(opts.metrics)
	needsCompilerSelection := opts.metrics.Enabled(config.MetricSwitchClauseSelection) || opts.metrics.Enabled(config.MetricTypeSwitchClauseSelection)
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

	jsonMode := goTestJSONEnabled(opts.goTestArgs)
	measurement, workspaces, measurementErr := measure(measurementRequest{
		context: ctx, timeout: opts.timeout, goTestArgs: opts.goTestArgs, json: jsonMode,
		workDirParent: opts.workDirParent, keepWorkDir: opts.keepWorkDir,
		loaded: loaded, sources: sources, generated: generatedFiles, decisions: decisions, clauses: clauses, noMatches: noMatches,
		needsC0: needsC0, needsAST: needsASTRun, compilerClauseSelection: needsCompilerSelection,
	}, stderr)
	if workspaces != nil {
		defer func() {
			if cleanupErr := workspaces.cleanup(stderr); cleanupErr != nil {
				exitCode = ExitMeasurementFailed
			}
		}()
	}
	if measurementErr != nil {
		fmt.Fprintf(stderr, "gomcdc: %v\n", measurementErr)
		return ExitMeasurementFailed
	}

	input := assembleReportInput(reportAssembly{
		loaded: loaded, sources: sources, coverage: opts.metrics, decisions: decisions,
		clauses: clauses, noMatches: noMatches,
		collection: measurement.collection, c0: measurement.c0, standardResult: measurement.standardResult, astResult: measurement.astResult,
		standardCoverRequested: needsC0, astRequested: needsASTRun,
		astEvidenceUnknown: measurement.astEvidenceIntegrityUnknown, c0EvidenceUnknown: measurement.c0EvidenceIntegrityUnknown,
		instrumentationUnknown: analysisUnknown, integrityFailure: measurement.integrityFailure, analysisIncomplete: analysisIncomplete,
		errors: reportErrors, measurementDiagnostics: measurement.diagnostics,
	})
	built := report.Build(input)
	strictFailure := opts.strict && (built.Instrumentation.HasGaps() || summaryAnalysisIncomplete(built.Summary) > 0)
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
	built = report.Build(input)
	if opts.format == "html" {
		built = report.WithSourceViews(built, input.SourceFiles)
	}
	if err := writeReport(opts, built, workingDir, stdout); err != nil {
		fmt.Fprintf(stderr, "gomcdc: report generation failed: %v\n", err)
		return ExitMeasurementFailed
	}
	if measurement.integrityFailure {
		return classifyExit(false, true, false, false)
	}
	if strictFailure {
		coverage := built.Instrumentation.Total
		fmt.Fprintf(
			stderr,
			"gomcdc: strict coverage failed: discovered=%d supported=%d instrumented=%d unsupported=%d unknown=%d analysis-incomplete=%d\n",
			coverage.Discovered,
			coverage.Supported,
			coverage.Instrumented,
			coverage.Unsupported,
			coverage.Unknown,
			summaryAnalysisIncomplete(built.Summary),
		)
		return classifyExit(false, true, false, false)
	}
	if analysisIncomplete {
		return classifyExit(false, true, false, false)
	}
	if testRunsFailed(measurement.standardResult, measurement.astResult) {
		if measurement.standardResult != nil && measurement.standardResult.Err != nil {
			fmt.Fprintf(stderr, "gomcdc: standard-cover measurement: %v\n", measurement.standardResult.Err)
		}
		if measurement.astResult != nil && measurement.astResult.Err != nil {
			fmt.Fprintf(stderr, "gomcdc: ast measurement: %v\n", measurement.astResult.Err)
		}
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
	metrics := []report.MetricSummary{
		summary.Statement, summary.Function, summary.Decision,
		summary.SwitchClauseBody, summary.TypeSwitchClauseBody, summary.SelectClauseBody,
		summary.SwitchClauseSelection, summary.TypeSwitchClauseSelection,
		summary.Condition, summary.MCDCUnique, summary.MCDCMasking,
	}
	total := 0
	for _, metric := range metrics {
		if metric.Enabled {
			total += metric.AnalysisIncomplete
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

func instrumentPackages(moduleDir string, sources []sourceInstrumentation, runtimeImportPath string, compilerClauseSelection bool) ([]instrument.PackageResult, error) {
	groups := make(map[string]*packageInstrumentation)
	for _, item := range sources {
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
		group.files = append(group.files, instrument.FileMapping{CopyPath: copyPath, Analysis: item.analysis})
	}

	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	results := make([]instrument.PackageResult, 0, len(keys))
	for _, key := range keys {
		group := groups[key]
		activeFiles, err := goFilesInDirectory(group.directory)
		if err != nil {
			return nil, err
		}
		result, err := instrument.InstrumentPackage(instrument.PackageOptions{
			Directory:               group.directory,
			PackageName:             group.packageName,
			PackagePath:             group.packagePath,
			RuntimeImportPath:       runtimeImportPath,
			CompilerClauseSelection: compilerClauseSelection,
			TestOnly:                group.testOnly,
			ActiveFiles:             activeFiles,
			Files:                   group.files,
		})
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func goFilesInDirectory(directory string) ([]string, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read copied package directory %q: %w", directory, err)
	}
	var files []string
	for _, entry := range entries {
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

func combineTestResults(results ...*gotest.Result) (cover.RunStatus, cover.RunFailureKind) {
	status := cover.RunPassed
	kind := cover.RunFailureNone
	for _, result := range results {
		if result == nil {
			continue
		}
		if result.Status == cover.RunTimeout {
			status = cover.RunTimeout
		} else if result.Status == cover.RunFailed && status != cover.RunTimeout {
			status = cover.RunFailed
		}
		if result.FailureKind == cover.RunFailureNone {
			continue
		}
		if kind == cover.RunFailureNone {
			kind = result.FailureKind
		} else if kind != result.FailureKind {
			kind = cover.RunFailureMixed
		}
	}
	if status == cover.RunTimeout {
		kind = cover.RunFailureTimeout
	}
	return status, kind
}

func measurementRuns(standard, ast *gotest.Result) []report.MeasurementRun {
	measurements := make([]report.MeasurementRun, 0, 2)
	appendResult := func(name string, result *gotest.Result) {
		if result == nil {
			return
		}
		packages := make(map[string]string, len(result.Packages))
		for packagePath, status := range result.Packages {
			packages[packagePath] = string(status)
		}
		measurements = append(measurements, report.MeasurementRun{
			Name: name,
			Run: report.TestRun{
				Status:      result.Status,
				FailureKind: result.FailureKind,
				Complete:    result.Status == cover.RunPassed,
			},
			Packages: packages,
		})
	}
	appendResult("standard-cover", standard)
	appendResult("ast", ast)
	return measurements
}

func mergePackageStatus(current, next string) string {
	rank := func(status string) int {
		switch status {
		case string(gotest.PackageBuildFailed):
			return 5
		case string(gotest.PackageFailed):
			return 4
		case string(gotest.PackageStarted):
			return 3
		case string(gotest.PackagePassed):
			return 2
		case string(gotest.PackageSkipped):
			return 1
		default:
			return 0
		}
	}
	if rank(next) > rank(current) {
		return next
	}
	return current
}

func testRunsFailed(results ...*gotest.Result) bool {
	for _, result := range results {
		if result != nil && result.Err != nil {
			return true
		}
	}
	return false
}

var measurementFlags = map[string]struct{}{
	"count": {}, "cover": {}, "coverprofile": {}, "covermode": {},
	"coverpkg": {}, "json": {}, "overlay": {}, "toolexec": {},
}

func measurementFlag(arguments []string) string {
	for _, argument := range arguments {
		if argument == "-args" || argument == "--args" {
			break
		}
		if !strings.HasPrefix(argument, "-") {
			continue
		}
		name := goflags.Name(argument)
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

func goTestJSONEnabled(arguments []string) bool {
	enabled := false
	for _, argument := range arguments {
		if argument == "-args" || argument == "--args" {
			break
		}
		name := strings.TrimLeft(argument, "-")
		switch {
		case name == "json", name == "json=true":
			enabled = true
		case name == "json=false":
			enabled = false
		}
	}
	return enabled
}

func collectC0(
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
	return buildC0Report(profile, loaded, sources, generated)
}

func buildC0Report(
	profile c0.Profile,
	loaded loader.Result,
	sources []sourceInstrumentation,
	generated []c0map.GeneratedFile,
) (*c0.Report, error) {
	mappedSources := make([]c0map.Source, 0, len(sources))
	for _, source := range sources {
		mappedSources = append(mappedSources, c0map.Source{
			PackagePath:    source.loaded.PackagePath,
			RelativePath:   source.analysis.RelativePath,
			OriginalSource: append([]byte(nil), source.analysis.Source...),
		})
	}
	sourceMap, err := c0map.Build(profile, loaded.ModulePath, mappedSources, generated)
	if err != nil {
		return nil, err
	}
	analyzed, err := c0.Analyze(profile, sourceMap, c0.Options{})
	if err != nil {
		return nil, err
	}
	return &analyzed, nil
}

func validateObservations(
	decisions []cover.DecisionMetadata,
	clauses []cover.ClauseMetadata,
	collection runtimecov.Collection,
	runID string,
	noMatches []cover.NoMatchMetadata,
) (runtimecov.Collection, error) {
	knownDecisions := make(map[cover.DecisionID]cover.DecisionMetadata, len(decisions))
	for _, decision := range decisions {
		knownDecisions[decision.ID] = decision
	}
	knownClauses := make(map[cover.ClauseID]cover.ClauseMetadata, len(clauses))
	for _, clause := range clauses {
		knownClauses[clause.ID] = clause
	}
	knownNoMatches := make(map[cover.SwitchID]struct{})
	for _, noMatch := range noMatches {
		knownNoMatches[noMatch.SwitchID] = struct{}{}
	}
	switchDecisions := conditionlessSwitchDecisionOrder(clauses)
	validated := runtimecov.Collection{
		Diagnostics: append([]runtimecov.Diagnostic(nil), collection.Diagnostics...),
		Files:       append([]runtimecov.ProcessFile(nil), collection.Files...),
	}
	var validationErrors []error
	validEvaluations := make(map[cover.EvaluationIdentity]cover.DecisionEvaluation)
	type skipCauseKey struct {
		Identity   cover.EvaluationIdentity
		DecisionID cover.DecisionID
	}
	observedSkips := make(map[skipCauseKey]map[cover.DecisionID]struct{})
	candidateSkips := make(map[skipCauseKey][]cover.DecisionNotEvaluatedObservation)
	for _, evaluation := range collection.Evaluations {
		metadata, exists := knownDecisions[evaluation.DecisionID]
		if !exists {
			validationErrors = append(validationErrors, fmt.Errorf("event contains unknown decision ID 0x%016x", uint64(evaluation.DecisionID)))
			continue
		}
		if evaluation.EvaluationID == 0 {
			validationErrors = append(validationErrors, fmt.Errorf("decision 0x%016x has reserved evaluation ID zero", uint64(evaluation.DecisionID)))
			continue
		}
		if evaluation.RunID != runID {
			validationErrors = append(validationErrors, fmt.Errorf("decision 0x%016x belongs to unexpected run %q", uint64(evaluation.DecisionID), evaluation.RunID))
			continue
		}
		if evaluation.PackagePath != metadata.Package {
			validationErrors = append(validationErrors, fmt.Errorf(
				"decision 0x%016x belongs to package %q, want %q",
				uint64(evaluation.DecisionID), evaluation.PackagePath, metadata.Package,
			))
			continue
		}
		if len(evaluation.Conditions) != len(metadata.Conditions) {
			validationErrors = append(validationErrors, fmt.Errorf(
				"decision 0x%016x has %d condition states, want %d",
				uint64(evaluation.DecisionID), len(evaluation.Conditions), len(metadata.Conditions),
			))
			continue
		}
		if semanticErr := mcdc.ValidateCompletedEvaluation(metadata, evaluation); semanticErr != nil {
			validationErrors = append(validationErrors, fmt.Errorf(
				"decision 0x%016x contains impossible completed evidence: %w",
				uint64(evaluation.DecisionID), semanticErr,
			))
			continue
		}
		validated.Evaluations = append(validated.Evaluations, evaluation)
		validEvaluations[evaluation.Identity()] = evaluation
	}
	for _, observation := range collection.NotEvaluatedDecisions {
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
			validationErrors = append(validationErrors, fmt.Errorf("event contains unknown skipped decision ID 0x%016x", uint64(observation.DecisionID)))
			continue
		case !causeExists:
			validationErrors = append(validationErrors, fmt.Errorf("event contains unknown skip-cause decision ID 0x%016x", uint64(observation.CauseDecisionID)))
			continue
		case observation.RunID != runID || observation.PackagePath != target.Package || cause.Package != target.Package:
			validationErrors = append(validationErrors, fmt.Errorf("skipped decision 0x%016x has inconsistent run or package provenance", uint64(observation.DecisionID)))
			continue
		case target.Kind != cover.DecisionSwitchCase || cause.Kind != cover.DecisionSwitchCase:
			validationErrors = append(validationErrors, fmt.Errorf("skipped decision 0x%016x is not a conditionless-switch decision", uint64(observation.DecisionID)))
			continue
		case !evidenceExists || causeEvaluation.DecisionID != observation.CauseDecisionID || causeEvaluation.Status != cover.EvaluationCompleted || !causeEvaluation.Result:
			validationErrors = append(validationErrors, fmt.Errorf("skipped decision 0x%016x has no completed true cause evaluation", uint64(observation.DecisionID)))
			continue
		}
		causeOrder, causeOrdered := switchDecisions[observation.CauseDecisionID]
		targetOrder, targetOrdered := switchDecisions[observation.DecisionID]
		if !causeOrdered || !targetOrdered || causeOrder.groupID != targetOrder.groupID || targetOrder.position <= causeOrder.position {
			validationErrors = append(validationErrors, fmt.Errorf("skipped decision 0x%016x is not later in the same conditionless switch", uint64(observation.DecisionID)))
			continue
		}
		key := skipCauseKey{Identity: identity, DecisionID: observation.CauseDecisionID}
		if observedSkips[key] == nil {
			observedSkips[key] = make(map[cover.DecisionID]struct{})
		}
		observedSkips[key][observation.DecisionID] = struct{}{}
		candidateSkips[key] = append(candidateSkips[key], observation)
	}
	for _, evaluation := range validated.Evaluations {
		identity := evaluation.Identity()
		order, exists := switchDecisions[evaluation.DecisionID]
		if !exists || evaluation.Status != cover.EvaluationCompleted || !evaluation.Result || len(order.suffix) == 0 {
			continue
		}
		observed := observedSkips[skipCauseKey{Identity: identity, DecisionID: evaluation.DecisionID}]
		key := skipCauseKey{Identity: identity, DecisionID: evaluation.DecisionID}
		if len(candidateSkips[key]) != len(observed) {
			validationErrors = append(validationErrors, fmt.Errorf("conditionless-switch decision 0x%016x has duplicate skipped-decision evidence", uint64(evaluation.DecisionID)))
			continue
		}
		if len(observed) != len(order.suffix) {
			validationErrors = append(validationErrors, fmt.Errorf(
				"conditionless-switch decision 0x%016x skipped %d decisions, want complete suffix of %d",
				uint64(evaluation.DecisionID), len(observed), len(order.suffix),
			))
			continue
		}
		complete := true
		for _, expected := range order.suffix {
			if _, found := observed[expected]; !found {
				complete = false
				validationErrors = append(validationErrors, fmt.Errorf(
					"conditionless-switch decision 0x%016x omitted skipped decision 0x%016x",
					uint64(evaluation.DecisionID), uint64(expected),
				))
			}
		}
		if complete {
			validated.NotEvaluatedDecisions = append(validated.NotEvaluatedDecisions, candidateSkips[key]...)
		}
	}
	for _, observation := range collection.Clauses {
		if observation.Event == cover.ClauseNoMatchSelection {
			if _, exists := knownNoMatches[observation.SwitchID]; !exists || observation.AlternativeKnown {
				validationErrors = append(validationErrors, fmt.Errorf("event contains unknown no-match switch ID 0x%016x", uint64(observation.SwitchID)))
				continue
			}
			validated.Clauses = append(validated.Clauses, observation)
			continue
		}
		metadata, exists := knownClauses[observation.ClauseID]
		if !exists {
			validationErrors = append(validationErrors, fmt.Errorf("event contains unknown clause ID 0x%016x", uint64(observation.ClauseID)))
			continue
		}
		if observation.SwitchID != 0 && observation.SwitchID != metadata.SwitchID {
			validationErrors = append(validationErrors, fmt.Errorf("clause 0x%016x has inconsistent switch ID", uint64(observation.ClauseID)))
			continue
		}
		switch observation.Event {
		case cover.ClauseBodyExecution:
			if observation.AlternativeKnown {
				validationErrors = append(validationErrors, fmt.Errorf("clause 0x%016x body event carries a case alternative", uint64(observation.ClauseID)))
				continue
			}
		case cover.ClauseDirectSelection:
			if metadata.Kind != cover.ClauseExpressionSwitch && metadata.Kind != cover.ClauseTypeSwitch {
				validationErrors = append(validationErrors, fmt.Errorf("clause 0x%016x kind %q cannot carry direct-selection evidence", uint64(observation.ClauseID), metadata.Kind))
				continue
			}
			alternatives := len(metadata.Expressions)
			if metadata.Kind == cover.ClauseTypeSwitch {
				alternatives = len(metadata.Types)
			}
			if metadata.Role == cover.ClauseDefault {
				if observation.AlternativeKnown {
					validationErrors = append(validationErrors, fmt.Errorf("default clause 0x%016x carries a case alternative", uint64(observation.ClauseID)))
					continue
				}
			} else if !observation.AlternativeKnown || int(observation.AlternativeIndex) >= alternatives {
				validationErrors = append(validationErrors, fmt.Errorf("clause 0x%016x carries an invalid case alternative", uint64(observation.ClauseID)))
				continue
			}
		default:
			validationErrors = append(validationErrors, fmt.Errorf("clause 0x%016x has unsupported event %q", uint64(observation.ClauseID), observation.Event))
			continue
		}
		validated.Clauses = append(validated.Clauses, observation)
	}
	return validated, errors.Join(validationErrors...)
}

type switchDecisionOrder struct {
	groupID  cover.ClauseGroupID
	position int
	suffix   []cover.DecisionID
}

func conditionlessSwitchDecisionOrder(clauses []cover.ClauseMetadata) map[cover.DecisionID]switchDecisionOrder {
	groups := make(map[cover.ClauseGroupID][]cover.ClauseMetadata)
	for _, clause := range clauses {
		if clause.Kind == cover.ClauseConditionlessSwitch && clause.GroupID != 0 {
			groups[clause.GroupID] = append(groups[clause.GroupID], clause)
		}
	}
	result := make(map[cover.DecisionID]switchDecisionOrder)
	for groupID, members := range groups {
		sort.Slice(members, func(i, j int) bool { return members[i].Index < members[j].Index })
		var sequence []cover.DecisionID
		for _, clause := range members {
			sequence = append(sequence, clause.DecisionIDs...)
		}
		for position, decisionID := range sequence {
			result[decisionID] = switchDecisionOrder{
				groupID:  groupID,
				position: position,
				suffix:   append([]cover.DecisionID(nil), sequence[position+1:]...),
			}
		}
	}
	return result
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

func runtimeDiagnosticsInvalidate(diagnostics []runtimecov.Diagnostic, astResult *gotest.Result) bool {
	if astResult != nil && len(astResult.RuntimeDiagnostics) > 0 {
		return true
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity != runtimecov.DiagnosticRecoverable {
			return true
		}
	}
	return len(diagnostics) > 0 && (astResult == nil || astResult.Status == cover.RunPassed)
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
