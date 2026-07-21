package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shrydev2020/gomcdc/internal/c0"
	"github.com/shrydev2020/gomcdc/internal/c0map"
	"github.com/shrydev2020/gomcdc/internal/compileraware"
	cover "github.com/shrydev2020/gomcdc/internal/coverage"
	"github.com/shrydev2020/gomcdc/internal/gotest"
	"github.com/shrydev2020/gomcdc/internal/instrument"
	"github.com/shrydev2020/gomcdc/internal/loader"
	"github.com/shrydev2020/gomcdc/internal/report"
	"github.com/shrydev2020/gomcdc/internal/runtimecov"
	"github.com/shrydev2020/gomcdc/internal/workspace"
)

type measurementRequest struct {
	context                 context.Context
	timeout                 time.Duration
	goTestArgs              []string
	json                    bool
	workDirParent           string
	keepWorkDir             bool
	loaded                  loader.Result
	sources                 []sourceInstrumentation
	generated               []c0map.GeneratedFile
	ignoredCoverageFiles    []string
	decisions               []cover.DecisionMetadata
	clauses                 []cover.ClauseMetadata
	noMatches               []cover.NoMatchMetadata
	needsC0                 bool
	needsAST                bool
	compilerClauseSelection bool
}

type measurementOutcome struct {
	standardResult   *gotest.Result
	astResult        *gotest.Result
	evidence         acceptedRuntimeEvidence
	c0               *c0.Report
	producerOutcomes []report.ProducerOutcome
	integrityFailure bool
	interrupted      bool
	diagnostics      []measurementDiagnostic
}

type measurementDiagnostic struct {
	phase   string
	code    string
	message string
}

type measurementWorkspace struct {
	measurement string
	workspace   *workspace.Workspace
}

type measurementWorkspaces struct {
	items []measurementWorkspace
}

func (set *measurementWorkspaces) add(measurement string, item *workspace.Workspace) {
	set.items = append(set.items, measurementWorkspace{measurement: measurement, workspace: item})
}

func (set *measurementWorkspaces) cleanup(stderr io.Writer) error {
	var cleanupErr error
	for _, item := range set.items {
		if err := item.workspace.Cleanup(); err != nil {
			fmt.Fprintf(stderr, "gomcdc: %s workspace cleanup failed: %v\n", item.measurement, err)
			cleanupErr = err
		}
		if item.workspace.IsKept() {
			fmt.Fprintf(stderr, "gomcdc: kept %s workspace: %s\n", item.measurement, item.workspace.RootDir)
		}
	}
	return cleanupErr
}

