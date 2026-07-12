package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/shrydev2020/gomcdc/internal/c0"
	cover "github.com/shrydev2020/gomcdc/internal/coverage"
	"github.com/shrydev2020/gomcdc/internal/gotest"
	"github.com/shrydev2020/gomcdc/internal/loader"
	"github.com/shrydev2020/gomcdc/internal/runtimecov"
	"github.com/shrydev2020/gomcdc/internal/workspace"
)

type measurementRequest struct {
	context       context.Context
	timeout       time.Duration
	goTestArgs    []string
	json          bool
	workDirParent string
	keepWorkDir   bool
	loaded        loader.Result
	sources       []sourceInstrumentation
	decisions     []cover.DecisionMetadata
	clauses       []cover.ClauseMetadata
	noMatches     []cover.NoMatchMetadata
	needsC0       bool
	needsAST      bool
}

type measurementOutcome struct {
	standardResult              *gotest.Result
	astResult                   *gotest.Result
	collection                  runtimecov.Collection
	c0                          *c0.Report
	astEvidenceIntegrityUnknown bool
	c0EvidenceIntegrityUnknown  bool
	integrityFailure            bool
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
// collection, and evidence validation. It owns measurement failures and
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
		if _, instrumentErr := instrumentPackages(astWork.ModuleDir, request.sources, injected.ImportPath); instrumentErr != nil {
			return outcome, workspaces, fmt.Errorf("source instrumentation failed: %w", instrumentErr)
		}
	}

	runID := ""
	if request.needsAST {
		runID, err = newRunID()
		if err != nil {
			return outcome, workspaces, fmt.Errorf("create coverage run ID: %w", err)
		}
		fmt.Fprintln(stderr, "gomcdc: measurement ast")
		result := runGoTest(request.context, request.timeout, gotest.Options{
			Dir:        filepath.Join(astWork.ModuleDir, request.loaded.RelativeWorkDir),
			Patterns:   request.loaded.PackageImportSet,
			Args:       request.goTestArgs,
			DataDirEnv: runtimecov.DataDirEnv,
			DataDir:    astWork.EventDir,
			Environment: map[string]string{
				runtimecov.RunIDEnv: runID,
				"GOWORK":            "off",
			},
			JSON:         request.json,
			DisableCover: true,
			Output:       stderr,
		})
		outcome.astResult = &result
	}

	collection := runtimecov.Collection{}
	var collectionErr error
	if request.needsAST {
		collection, collectionErr = runtimecov.CollectDetailed(astWork.EventDir)
	}
	if collectionErr != nil {
		fmt.Fprintf(stderr, "gomcdc: runtime coverage collection failed: %v\n", collectionErr)
		outcome.integrityFailure = true
		outcome.astEvidenceIntegrityUnknown = true
	}
	validated, validationErr := validateObservations(request.decisions, request.clauses, collection, runID, request.noMatches)
	collection = validated
	if validationErr != nil {
		fmt.Fprintf(stderr, "gomcdc: runtime coverage validation failed: %v\n", validationErr)
		outcome.integrityFailure = true
		outcome.astEvidenceIntegrityUnknown = true
	}
	for _, diagnostic := range collection.Diagnostics {
		fmt.Fprintf(stderr, "gomcdc: runtime coverage diagnostic: %s\n", formatRuntimeDiagnostic(diagnostic))
	}
	if runtimeDiagnosticsInvalidate(collection.Diagnostics, outcome.astResult) {
		outcome.integrityFailure = true
		outcome.astEvidenceIntegrityUnknown = true
	}
	outcome.collection = collection

	if request.needsC0 {
		analyzed, c0Err := collectC0(coverProfile, request.loaded, request.sources, nil)
		if c0Err != nil {
			outcome.c0EvidenceIntegrityUnknown = true
			standardPassed := outcome.standardResult != nil && outcome.standardResult.Status == cover.RunPassed
			if standardPassed {
				fmt.Fprintf(stderr, "gomcdc: C0 coverage collection failed: %v\n", c0Err)
				outcome.integrityFailure = true
			}
			partialInventory, inventoryErr := buildC0Report(c0.Profile{Mode: c0.ModeSet}, request.loaded, request.sources, nil)
			if inventoryErr != nil {
				fmt.Fprintf(stderr, "gomcdc: C0 inventory recovery failed after %v: %v\n", c0Err, inventoryErr)
			} else {
				outcome.c0 = partialInventory
			}
		} else {
			outcome.c0 = analyzed
		}
	}
	return outcome, workspaces, nil
}
