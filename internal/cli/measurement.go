package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/shrydev2020/gomcdc/internal/c0"
	"github.com/shrydev2020/gomcdc/internal/c0map"
	"github.com/shrydev2020/gomcdc/internal/compileraware"
	cover "github.com/shrydev2020/gomcdc/internal/coverage"
	"github.com/shrydev2020/gomcdc/internal/gotest"
	"github.com/shrydev2020/gomcdc/internal/loader"
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
	decisions               []cover.DecisionMetadata
	clauses                 []cover.ClauseMetadata
	noMatches               []cover.NoMatchMetadata
	needsC0                 bool
	needsAST                bool
	compilerClauseSelection bool
}

type measurementOutcome struct {
	standardResult              *gotest.Result
	astResult                   *gotest.Result
	evidence                    verifiedRuntimeEvidence
	c0                          *c0.Report
	astEvidenceIntegrityUnknown bool
	c0EvidenceIntegrityUnknown  bool
	integrityFailure            bool
	diagnostics                 []measurementDiagnostic
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

// measure performs source-copy setup, the requested test runs, evidence
// collection, and evidence verification. It owns measurement failures and
// returns a workspace set so the caller can preserve cleanup timing.
func measure(request measurementRequest, stderr io.Writer) (measurementOutcome, *measurementWorkspaces, error) {
	outcome := measurementOutcome{}
	workspaces := &measurementWorkspaces{}
	primary, err := workspace.Create(workspace.Options{
		SourceDir:  request.loaded.ModuleRoot,
		TempParent: request.workDirParent,
		Keep:       request.keepWorkDir,
	})
	if err != nil {
		return outcome, workspaces, fmt.Errorf("temporary workspace creation failed: %w", err)
	}
	var standardWork, astWork *workspace.Workspace
	if request.needsAST {
		astWork = primary
		workspaces.add("ast", astWork)
	} else {
		standardWork = primary
		workspaces.add("standard-cover", standardWork)
	}
	if request.needsC0 && request.needsAST {
		standardWork, err = workspace.Create(workspace.Options{
			SourceDir:  request.loaded.ModuleRoot,
			TempParent: request.workDirParent,
			Keep:       request.keepWorkDir,
		})
		if err != nil {
			return outcome, workspaces, fmt.Errorf("standard-cover workspace creation failed: %w", err)
		}
		workspaces.add("standard-cover", standardWork)
	}

	coverProfile := ""
	if standardWork != nil {
		coverProfile = filepath.Join(standardWork.RootDir, "standard-cover.out")
	}
	if request.needsC0 {
		fmt.Fprintln(stderr, "gomcdc: measurement standard-cover (original source)")
		result := runGoTest(request.context, request.timeout, gotest.Options{
			Dir:           filepath.Join(standardWork.ModuleDir, request.loaded.RelativeWorkDir),
			Patterns:      request.loaded.PackageImportSet,
			Args:          request.goTestArgs,
			CoverProfile:  coverProfile,
			CoverPackages: request.loaded.CoverPackageImportSet,
			Environment:   map[string]string{"GOWORK": "off"},
			JSON:          request.json,
			Output:        stderr,
		})
		outcome.standardResult = &result
	}

	if request.needsAST && (len(request.decisions) > 0 || len(request.clauses) > 0) {
		injected, injectErr := runtimecov.Inject(astWork.ModuleDir, request.loaded.ModulePath)
		if injectErr != nil {
			return outcome, workspaces, fmt.Errorf("runtime instrumentation failed: %w", injectErr)
		}
		if _, instrumentErr := instrumentPackages(astWork.ModuleDir, request.sources, injected.ImportPath, request.compilerClauseSelection); instrumentErr != nil {
			return outcome, workspaces, fmt.Errorf("source instrumentation failed: %w", instrumentErr)
		}
	}

	compilerToolchain := compileraware.Toolchain{}
	if request.needsAST && request.compilerClauseSelection {
		compilerToolchain, err = compileraware.Prepare(request.context, filepath.Join(astWork.RootDir, "tools"))
		if err != nil {
			return outcome, workspaces, fmt.Errorf("compiler-aware instrumentation failed: %w", err)
		}
	}

	runID := ""
	if request.needsAST {
		runID, err = newRunID()
		if err != nil {
			return outcome, workspaces, fmt.Errorf("create coverage run ID: %w", err)
		}
		fmt.Fprintln(stderr, "gomcdc: measurement ast")
		astEnvironment := map[string]string{
			runtimecov.RunIDEnv: runID,
			"GOWORK":            "off",
		}
		for key, value := range compilerToolchain.Environment {
			astEnvironment[key] = value
		}
		result := runGoTest(request.context, request.timeout, gotest.Options{
			Dir:          filepath.Join(astWork.ModuleDir, request.loaded.RelativeWorkDir),
			Patterns:     request.loaded.PackageImportSet,
			Args:         request.goTestArgs,
			DataDirEnv:   runtimecov.DataDirEnv,
			DataDir:      astWork.EventDir,
			Environment:  astEnvironment,
			Toolexec:     compilerToolchain.Toolexec,
			JSON:         request.json,
			DisableCover: true,
			Output:       stderr,
		})
		outcome.astResult = &result
	}

	recorded := runtimecov.RecordedEvidence{}
	var collectionErr error
	if request.needsAST {
		recorded, collectionErr = runtimecov.CollectDetailed(astWork.EventDir)
	}
	if collectionErr != nil {
		fmt.Fprintf(stderr, "gomcdc: runtime coverage collection failed: %v\n", collectionErr)
		outcome.diagnostics = append(outcome.diagnostics, measurementDiagnostic{
			phase: "collection", code: "runtime-collection-failed", message: "runtime coverage collection failed",
		})
		outcome.integrityFailure = true
		outcome.astEvidenceIntegrityUnknown = true
	}
	verified, validationErr := verifyRuntimeEvidence(request.decisions, request.clauses, recorded, runID, request.noMatches)
	if validationErr != nil {
		fmt.Fprintf(stderr, "gomcdc: runtime coverage validation failed: %v\n", validationErr)
		outcome.diagnostics = append(outcome.diagnostics, measurementDiagnostic{
			phase: "validation", code: "runtime-evidence-invalid", message: "runtime coverage evidence failed validation",
		})
		outcome.integrityFailure = true
		outcome.astEvidenceIntegrityUnknown = true
	}
	for _, diagnostic := range verified.Diagnostics {
		fmt.Fprintf(stderr, "gomcdc: runtime coverage diagnostic: %s\n", formatRuntimeDiagnostic(diagnostic))
		outcome.diagnostics = append(outcome.diagnostics, measurementDiagnostic{
			phase: "collection", code: "runtime-" + string(diagnostic.Severity), message: runtimeDiagnosticReportMessage(diagnostic.Severity),
		})
	}
	if runtimeDiagnosticsInvalidate(verified.Diagnostics, outcome.astResult) {
		outcome.integrityFailure = true
		outcome.astEvidenceIntegrityUnknown = true
	}
	outcome.evidence = verified

	if request.needsC0 {
		analyzed, c0Err := collectC0(coverProfile, request.loaded, request.sources, request.generated)
		if c0Err != nil {
			outcome.diagnostics = append(outcome.diagnostics, measurementDiagnostic{
				phase: "collection", code: "standard-cover-collection-failed", message: "standard cover profile collection failed",
			})
			outcome.c0EvidenceIntegrityUnknown = true
			standardPassed := outcome.standardResult != nil && outcome.standardResult.Status == cover.RunPassed
			if standardPassed {
				fmt.Fprintf(stderr, "gomcdc: C0 coverage collection failed: %v\n", c0Err)
				outcome.integrityFailure = true
			}
			partialInventory, inventoryErr := buildC0Report(c0.Profile{Mode: c0.ModeSet}, request.loaded, request.sources, request.generated)
			if inventoryErr != nil {
				fmt.Fprintf(stderr, "gomcdc: C0 inventory recovery failed after %v: %v\n", c0Err, inventoryErr)
				outcome.diagnostics = append(outcome.diagnostics, measurementDiagnostic{
					phase: "analysis", code: "standard-cover-inventory-failed", message: "standard cover inventory recovery failed",
				})
			} else {
				outcome.c0 = partialInventory
			}
		} else {
			outcome.c0 = analyzed
		}
	}
	return outcome, workspaces, nil
}