// measure performs source-copy setup, one requested test run, evidence
// collection, and evidence acceptance. It owns measurement failures and
// returns a workspace set so the caller can preserve cleanup timing.
func measure(request measurementRequest, stderr io.Writer) (measurementOutcome, *measurementWorkspaces, error) {
	outcome := measurementOutcome{}
	markInterrupted := func() {
		if outcome.interrupted {
			return
		}
		outcome.interrupted = true
		outcome.diagnostics = append(outcome.diagnostics, measurementDiagnostic{
			phase: "execution", code: "measurement-interrupted", message: "measurement was interrupted",
		})
	}
	workspaces := &measurementWorkspaces{}
	work, err := workspace.Create(request.context, workspace.Options{
		SourceDir:  request.loaded.ModuleRoot,
		TempParent: request.workDirParent,
		Keep:       request.keepWorkDir,
	})
	if err != nil {
		return outcome, workspaces, fmt.Errorf("temporary workspace creation failed: %w", err)
	}
	measurementName := "standard-cover"
	if request.needsAST && request.needsC0 {
		measurementName = "combined"
	} else if request.needsAST {
		measurementName = "ast"
	}
	workspaces.add(measurementName, work)

	var instrumentationResults []instrument.PackageResult
	generatedProfilePaths := generatedPaths(request.generated)
	if !outcome.interrupted && request.needsAST && (len(request.decisions) > 0 || len(request.clauses) > 0) {
		injected, injectErr := runtimecov.Inject(request.context, work.ModuleDir, request.loaded.ModulePath)
		if injectErr != nil {
			if request.context.Err() != nil {
				markInterrupted()
			} else {
				return outcome, workspaces, fmt.Errorf("runtime instrumentation failed: %w", injectErr)
			}
		}
		if request.context.Err() != nil {
			markInterrupted()
		}
		if !outcome.interrupted {
			instrumentationResults, injectErr = instrumentPackages(
				request.context,
				work.ModuleDir,
				request.sources,
				injected.ImportPath,
				request.compilerClauseSelection,
				request.needsC0,
			)
			instrumentErr := injectErr
			if instrumentErr != nil {
				if request.context.Err() != nil {
					markInterrupted()
				} else {
					return outcome, workspaces, fmt.Errorf("source instrumentation failed: %w", instrumentErr)
				}
			}
			for _, result := range instrumentationResults {
				for _, generatedFile := range result.GeneratedFiles {
					relative, relErr := filepath.Rel(work.ModuleDir, generatedFile)
					if relErr != nil {
						return outcome, workspaces, fmt.Errorf("resolve generated coverage file %q: %w", generatedFile, relErr)
					}
					generatedProfilePaths = append(generatedProfilePaths, filepath.ToSlash(relative))
				}
			}
		}
	}

	compilerToolchain := compileraware.Toolchain{}
	if !outcome.interrupted && request.needsAST && request.compilerClauseSelection {
		compilerToolchain, err = compileraware.Prepare(request.context, filepath.Join(work.RootDir, "tools"))
		if err != nil {
			if request.context.Err() != nil {
				markInterrupted()
			} else {
				return outcome, workspaces, fmt.Errorf("compiler-aware instrumentation failed: %w", err)
			}
		}
	}

	runID := ""
	if !outcome.interrupted && request.needsAST {
		runID, err = newRunID()
		if err != nil {
			return outcome, workspaces, fmt.Errorf("create coverage run ID: %w", err)
		}
	}
	if !outcome.interrupted {
		fmt.Fprintf(stderr, "gomcdc: measurement %s\n", measurementName)
		environment := map[string]string{"GOWORK": "off"}
		if request.needsAST {
			environment[runtimecov.RunIDEnv] = runID
		}
		for key, value := range compilerToolchain.Environment {
			environment[key] = value
		}
		coverProfile := ""
		if request.needsC0 {
			coverProfile = filepath.Join(work.RootDir, "cover.out")
		}
		coverPackages := request.loaded.CoverPackageImportSet
		if len(coverPackages) != len(request.loaded.PackageImportSet) {
			// Go 1.26 makes every package test action depend on -coverpkg
			// instrumentation. If any selected package is already known to be
			// unbuildable, retaining -coverpkg would prevent healthy package test
			// binaries from running and destroy recoverable partial evidence.
			coverPackages = nil
		}
		result := runGoTest(request.context, request.timeout, gotest.Options{
			Dir:           filepath.Join(work.ModuleDir, request.loaded.RelativeWorkDir),
			Patterns:      request.loaded.PackageImportSet,
			Args:          request.goTestArgs,
			CoverProfile:  coverProfile,
			CoverPackages: coverPackages,
			DataDirEnv:    runtimeDataDirEnv(request.needsAST),
			DataDir:       work.EventDir,
			Environment:   environment,
			Toolexec:      compilerToolchain.Toolexec,
			JSON:          request.json,
			DisableCover:  !request.needsC0,
			Output:        stderr,
		})
		if request.needsAST {
			outcome.astResult = &result
		} else {
			outcome.standardResult = &result
		}
		if request.context.Err() != nil {
			markInterrupted()
		}
	}

	recoveryContext, cancelRecovery := newRecoveryContext(request.context, interruptedRecoveryTimeout)
	defer cancelRecovery()

	recorded := runtimecov.RecordedEvidence{}
	var collectionErr error
	if request.needsAST {
		recorded, collectionErr = runtimecov.CollectDetailed(recoveryContext, work.EventDir)
	}
	if collectionErr != nil {
		fmt.Fprintf(stderr, "gomcdc: runtime coverage collection failed: %v\n", collectionErr)
		outcome.diagnostics = append(outcome.diagnostics, measurementDiagnostic{
			phase: "collection", code: "runtime-collection-failed", message: "runtime coverage collection failed",
		})
		outcome.integrityFailure = true
	}
	var runtimeValidationIssues runtimeAcceptanceIssues
	var runtimeDiagnosticsErr error
	if request.needsAST {
		acceptedRuntime, validationIssues := acceptRuntimeEvidenceByProducer(recoveryContext, request.decisions, request.clauses, recorded, runID, request.noMatches)
		runtimeValidationIssues = validationIssues
		validationErr := validationIssues.err()
		if validationErr != nil {
			fmt.Fprintf(stderr, "gomcdc: runtime coverage acceptance failed: %v\n", validationErr)
			outcome.diagnostics = append(outcome.diagnostics, measurementDiagnostic{
				phase: "validation", code: "runtime-evidence-invalid", message: "runtime coverage evidence failed acceptance",
			})
			outcome.integrityFailure = true
		}
		for _, diagnostic := range acceptedRuntime.Diagnostics {
			if err := recoveryContext.Err(); err != nil {
				outcome.integrityFailure = true
				runtimeDiagnosticsErr = err
				break
			}
			fmt.Fprintf(stderr, "gomcdc: runtime coverage diagnostic: %s\n", formatRuntimeDiagnostic(diagnostic))
			outcome.diagnostics = append(outcome.diagnostics, measurementDiagnostic{
				phase: "collection", code: "runtime-" + string(diagnostic.Severity), message: runtimeDiagnosticReportMessage(diagnostic.Severity),
			})
		}
		diagnosticsInvalidate, diagnosticsErr := runtimeDiagnosticsInvalidate(recoveryContext, acceptedRuntime.Diagnostics, outcome.astResult)
		runtimeDiagnosticsErr = errors.Join(runtimeDiagnosticsErr, diagnosticsErr)
		if diagnosticsErr != nil || diagnosticsInvalidate {
			outcome.integrityFailure = true
		}
		outcome.evidence = acceptedRuntime
		outcome.producerOutcomes = append(outcome.producerOutcomes, runtimeProducerOutcome(
			report.ProducerASTRuntime,
			outcome.astResult,
			collectionErr,
			runtimeValidationIssues.astErr(),
			runtimeDiagnosticsErr,
			acceptedRuntime.Diagnostics,
		))
		if request.compilerClauseSelection {
			outcome.producerOutcomes = append(outcome.producerOutcomes, runtimeProducerOutcome(
				report.ProducerCompilerSelection,
				outcome.astResult,
				collectionErr,
				runtimeValidationIssues.compilerErr(),
				runtimeDiagnosticsErr,
				acceptedRuntime.Diagnostics,
			))
		}
	}

	if request.needsC0 {
		coverProfile := filepath.Join(work.RootDir, "cover.out")
		profile, profileReadErr := readCoverProfile(coverProfile)
		plans, planErr := sourceCoveragePlans(recoveryContext, request.sources, instrumentationResults)
		if planErr != nil {
			return outcome, workspaces, fmt.Errorf("coverage correspondence assembly failed: %w", planErr)
		}
		runResult := outcome.standardResult
		if request.needsAST {
			runResult = outcome.astResult
		}
		runComplete := runResult != nil && runResult.Status == cover.RunPassed
		var mappingErr error
		if profileReadErr == nil {
			acceptedCover, acceptErr := c0.AcceptProfileEvidence(recoveryContext, profile, plans, c0.ProfileAcceptanceOptions{
				ModulePath: request.loaded.ModulePath, RunComplete: runComplete,
				GeneratedProfilePath: generatedProfilePaths,
				IgnoredProfilePath:   request.ignoredCoverageFiles,
			})
			if acceptErr == nil {
				projected, projectErr := c0.ProjectAcceptedEvidence(recoveryContext, request.loaded.ModulePath, acceptedCover, c0.Options{})
				if projectErr == nil {
					outcome.c0 = &projected
				} else {
					acceptErr = projectErr
				}
			}
			mappingErr = acceptErr
		}
		outcome.producerOutcomes = append(outcome.producerOutcomes, goCoverProducerOutcome(runResult, profileReadErr, mappingErr))
		coverErr := errors.Join(profileReadErr, mappingErr)
		if coverErr != nil {
			fmt.Fprintf(stderr, "gomcdc: Go cover evidence acceptance failed: %v\n", coverErr)
			outcome.diagnostics = append(outcome.diagnostics, measurementDiagnostic{
				phase: "validation", code: "go-cover-evidence-invalid", message: "Go cover evidence failed acceptance",
			})
			if runComplete {
				outcome.integrityFailure = true
			}
			emptyAccepted, emptyErr := c0.AcceptProfileEvidence(
				recoveryContext,
				c0.Profile{Mode: c0.ModeSet},
				plans,
				c0.ProfileAcceptanceOptions{ModulePath: request.loaded.ModulePath},
			)
			if emptyErr == nil {
				projected, projectErr := c0.ProjectAcceptedEvidence(recoveryContext, request.loaded.ModulePath, emptyAccepted, c0.Options{})
				if projectErr == nil {
					outcome.c0 = &projected
				}
			}
		}
	}
	if request.context.Err() != nil {
		markInterrupted()
	}
	return outcome, workspaces, nil
}

func runtimeProducerOutcome(
	producer report.ProducerName,
	run *gotest.Result,
	collectionErr error,
	mappingErr error,
	diagnosticErr error,
	diagnostics []runtimecov.Diagnostic,
) report.ProducerOutcome {
	outcome := report.ProducerOutcome{
		Producer: producer, Integrity: report.ProducerIntegrityValid,
		Completeness: report.ProducerCompletenessPartial,
		Mapping:      report.ProducerMappingComplete,
		Usability:    report.ProducerUsabilityAcceptedPartial,
	}
	if run == nil {
		outcome.Integrity = report.ProducerIntegrityUnavailable
		outcome.Completeness = report.ProducerCompletenessUnavailable
		outcome.Mapping = report.ProducerMappingUnavailable
		outcome.Usability = report.ProducerUsabilityRejected
		return outcome
	}
	if run.Status == cover.RunPassed {
		outcome.Completeness = report.ProducerCompletenessComplete
		outcome.Usability = report.ProducerUsabilityAccepted
	}
	if collectionErr != nil || diagnosticErr != nil {
		outcome.Integrity = report.ProducerIntegrityUnavailable
		outcome.Completeness = report.ProducerCompletenessUnavailable
		outcome.Mapping = report.ProducerMappingUnavailable
		outcome.Usability = report.ProducerUsabilityRejected
		return outcome
	}
	for _, diagnostic := range diagnostics {
		switch diagnostic.Severity {
		case runtimecov.DiagnosticIntegrity:
			outcome.Integrity = report.ProducerIntegrityInvalid
			outcome.Completeness = report.ProducerCompletenessPartial
			outcome.Usability = report.ProducerUsabilityRejected
		case runtimecov.DiagnosticRecoverable:
			if outcome.Integrity == report.ProducerIntegrityValid {
				outcome.Integrity = report.ProducerIntegrityValidPrefix
			}
			outcome.Completeness = report.ProducerCompletenessPartial
			if run.Status == cover.RunPassed {
				outcome.Usability = report.ProducerUsabilityRejected
			}
		default:
			outcome.Integrity = report.ProducerIntegrityInvalid
			outcome.Usability = report.ProducerUsabilityRejected
		}
	}
	if mappingErr != nil {
		outcome.Mapping = report.ProducerMappingInvalid
		outcome.Usability = report.ProducerUsabilityRejected
	}
	return outcome
}

func goCoverProducerOutcome(run *gotest.Result, readErr, mappingErr error) report.ProducerOutcome {
	outcome := report.ProducerOutcome{
		Producer: report.ProducerGoCover, Integrity: report.ProducerIntegrityValid,
		Completeness: report.ProducerCompletenessPartial,
		Mapping:      report.ProducerMappingComplete,
		Usability:    report.ProducerUsabilityAcceptedPartial,
	}
	if run == nil {
		outcome.Integrity = report.ProducerIntegrityUnavailable
		outcome.Completeness = report.ProducerCompletenessUnavailable
		outcome.Mapping = report.ProducerMappingUnavailable
		outcome.Usability = report.ProducerUsabilityRejected
		return outcome
	}
	if run.Status == cover.RunPassed {
		outcome.Completeness = report.ProducerCompletenessComplete
		outcome.Usability = report.ProducerUsabilityAccepted
	}
	if readErr != nil {
		outcome.Integrity = report.ProducerIntegrityInvalid
		if errors.Is(readErr, os.ErrNotExist) {
			outcome.Integrity = report.ProducerIntegrityUnavailable
		}
		outcome.Mapping = report.ProducerMappingUnavailable
		outcome.Usability = report.ProducerUsabilityRejected
		return outcome
	}
	if mappingErr != nil {
		outcome.Mapping = report.ProducerMappingInvalid
		outcome.Usability = report.ProducerUsabilityRejected
	}
	return outcome
}

func generatedPaths(generated []c0map.GeneratedFile) []string {
	paths := make([]string, 0, len(generated))
	for _, file := range generated {
		paths = append(paths, filepath.ToSlash(file.Path))
	}
	sort.Strings(paths)
	return paths
}

func runtimeDataDirEnv(enabled bool) string {
	if enabled {
		return runtimecov.DataDirEnv
	}
	return ""
}

func readCoverProfile(profilePath string) (_ c0.Profile, err error) {
	profileFile, err := os.Open(profilePath)
	if err != nil {
		return c0.Profile{}, err
	}
	defer func() { err = errors.Join(err, profileFile.Close()) }()

	profile, err := c0.ParseProfile(profileFile)
	if err != nil {
		return c0.Profile{}, fmt.Errorf("parse %q: %w", profilePath, err)
	}
	return profile, nil
}

func sourceCoveragePlans(
	ctx context.Context,
	sources []sourceInstrumentation,
	results []instrument.PackageResult,
) ([]c0.SourceCoveragePlan, error) {
	correspondences := make(map[string]c0.CoverageCorrespondence)
	for _, result := range results {
		for _, plan := range result.CoveragePlans {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			originalPath := filepath.ToSlash(filepath.Clean(plan.OriginalPath))
			if _, duplicate := correspondences[originalPath]; duplicate {
				return nil, fmt.Errorf("duplicate coverage correspondence for %q", originalPath)
			}
			correspondences[originalPath] = plan.Correspondence
		}
	}

	plans := make([]c0.SourceCoveragePlan, 0, len(sources))
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if source.inventory == nil {
			continue
		}
		originalPath := filepath.ToSlash(filepath.Clean(source.analysis.RelativePath))
		correspondence, instrumented := correspondences[originalPath]
		if !instrumented {
			identity, err := c0.PlanCoverageCorrespondence(ctx, c0.CorrespondencePlanInput{
				PackagePath:  source.loaded.PackagePath,
				OriginalPath: originalPath,
				Original:     *source.inventory,
				Rewritten:    *source.inventory,
			})
			if err != nil {
				return nil, fmt.Errorf("plan identity coverage correspondence for %q: %w", originalPath, err)
			}
			correspondence = identity
		} else {
			delete(correspondences, originalPath)
		}
		plans = append(plans, c0.SourceCoveragePlan{
			PackagePath:    source.loaded.PackagePath,
			OriginalPath:   originalPath,
			OriginalSource: append([]byte(nil), source.analysis.Source...),
			Inventory:      *source.inventory,
			Correspondence: correspondence,
		})
	}
	if len(correspondences) > 0 {
		unknown := make([]string, 0, len(correspondences))
		for originalPath := range correspondences {
			unknown = append(unknown, originalPath)
		}
		sort.Strings(unknown)
		return nil, fmt.Errorf("coverage correspondence has no original inventory: %s", strings.Join(unknown, ", "))
	}
	sort.Slice(plans, func(i, j int) bool {
		if plans[i].PackagePath != plans[j].PackagePath {
			return plans[i].PackagePath < plans[j].PackagePath
		}
		return plans[i].OriginalPath < plans[j].OriginalPath
	})
	return plans, nil
}

const interruptedRecoveryTimeout = 30 * time.Second

// newRecoveryContext separates bounded finalization from normal measurement.
// Cancellation starts the recovery deadline instead of aborting evidence
// collection immediately; a second OS signal restores forced termination in
// main.
func newRecoveryContext(request context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	base := context.WithoutCancel(request)
	if request.Err() != nil {
		return context.WithTimeout(base, timeout)
	}
	recovery, cancel := context.WithCancel(base)
	stopDeadline := context.AfterFunc(request, func() {
		time.AfterFunc(timeout, cancel)
	})
	return recovery, func() {
		stopDeadline()
		cancel()
	}
}
