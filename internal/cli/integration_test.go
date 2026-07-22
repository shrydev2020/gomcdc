package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shrydev2020/gomcdc/internal/backend"
	cover "github.com/shrydev2020/gomcdc/internal/coverage"
	"github.com/shrydev2020/gomcdc/internal/gotest"
	"github.com/shrydev2020/gomcdc/internal/report"
	"github.com/shrydev2020/gomcdc/internal/runtimecov"
)

func TestCancellationBoundariesStopHelperWork(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := goFilesInDirectory(ctx, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("goFilesInDirectory error = %v, want context.Canceled", err)
	}
	if _, err := acceptRuntimeEvidence(ctx, nil, nil, runtimecov.RecordedEvidence{}, "run", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("acceptRuntimeEvidence error = %v, want context.Canceled", err)
	}
	if _, err := conditionlessSwitchDecisionOrder(ctx, []cover.ClauseMetadata{{GroupID: 1}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("conditionlessSwitchDecisionOrder error = %v, want context.Canceled", err)
	}
	if _, err := deduplicateAcceptedEvaluations(ctx, []cover.DecisionEvaluation{{}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("deduplicateAcceptedEvaluations error = %v, want context.Canceled", err)
	}
	if _, err := runtimeDiagnosticsInvalidate(ctx, []runtimecov.Diagnostic{{Severity: runtimecov.DiagnosticRecoverable}}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("runtimeDiagnosticsInvalidate error = %v, want context.Canceled", err)
	}
	if _, err := instrumentPackages(ctx, t.TempDir(), nil, "example.test/runtime", false, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("instrumentPackages error = %v, want context.Canceled", err)
	}
}

func TestGoFilesInDirectoryFiltersDirectoriesAndNonGoFiles(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "notes.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	goFile := filepath.Join(directory, "active.go")
	if err := os.WriteFile(goFile, []byte("package active\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := goFilesInDirectory(t.Context(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != goFile {
		t.Fatalf("goFilesInDirectory = %v, want [%s]", files, goFile)
	}
	if _, err := goFilesInDirectory(t.Context(), filepath.Join(directory, "missing")); err == nil {
		t.Fatal("goFilesInDirectory accepted a missing directory")
	}
}

func TestSingleModuleGoWorkSettingsApplyToAnalysisAndTest(t *testing.T) {
	root := t.TempDir()
	module := filepath.Join(root, "module")
	dependency := filepath.Join(root, "dependency")
	writeIntegrationFile(t, filepath.Join(module, "go.mod"), "module example.test/workmodule\n\ngo 1.26\n\nrequire example.test/dependency v0.0.0\n")
	writeIntegrationFile(t, filepath.Join(module, "value.go"), "package workmodule\n\nimport \"example.test/dependency\"\n\nfunc Value() bool { return dependency.Value() }\n")
	writeIntegrationFile(t, filepath.Join(module, "value_test.go"), "package workmodule\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) { if !Value() { t.Fatal(\"false\") } }\n")
	writeIntegrationFile(t, filepath.Join(dependency, "go.mod"), "module example.test/dependency\n\ngo 1.26\n")
	writeIntegrationFile(t, filepath.Join(dependency, "value.go"), "package dependency\n\nfunc Value() bool { return true }\n")
	goWork := filepath.Join(root, "go.work")
	writeIntegrationFile(t, goWork, "go 1.26\n\nuse ./module\n\nreplace example.test/dependency => ./dependency\n")
	t.Setenv("GOWORK", goWork)

	for _, invocation := range []struct {
		name    string
		dir     string
		pattern string
	}{
		{name: "module-directory", dir: module, pattern: "."},
		{name: "workspace-root", dir: root, pattern: "./module"},
	} {
		t.Run(invocation.name, func(t *testing.T) {
			built, stderr, code := runFixture(t, invocation.dir, "--coverage=statement", "--format=json", invocation.pattern)
			if code != ExitSuccess {
				t.Fatalf("single-module go.work exit=%d\nstderr:\n%s", code, stderr)
			}
			if built.Module != "example.test/workmodule" || built.Run.Results.Test != report.ResultPassed || built.Run.Results.Measurement != report.ResultPassed {
				t.Fatalf("single-module go.work report = module %q run %#v", built.Module, built.Run)
			}
		})
	}
}

func TestAlternateModFileAppliesSameSnapshotToAnalysisAndTest(t *testing.T) {
	for _, selection := range []struct {
		name             string
		goFlags          bool
		explicitOverride bool
	}{
		{name: "explicit"},
		{name: "GOFLAGS", goFlags: true},
		{name: "explicit-overrides-GOFLAGS", explicitOverride: true},
	} {
		t.Run(selection.name, func(t *testing.T) {
			root := t.TempDir()
			module := filepath.Join(root, "module")
			writeIntegrationFile(t, filepath.Join(module, "go.mod"), "module example.test/modfile\n\ngo 1.26\n")
			writeIntegrationFile(t, filepath.Join(module, "value.go"), "package modfile\n\nimport \"example.test/dependency\"\n\nfunc Value() bool { return dependency.Value() }\n")
			writeIntegrationFile(t, filepath.Join(module, "value_test.go"), "package modfile\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) { if !Value() { t.Fatal(\"false\") } }\n")
			writeIntegrationFile(t, filepath.Join(module, "dependency", "go.mod"), "module example.test/dependency\n\ngo 1.26\n")
			writeIntegrationFile(t, filepath.Join(module, "dependency", "value.go"), "package dependency\n\nfunc Value() bool { return true }\n")
			alternate := filepath.Join(root, "config", "analysis.mod")
			writeIntegrationFile(t, alternate, "module example.test/modfile\n\ngo 1.26\n\nrequire example.test/dependency v0.0.0\nreplace example.test/dependency => ./dependency\n")
			writeIntegrationFile(t, strings.TrimSuffix(alternate, ".mod")+".sum", "")
			t.Setenv("GOWORK", "off")
			arguments := []string{"--coverage=statement", "--format=json", "."}
			if selection.goFlags {
				t.Setenv("GOFLAGS", "-modfile="+alternate)
			} else {
				arguments = append(arguments, "--", "-modfile="+alternate)
				if selection.explicitOverride {
					environmentMod := filepath.Join(root, "config", "environment.mod")
					writeIntegrationFile(t, environmentMod, "module example.test/modfile\n\ngo 1.26\n")
					t.Setenv("GOFLAGS", "-modfile="+environmentMod)
				}
			}

			built, stderr, code := runFixture(t, module, arguments...)
			if code != ExitSuccess {
				t.Fatalf("alternate modfile exit=%d\nstderr:\n%s", code, stderr)
			}
			if built.Module != "example.test/modfile" || built.Run.Results.Test != report.ResultPassed || built.Run.Results.Measurement != report.ResultPassed {
				t.Fatalf("alternate modfile report = module %q run %#v", built.Module, built.Run)
			}
		})
	}
}

func TestCleanupFailureIsPublishedInReportBeforeExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory removal permissions differ on Windows")
	}
	module := t.TempDir()
	workParent := t.TempDir()
	writeIntegrationFile(t, filepath.Join(module, "go.mod"), "module example.test/cleanup\n\ngo 1.26\n")
	writeIntegrationFile(t, filepath.Join(module, "value.go"), "package cleanup\n\nfunc Value() bool { return true }\n")
	writeIntegrationFile(t, filepath.Join(module, "value_test.go"), `package cleanup

import (
	"os"
	"testing"
)

func TestValue(t *testing.T) {
	if !Value() { t.Fatal("false") }
	if err := os.Chmod(os.Getenv("GOMCDC_CLEANUP_PARENT"), 0o500); err != nil { t.Fatal(err) }
}
`)
	t.Setenv("GOWORK", "off")
	t.Setenv("GOMCDC_CLEANUP_PARENT", workParent)
	var stdout, stderr bytes.Buffer
	code := runAt(t.Context(), module, []string{
		"test", "--coverage=statement", "--format=json", "--workdir=" + workParent, ".",
	}, &stdout, &stderr)
	if err := os.Chmod(workParent, 0o700); err != nil {
		t.Fatal(err)
	}
	var built report.Report
	if err := json.Unmarshal(stdout.Bytes(), &built); err != nil {
		t.Fatalf("decode cleanup-failure report (exit %d): %v\nstdout:\n%s\nstderr:\n%s", code, err, stdout.String(), stderr.String())
	}
	if code != ExitMeasurementFailed {
		t.Fatalf("cleanup failure exit=%d, want %d\nstderr:\n%s", code, ExitMeasurementFailed, stderr.String())
	}
	if built.Run.Results.Test != report.ResultPassed || built.Run.Results.Measurement != report.ResultFailed || built.Run.Complete {
		t.Fatalf("cleanup failure run = %#v", built.Run)
	}
	found := false
	for _, reportErr := range built.Errors {
		if reportErr.Code == "workspace-cleanup-failed" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("cleanup failure missing from report errors: %#v", built.Errors)
	}
}

func TestIntegratedFixtureWritesPackageCenteredHTML(t *testing.T) {
	configureIntegrationEnvironment(t)
	root := fixturePath(t, "integration")
	output := filepath.Join(t.TempDir(), "coverage-html")
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	code := runAt(ctx, root, []string{"test", "--timeout=2m", "--format=html", "--output=" + output, "./..."}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("HTML exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("HTML report unexpectedly wrote stdout: %q", stdout.String())
	}
	contents, err := os.ReadFile(filepath.Join(output, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range [][]byte{[]byte("Package navigation"), []byte("Evidence producers"), []byte("compiler-selection"), []byte("accepted"), []byte("example.test/gomcdc-fixture/allow"), []byte("allow/allow.go"), []byte("Allow"), []byte("Original source"), []byte("source-code"), []byte("metric-condition"), []byte("No-match selection"), []byte("a &amp;&amp; b"), []byte("UC MC/DC"), []byte("Mask MC/DC"), []byte("Masking witness")} {
		if !bytes.Contains(contents, required) {
			t.Errorf("HTML missing %q", required)
		}
	}
	if bytes.Contains(contents, []byte("<script")) || bytes.Contains(contents, []byte("src=\"http")) {
		t.Fatal("HTML report must not execute scripts or load external resources")
	}
}

func TestIntegratedFixtureReportsAllMetricsAcrossPackages(t *testing.T) {
	configureIntegrationEnvironment(t)
	markerDir := t.TempDir()
	t.Setenv("GOMCDC_EXECUTION_MARKER_DIR", markerDir)
	root := fixturePath(t, "integration")

	all, allStderr, code := runFixture(t, root, "--format=json", "./...")
	if code != ExitSuccess {
		t.Fatalf("all-metric exit = %d\nstderr:\n%s", code, allStderr)
	}
	if all.SchemaVersion != report.SchemaVersion || all.ToolVersion == "" || all.Module != "example.test/gomcdc-fixture" {
		t.Fatalf("report identity = schema %q tool %q module %q", all.SchemaVersion, all.ToolVersion, all.Module)
	}
	if all.Run.Status != cover.RunPassed || !all.Run.Complete {
		t.Fatalf("run = %#v", all.Run)
	}
	if all.Run.Results != (report.RunResults{
		Test: report.ResultPassed, Measurement: report.ResultPassed, Integrity: report.ResultPassed,
		Strict: report.ResultNotRequested, Threshold: report.ResultNotRequested,
	}) {
		t.Fatalf("independent run results = %#v", all.Run.Results)
	}
	if len(all.Errors) != 0 {
		t.Fatalf("successful report errors = %#v", all.Errors)
	}
	if all.MeasurementMode != report.MeasurementSingleRun || len(all.Measurements) != 1 || all.Measurements[0].Name != "combined" {
		t.Fatalf("single measurement provenance = mode %q runs %#v", all.MeasurementMode, all.Measurements)
	}
	if len(all.ProducerOutcomes) != 3 {
		t.Fatalf("producer outcomes = %#v, want three combined producers", all.ProducerOutcomes)
	}
	for _, producer := range []report.ProducerName{
		report.ProducerGoCover,
		report.ProducerASTRuntime,
		report.ProducerCompilerSelection,
	} {
		outcome := findProducerOutcome(t, all, producer)
		if outcome.Integrity != report.ProducerIntegrityValid ||
			outcome.Completeness != report.ProducerCompletenessComplete ||
			outcome.Mapping != report.ProducerMappingComplete ||
			outcome.Usability != report.ProducerUsabilityAccepted {
			t.Fatalf("complete producer %q outcome = %#v", producer, outcome)
		}
	}
	for _, packageName := range []string{"allow", "consumer", "routing"} {
		if _, err := os.Stat(filepath.Join(markerDir, packageName)); err != nil {
			t.Fatalf("package %q did not record its single test-binary execution: %v", packageName, err)
		}
	}
	t.Setenv("GOMCDC_EXECUTION_MARKER_DIR", "")
	for _, measurement := range all.Measurements {
		if len(measurement.Packages) != 4 {
			t.Fatalf("measurement %q package statuses = %#v, want 4 packages", measurement.Name, measurement.Packages)
		}
		if got := measurement.Packages["example.test/gomcdc-fixture/shared"]; got != string(gotest.PackageSkipped) {
			t.Fatalf("measurement %q shared package status = %q, want %q", measurement.Name, got, gotest.PackageSkipped)
		}
	}
	if all.Capabilities.Status(backend.CapabilityDirectCaseSelection) != backend.CapabilitySupported || all.Instrumentation.Total.Discovered == 0 || all.Instrumentation.HasGaps() {
		t.Fatalf("backend capability/instrumentation accounting = capabilities %#v instrumentation %#v", all.Capabilities, all.Instrumentation)
	}
	if len(all.Packages) != 4 {
		t.Fatalf("packages = %d, want 4", len(all.Packages))
	}
	for name, metric := range reportMetrics(all.Summary) {
		if !metric.Enabled {
			t.Fatalf("default all metric %s = %#v", name, metric)
		}
		if metric.Total == 0 {
			t.Fatalf("default all metric %s has empty denominator: %#v", name, metric)
		}
	}
	assertPackageSums(t, all)
	shared := findPackage(t, all, "example.test/gomcdc-fixture/shared")
	if shared.Summary.Statement.Covered == 0 || shared.Summary.Function.Covered == 0 || shared.Summary.Decision.Covered != shared.Summary.Decision.Total {
		t.Fatalf("cross-package calls did not align C0 and AST scope: %#v", shared.Summary)
	}

	allow := findDecisionInFunction(t, all, "Allow", "a && b")
	if len(allow.DecisionID) != 18 || !strings.HasPrefix(allow.DecisionID, "0x") {
		t.Fatalf("Allow decision ID = %q", allow.DecisionID)
	}
	conditionIDs := make(map[string]struct{}, len(allow.Conditions))
	for _, condition := range allow.Conditions {
		if len(condition.ConditionID) != 18 || !strings.HasPrefix(condition.ConditionID, "0x") {
			t.Fatalf("condition ID = %q", condition.ConditionID)
		}
		if _, duplicate := conditionIDs[condition.ConditionID]; duplicate {
			t.Fatalf("duplicate condition ID = %q", condition.ConditionID)
		}
		conditionIDs[condition.ConditionID] = struct{}{}
	}
	if !allow.DecisionCoverage.True || !allow.DecisionCoverage.False {
		t.Fatalf("Allow decision outcomes = %#v", allow.DecisionCoverage)
	}
	if got := allow.Conditions[1].NotEvaluated; got == 0 {
		t.Fatalf("short-circuited b not-evaluated count = %d", got)
	}
	if allow.Conditions[0].MCDCUnique.Status != string(cover.CoverageInfeasible) ||
		allow.Conditions[0].MCDCMasking.Status != string(cover.CoverageCovered) {
		t.Fatalf("Allow(a) MC/DC unique=%q masking=%q", allow.Conditions[0].MCDCUnique.Status, allow.Conditions[0].MCDCMasking.Status)
	}
	if allow.Conditions[0].MCDCMasking.Witness == nil {
		t.Fatal("masking MC/DC witness is missing")
	}
	for _, packageReport := range all.Packages {
		for _, file := range packageReport.Files {
			if file.Path == "allow/generated.go" {
				t.Fatalf("generated source was retained in the target set: %#v", file)
			}
		}
	}
	initFunctions := 0
	for _, function := range findFile(t, all, "allow/init.go").Functions {
		if function.Name == "init" {
			initFunctions++
			if len(function.Decisions) != 1 {
				t.Fatalf("init function at %#v has %d decisions, want 1", function.Location, len(function.Decisions))
			}
		}
	}
	if initFunctions != 2 {
		t.Fatalf("distinct init functions = %d, want 2", initFunctions)
	}
	skippedCase := findDecision(t, all, "b && c")
	if skippedCase.NotEvaluated == 0 || skippedCase.Conditions[0].NotEvaluated == 0 || skippedCase.Conditions[1].NotEvaluated == 0 {
		t.Fatalf("conditionless-switch skipped decision evidence = %#v", skippedCase)
	}
	multiC := findDecisionInFunction(t, all, "MultiConditional", "c")
	multiNotA := findDecisionInFunction(t, all, "MultiConditional", "!a")
	if multiC.NotEvaluated == 0 || multiNotA.NotEvaluated < 2 {
		t.Fatalf("multi-expression conditionless-switch skip suffix: c=%#v !a=%#v", multiC, multiNotA)
	}

	if !hasAbortedEvaluation(all) {
		t.Fatal("panicking condition did not produce an aborted evaluation")
	}
	goexit := findDecisionInFunction(t, all, "GoexitDecision", "goexitPredicate()")
	if !hasEvaluationStatus(goexit, "aborted") {
		t.Fatalf("runtime.Goexit condition did not produce an aborted evaluation: %#v", goexit.Evaluations)
	}
	if !hasCoveredSelectClause(all) {
		t.Fatal("select clause body coverage is missing")
	}
	expressionSwitch := findFunction(t, all, "ExpressionSwitch")
	if len(expressionSwitch.Clauses) != 3 {
		t.Fatalf("ExpressionSwitch clauses = %#v", expressionSwitch.Clauses)
	}
	fallthroughOnly := expressionSwitch.Clauses[1]
	if fallthroughOnly.BodyCoverage.Covered != 1 || fallthroughOnly.SelectionCoverage.Covered != 0 || fallthroughOnly.DirectSelections != 0 {
		t.Fatalf("fallthrough was conflated with direct selection: %#v", fallthroughOnly)
	}
	noDefault := findFunction(t, all, "NoDefault")
	if got := noDefault.Clauses[0].SelectedAlternatives; len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Fatalf("expression case alternative evidence = %#v", got)
	}
	typeSwitch := findFunction(t, all, "TypeSwitch")
	if got := typeSwitch.Clauses[1].SelectedAlternatives; len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Fatalf("type case alternative evidence = %#v", got)
	}
	if len(noDefault.NoMatches) != 1 || noDefault.NoMatches[0].SelectionCoverage.Covered != 1 {
		t.Fatalf("no-match dispatch evidence = %#v", noDefault.NoMatches)
	}
	for _, packageReport := range all.Packages {
		for _, file := range packageReport.Files {
			if filepath.IsAbs(file.Path) || strings.Contains(file.Path, ".gomcdc") || strings.Contains(file.Path, "gomcdc-") {
				t.Fatalf("report leaked a generated/temporary file path %q", file.Path)
			}
			for _, function := range file.Functions {
				if function.Location != nil && function.Location.File != file.Path {
					t.Fatalf("function source location file = %q, want %q", function.Location.File, file.Path)
				}
				for _, statement := range function.Statements {
					if statement.Location.File != file.Path {
						t.Fatalf("statement source location file = %q, want %q", statement.Location.File, file.Path)
					}
				}
			}
		}
	}

	oracle := runV1DualRunOracle(t, root)
	if got, want := projectC0Semantics(all), projectC0Semantics(oracle.c0); !reflect.DeepEqual(got, want) {
		t.Fatalf("single-run C0 projection differs from v1 dual-run oracle: %s", firstSemanticDifference(reflect.ValueOf(got), reflect.ValueOf(want), "c0"))
	}
	if got, want := projectASTSemantics(all), projectASTSemantics(oracle.ast); !reflect.DeepEqual(got, want) {
		t.Fatalf("single-run AST projection differs from v1 dual-run oracle: %s", firstSemanticDifference(reflect.ValueOf(got), reflect.ValueOf(want), "ast"))
	}
}

func TestThresholdFailureHasDistinctExitCode(t *testing.T) {
	configureIntegrationEnvironment(t)
	root := fixturePath(t, "integration")
	built, stderr, code := runFixture(t, root, "--coverage=decision", "--include-tests", "--strict", "--fail-under-decision=100", "--format=json", "./...")
	if code != ExitCoverageThreshold {
		t.Fatalf("exit = %d, want %d\nstderr:\n%s", code, ExitCoverageThreshold, stderr)
	}
	if !strings.Contains(stderr, "decision") || !strings.Contains(stderr, "below 100.00%") {
		t.Fatalf("threshold diagnostic missing: %s", stderr)
	}
	if !hasTestSourceDecision(built) {
		t.Fatal("--include-tests did not add _test.go decisions")
	}
	if len(built.Errors) != 1 || built.Errors[0].Phase != "threshold" || built.Errors[0].Code != "coverage-threshold-failed" {
		t.Fatalf("threshold report errors = %#v", built.Errors)
	}
	if built.Run.Results.Test != report.ResultPassed || built.Run.Results.Measurement != report.ResultPassed ||
		built.Run.Results.Integrity != report.ResultPassed || built.Run.Results.Strict != report.ResultPassed ||
		built.Run.Results.Threshold != report.ResultFailed {
		t.Fatalf("threshold result axes = %#v", built.Run.Results)
	}
}

func TestCompilerAwareMeasurementSupportsWorkDirWithSpaces(t *testing.T) {
	configureIntegrationEnvironment(t)
	root := fixturePath(t, "integration")
	parent := filepath.Join(t.TempDir(), "workspace with spaces")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	built, stderr, code := runFixture(
		t,
		root,
		"--coverage=switch-clause-selection",
		"--workdir="+parent,
		"--format=json",
		"./routing",
	)
	if code != ExitSuccess {
		t.Fatalf("compiler-aware measurement with spaced workdir exit=%d\nstderr:\n%s", code, stderr)
	}
	if built.Summary.SwitchClauseSelection.Covered == 0 {
		t.Fatalf("compiler-aware selection evidence is missing: %#v", built.Summary.SwitchClauseSelection)
	}
}

func hasTestSourceDecision(built report.Report) bool {
	for _, packageReport := range built.Packages {
		for _, file := range packageReport.Files {
			if strings.HasSuffix(file.Path, "_test.go") {
				for _, function := range file.Functions {
					if len(function.Decisions) > 0 {
						return true
					}
				}
			}
		}
	}
	return false
}

func TestBuildFailureStillProducesPartialMultiPackageReport(t *testing.T) {
	configureIntegrationEnvironment(t)
	root := fixturePath(t, "partial")
	built, stderr, code := runFixture(
		t, root,
		"--strict", "--fail-under-switch-clause-selection=100", "--format=json", "./...",
	)
	if code != ExitMeasurementFailed {
		t.Fatalf("exit = %d, want measurement failure %d\nstderr:\n%s", code, ExitMeasurementFailed, stderr)
	}
	if built.Run.Status != cover.RunFailed || built.Run.FailureKind != cover.RunFailureBuild || built.Run.Complete {
		t.Fatalf("partial run = %#v", built.Run)
	}
	for _, producer := range []report.ProducerName{
		report.ProducerGoCover,
		report.ProducerASTRuntime,
		report.ProducerCompilerSelection,
	} {
		outcome := findProducerOutcome(t, built, producer)
		if outcome.Integrity != report.ProducerIntegrityValid ||
			outcome.Completeness != report.ProducerCompletenessPartial ||
			outcome.Mapping != report.ProducerMappingComplete ||
			outcome.Usability != report.ProducerUsabilityAcceptedPartial {
			t.Fatalf("partial producer %q outcome = %#v", producer, outcome)
		}
	}
	hasAnalysisError := false
	for _, reportError := range built.Errors {
		hasAnalysisError = hasAnalysisError || reportError.Phase == "analysis"
	}
	if !hasAnalysisError {
		t.Fatalf("partial report omitted its analysis error: %#v", built.Errors)
	}
	if built.Run.Results != (report.RunResults{
		Test: report.ResultFailed, Measurement: report.ResultFailed, Integrity: report.ResultPassed,
		Strict: report.ResultFailed, Threshold: report.ResultFailed,
	}) {
		t.Fatalf("combined measurement/strict/threshold results = %#v", built.Run.Results)
	}
	for _, code := range []string{"strict-coverage-gap", "coverage-threshold-failed"} {
		if !hasReportErrorCode(built, code) {
			t.Fatalf("partial report errors omit %q: %#v", code, built.Errors)
		}
	}
	good := findPackage(t, built, "example.test/gomcdc-partial/good")
	if !good.Evidence || good.Summary.Decision.Covered != good.Summary.Decision.Total {
		t.Fatalf("good package evidence/decision = %#v", good)
	}
	broken := findPackage(t, built, "example.test/gomcdc-partial/broken")
	if broken.Status != "build-failed" || broken.Evidence ||
		broken.Summary.Statement.Unknown == 0 || broken.Summary.Function.Unknown == 0 ||
		broken.Summary.Decision.Unknown != 2 || broken.Summary.Decision.Total != 0 {
		t.Fatalf("broken package = %#v", broken)
	}
	malformed := findPackage(t, built, "example.test/gomcdc-partial/malformed")
	if malformed.Status != "build-failed" || malformed.Evidence {
		t.Fatalf("malformed package = %#v", malformed)
	}
}

func TestFailedAndInterruptedRunsRetainEvidenceAndIndependentResults(t *testing.T) {
	configureIntegrationEnvironment(t)
	root := fixturePath(t, "failure")

	t.Run("test failure takes precedence over threshold", func(t *testing.T) {
		t.Setenv("GOMCDC_FAILURE_MODE", "fail")
		built, stderr, code := runFixture(
			t, root,
			"--coverage=statement,decision", "--strict", "--fail-under-decision=100", "--format=json", "./unstable",
		)
		if code != ExitTestsFailed {
			t.Fatalf("exit=%d, want %d\nstderr:\n%s", code, ExitTestsFailed, stderr)
		}
		wantResults := report.RunResults{
			Test: report.ResultFailed, Measurement: report.ResultPassed, Integrity: report.ResultPassed,
			Strict: report.ResultPassed, Threshold: report.ResultFailed,
		}
		if built.Run.Status != cover.RunFailed || built.Run.FailureKind != cover.RunFailureTest || built.Run.Complete || built.Run.Results != wantResults {
			t.Fatalf("failed run = %#v", built.Run)
		}
		assertCombinedFailureEvidence(t, built, report.ProducerIntegrityValid, report.ProducerUsabilityAcceptedPartial, true)
		assertPartialDecisionEvidence(t, built)
		for _, code := range []string{"go-test-test", "coverage-threshold-failed"} {
			if !hasReportErrorCode(built, code) {
				t.Fatalf("report errors omit %q: %#v", code, built.Errors)
			}
		}
	})

	t.Run("truncated tail is recoverable after test failure", func(t *testing.T) {
		t.Setenv("GOMCDC_FAILURE_MODE", "truncate")
		built, stderr, code := runFixture(t, root, "--coverage=statement,decision", "--format=json", "./unstable")
		if code != ExitTestsFailed {
			t.Fatalf("exit=%d, want %d\nstderr:\n%s", code, ExitTestsFailed, stderr)
		}
		if built.Run.Results.Test != report.ResultFailed || built.Run.Results.Integrity != report.ResultPassed {
			t.Fatalf("truncated-tail results = %#v", built.Run.Results)
		}
		assertCombinedFailureEvidence(t, built, report.ProducerIntegrityValidPrefix, report.ProducerUsabilityAcceptedPartial, true)
		assertPartialDecisionEvidence(t, built)
		if !hasReportErrorCode(built, "runtime-recoverable-interruption") {
			t.Fatalf("recoverable diagnostic is missing: %#v", built.Errors)
		}
	})

	t.Run("integrity failure takes precedence over test and threshold", func(t *testing.T) {
		t.Setenv("GOMCDC_FAILURE_MODE", "corrupt")
		built, stderr, code := runFixture(
			t, root,
			"--coverage=statement,decision", "--fail-under-decision=100", "--format=json", "./unstable",
		)
		if code != ExitMeasurementFailed {
			t.Fatalf("exit=%d, want %d\nstderr:\n%s", code, ExitMeasurementFailed, stderr)
		}
		wantResults := report.RunResults{
			Test: report.ResultFailed, Measurement: report.ResultPassed, Integrity: report.ResultFailed,
			Strict: report.ResultNotRequested, Threshold: report.ResultFailed,
		}
		if built.Run.Results != wantResults {
			t.Fatalf("integrity-failure results = %#v", built.Run.Results)
		}
		assertCombinedFailureEvidence(t, built, report.ProducerIntegrityInvalid, report.ProducerUsabilityRejected, true)
		if built.Summary.Decision.Covered != 0 || built.Summary.Decision.Total != 0 || built.Summary.Decision.Unknown != 2 {
			t.Fatalf("corrupt evidence was reported as coverage: %#v", built.Summary.Decision)
		}
		for _, code := range []string{"runtime-integrity-error", "go-test-test", "coverage-threshold-failed"} {
			if !hasReportErrorCode(built, code) {
				t.Fatalf("report errors omit %q: %#v", code, built.Errors)
			}
		}
	})

	t.Run("timeout remains distinct from ordinary test failure", func(t *testing.T) {
		t.Setenv("GOMCDC_FAILURE_MODE", "timeout")
		built, stderr, code := runFixture(
			t, root,
			"--coverage=statement,decision", "--strict", "--fail-under-decision=100", "--format=json", "./unstable",
			"--", "-test.timeout=250ms",
		)
		if code != ExitTestsFailed {
			t.Fatalf("exit=%d, want %d\nstderr:\n%s", code, ExitTestsFailed, stderr)
		}
		wantResults := report.RunResults{
			Test: report.ResultTimeout, Measurement: report.ResultPassed, Integrity: report.ResultPassed,
			Strict: report.ResultPassed, Threshold: report.ResultFailed,
		}
		if built.Run.Status != cover.RunTimeout || built.Run.FailureKind != cover.RunFailureTimeout || built.Run.Results != wantResults {
			t.Fatalf("timeout run = %#v", built.Run)
		}
		assertCombinedFailureEvidence(t, built, report.ProducerIntegrityValid, report.ProducerUsabilityAcceptedPartial, true)
		assertPartialDecisionEvidence(t, built)
		if !hasReportErrorCode(built, "go-test-timeout") {
			t.Fatalf("timeout error is missing: %#v", built.Errors)
		}
	})

	t.Run("panic retains accepted evidence", func(t *testing.T) {
		t.Setenv("GOMCDC_FAILURE_MODE", "panic")
		built, stderr, code := runFixture(t, root, "--coverage=statement,decision", "--format=json", "./unstable")
		if code != ExitTestsFailed {
			t.Fatalf("exit=%d, want %d\nstderr:\n%s", code, ExitTestsFailed, stderr)
		}
		if built.Run.Status != cover.RunFailed || built.Run.FailureKind != cover.RunFailureTest || built.Run.Complete {
			t.Fatalf("panic run = %#v", built.Run)
		}
		assertCombinedFailureEvidence(t, built, report.ProducerIntegrityValid, report.ProducerUsabilityAcceptedPartial, false)
		assertPartialDecisionEvidence(t, built)
	})

	t.Run("caller interruption retains accepted AST prefix", func(t *testing.T) {
		t.Setenv("GOMCDC_FAILURE_MODE", "interrupt")
		marker := filepath.Join(t.TempDir(), "ready")
		t.Setenv("GOMCDC_INTERRUPT_MARKER", marker)
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		canceled := make(chan struct{})
		go func() {
			defer close(canceled)
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				if _, err := os.Stat(marker); err == nil {
					cancel()
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
		var stdout, stderr bytes.Buffer
		code := runAt(ctx, root, []string{
			"test", "--timeout=2m", "--coverage=statement,decision", "--format=json", "./unstable",
		}, &stdout, &stderr)
		<-canceled
		var built report.Report
		if err := json.Unmarshal(stdout.Bytes(), &built); err != nil {
			t.Fatalf("decode interrupted report (exit %d): %v\nstdout:\n%s\nstderr:\n%s", code, err, stdout.String(), stderr.String())
		}
		if code != ExitInterrupted {
			t.Fatalf("exit=%d, want %d\nstderr:\n%s", code, ExitInterrupted, stderr.String())
		}
		if built.Run.Status != cover.RunFailed || built.Run.FailureKind != cover.RunFailureInterrupted || built.Run.Complete {
			t.Fatalf("interrupted run = %#v", built.Run)
		}
		if built.MeasurementMode != report.MeasurementSingleRun || len(built.Measurements) != 1 || built.Measurements[0].Name != "combined" {
			t.Fatalf("interrupted measurement provenance = %#v", built.Measurements)
		}
		astOutcome := findProducerOutcome(t, built, report.ProducerASTRuntime)
		if astOutcome.Mapping != report.ProducerMappingComplete || astOutcome.Usability != report.ProducerUsabilityAcceptedPartial {
			t.Fatalf("interrupted AST outcome = %#v", astOutcome)
		}
		assertPartialDecisionEvidence(t, built)
	})
}

func assertCombinedFailureEvidence(
	t *testing.T,
	built report.Report,
	astIntegrity report.ProducerIntegrity,
	astUsability report.ProducerUsability,
	wantC0Evidence bool,
) {
	t.Helper()
	if built.MeasurementMode != report.MeasurementSingleRun || len(built.Measurements) != 1 || built.Measurements[0].Name != "combined" {
		t.Fatalf("combined measurement provenance = mode %q runs %#v", built.MeasurementMode, built.Measurements)
	}
	goCover := findProducerOutcome(t, built, report.ProducerGoCover)
	if goCover != (report.ProducerOutcome{
		Producer: report.ProducerGoCover, Integrity: report.ProducerIntegrityValid,
		Completeness: report.ProducerCompletenessPartial, Mapping: report.ProducerMappingComplete,
		Usability: report.ProducerUsabilityAcceptedPartial,
	}) {
		t.Fatalf("partial Go cover outcome = %#v", goCover)
	}
	ast := findProducerOutcome(t, built, report.ProducerASTRuntime)
	if ast != (report.ProducerOutcome{
		Producer: report.ProducerASTRuntime, Integrity: astIntegrity,
		Completeness: report.ProducerCompletenessPartial, Mapping: report.ProducerMappingComplete,
		Usability: astUsability,
	}) {
		t.Fatalf("partial AST outcome = %#v", ast)
	}
	if wantC0Evidence {
		if built.Summary.Statement.Covered == 0 || built.Summary.Statement.Total == 0 || built.Summary.Statement.Unknown != 0 {
			t.Fatalf("partial Go cover evidence = %#v", built.Summary.Statement)
		}
	} else if built.Summary.Statement.Unknown == 0 {
		t.Fatalf("missing partial Go cover evidence was not reported unknown: %#v", built.Summary.Statement)
	}
}

func assertPartialDecisionEvidence(t *testing.T, built report.Report) {
	t.Helper()
	if built.Summary.Decision.Covered != 1 || built.Summary.Decision.Total != 2 || built.Summary.Decision.Unknown != 0 {
		t.Fatalf("partial decision evidence = %#v", built.Summary.Decision)
	}
}

func hasReportErrorCode(built report.Report, code string) bool {
	for _, item := range built.Errors {
		if item.Code == code {
			return true
		}
	}
	return false
}

func TestEvidenceVerificationDropsImpossibleCompletedEvidence(t *testing.T) {
	t.Parallel()
	decision := cover.DecisionMetadata{
		ID: 1, Package: "example.test/p",
		Conditions: []cover.ConditionMetadata{{Index: 0}, {Index: 1}},
		ExpressionTree: cover.NewAndExpression(
			cover.NewConditionExpression(0),
			cover.NewConditionExpression(1),
		),
	}
	recorded := runtimecov.RecordedEvidence{Evaluations: []cover.DecisionEvaluation{{
		DecisionID: 1, EvaluationID: 1, RunID: "run", PackagePath: "example.test/p", ProcessID: 1,
		Conditions: []cover.ConditionState{cover.ConditionFalse, cover.ConditionTrue},
		Result:     false, Status: cover.EvaluationCompleted,
	}}}
	validated, err := acceptRuntimeEvidence(t.Context(), []cover.DecisionMetadata{decision}, nil, recorded, "run", nil)
	if err == nil || len(validated.Evaluations) != 0 {
		t.Fatalf("validated=%#v err=%v; impossible vector became coverage evidence", validated, err)
	}
}

func TestEvidenceVerificationKeepsConditionlessSwitchNotEvaluatedEvidence(t *testing.T) {
	t.Parallel()
	first := cover.DecisionMetadata{
		ID: 1, Package: "example.test/p", Function: "Choose", Kind: cover.DecisionSwitchCase,
		Location:   cover.SourceLocation{File: "p.go", Start: cover.Position{Line: 3, Column: 7}, End: cover.Position{Line: 3, Column: 8}},
		Conditions: []cover.ConditionMetadata{{Index: 0}}, ExpressionTree: cover.NewConditionExpression(0),
	}
	second := first
	second.ID = 2
	second.Location.Start.Line, second.Location.End.Line = 4, 4
	evaluation := cover.DecisionEvaluation{
		DecisionID: 1, EvaluationID: 9, RunID: "run", PackagePath: "example.test/p", ProcessID: 12,
		TestID: cover.UnknownTestID, Conditions: []cover.ConditionState{cover.ConditionTrue}, Result: true, Status: cover.EvaluationCompleted,
	}
	observation := cover.DecisionNotEvaluatedObservation{
		DecisionID: 2, CauseDecisionID: 1, CauseEvaluationID: 9,
		RunID: "run", PackagePath: "example.test/p", ProcessID: 12,
	}
	clauses := []cover.ClauseMetadata{
		{ID: 10, Package: "example.test/p", GroupID: 100, Kind: cover.ClauseConditionlessSwitch, Role: cover.ClauseCase, Index: 0, DecisionIDs: []cover.DecisionID{1}},
		{ID: 11, Package: "example.test/p", GroupID: 100, Kind: cover.ClauseConditionlessSwitch, Role: cover.ClauseCase, Index: 1, DecisionIDs: []cover.DecisionID{2}},
	}
	validated, err := acceptRuntimeEvidence(t.Context(),
		[]cover.DecisionMetadata{first, second},
		clauses,
		runtimecov.RecordedEvidence{Evaluations: []cover.DecisionEvaluation{evaluation}, NotEvaluatedDecisions: []cover.DecisionNotEvaluatedObservation{observation}},
		"run",
		nil,
	)
	if err != nil || len(validated.NotEvaluatedDecisions) != 1 {
		t.Fatalf("validated=%#v err=%v", validated, err)
	}
	withoutSuffix, err := acceptRuntimeEvidence(t.Context(),
		[]cover.DecisionMetadata{first, second},
		clauses,
		runtimecov.RecordedEvidence{Evaluations: []cover.DecisionEvaluation{evaluation}},
		"run",
		nil,
	)
	if err == nil || len(withoutSuffix.NotEvaluatedDecisions) != 0 {
		t.Fatalf("missing complete skip suffix was accepted: validated=%#v err=%v", withoutSuffix, err)
	}
}

func TestEvidenceVerificationRejectsInvalidEvaluationIdentityAndShape(t *testing.T) {
	t.Parallel()
	metadata := cover.DecisionMetadata{
		ID: 1, Package: "example.test/p", Kind: cover.DecisionIf,
		Conditions:     []cover.ConditionMetadata{{Index: 0}},
		ExpressionTree: cover.NewConditionExpression(0),
	}
	validEvaluation := func() cover.DecisionEvaluation {
		return cover.DecisionEvaluation{
			DecisionID: 1, EvaluationID: 7, RunID: "run", PackagePath: "example.test/p", ProcessID: 9,
			Conditions: []cover.ConditionState{cover.ConditionTrue}, Result: true, Status: cover.EvaluationCompleted,
		}
	}
	for _, test := range []struct {
		name    string
		mutate  func(*cover.DecisionEvaluation)
		wantErr string
	}{
		{name: "valid"},
		{name: "unknown decision", mutate: func(value *cover.DecisionEvaluation) { value.DecisionID = 99 }, wantErr: "unknown decision ID"},
		{name: "zero evaluation ID", mutate: func(value *cover.DecisionEvaluation) { value.EvaluationID = 0 }, wantErr: "reserved evaluation ID zero"},
		{name: "wrong run", mutate: func(value *cover.DecisionEvaluation) { value.RunID = "other" }, wantErr: "unexpected run"},
		{name: "wrong package", mutate: func(value *cover.DecisionEvaluation) { value.PackagePath = "example.test/other" }, wantErr: "belongs to package"},
		{name: "zero process", mutate: func(value *cover.DecisionEvaluation) { value.ProcessID = 0 }, wantErr: "invalid process provenance"},
		{name: "wrong condition count", mutate: func(value *cover.DecisionEvaluation) {
			value.Conditions = append(value.Conditions, cover.ConditionFalse)
		}, wantErr: "condition states"},
	} {
		t.Run(test.name, func(t *testing.T) {
			evaluation := validEvaluation()
			if test.mutate != nil {
				test.mutate(&evaluation)
			}
			validated, err := acceptRuntimeEvidence(t.Context(),
				[]cover.DecisionMetadata{metadata}, nil,
				runtimecov.RecordedEvidence{Evaluations: []cover.DecisionEvaluation{evaluation}}, "run", nil,
			)
			if test.wantErr == "" {
				if err != nil || len(validated.Evaluations) != 1 {
					t.Fatalf("valid evaluation rejected: validated=%#v err=%v", validated, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) || len(validated.Evaluations) != 0 {
				t.Fatalf("validated=%#v err=%v, want rejection containing %q", validated, err, test.wantErr)
			}
		})
	}
}

func TestEvaluationEvidenceIsVerifiedBeforeSemanticDeduplication(t *testing.T) {
	t.Parallel()
	metadata := cover.DecisionMetadata{
		ID: 1, Package: "example.test/p", Kind: cover.DecisionIf,
		Conditions: []cover.ConditionMetadata{{Index: 0}}, ExpressionTree: cover.NewConditionExpression(0),
	}
	valid := cover.DecisionEvaluation{
		DecisionID: 1, EvaluationID: 7, RunID: "run", PackagePath: "example.test/p", ProcessID: 9,
		TestID: cover.UnknownTestID, Conditions: []cover.ConditionState{cover.ConditionTrue},
		Result: true, Status: cover.EvaluationCompleted,
	}
	invalidDuplicate := valid
	invalidDuplicate.EvaluationID = 8
	invalidDuplicate.ProcessID = 0
	validDuplicate := valid
	validDuplicate.EvaluationID = 9
	validDuplicate.ProcessID = 10
	validDuplicate.TestID = "TestNamed"

	accepted, err := acceptRuntimeEvidence(t.Context(),
		[]cover.DecisionMetadata{metadata}, nil,
		runtimecov.RecordedEvidence{Evaluations: []cover.DecisionEvaluation{valid, invalidDuplicate, validDuplicate}},
		"run", nil,
	)
	if err == nil || !strings.Contains(err.Error(), "invalid process provenance") {
		t.Fatalf("invalid duplicate provenance was hidden: accepted=%#v err=%v", accepted, err)
	}
	if len(accepted.Evaluations) != 1 || accepted.Evaluations[0].TestID != "TestNamed" {
		t.Fatalf("valid duplicates were not projected idempotently: %#v", accepted.Evaluations)
	}
}

func TestEvidenceVerificationRejectsInvalidConditionlessSwitchSkipEvidence(t *testing.T) {
	t.Parallel()
	decision := func(id cover.DecisionID, line int) cover.DecisionMetadata {
		return cover.DecisionMetadata{
			ID: id, Package: "example.test/p", Function: "Choose", Kind: cover.DecisionSwitchCase,
			Location:   cover.SourceLocation{File: "p.go", Start: cover.Position{Line: line, Column: 7}, End: cover.Position{Line: line, Column: 8}},
			Conditions: []cover.ConditionMetadata{{Index: 0}}, ExpressionTree: cover.NewConditionExpression(0),
		}
	}
	base := func() ([]cover.DecisionMetadata, []cover.ClauseMetadata, runtimecov.RecordedEvidence) {
		decisions := []cover.DecisionMetadata{decision(1, 3), decision(2, 4), decision(3, 5)}
		clauses := []cover.ClauseMetadata{
			{ID: 10, Package: "example.test/p", GroupID: 100, Kind: cover.ClauseConditionlessSwitch, Role: cover.ClauseCase, Index: 0, DecisionIDs: []cover.DecisionID{1}},
			{ID: 11, Package: "example.test/p", GroupID: 100, Kind: cover.ClauseConditionlessSwitch, Role: cover.ClauseCase, Index: 1, DecisionIDs: []cover.DecisionID{2}},
			{ID: 12, Package: "example.test/p", GroupID: 100, Kind: cover.ClauseConditionlessSwitch, Role: cover.ClauseCase, Index: 2, DecisionIDs: []cover.DecisionID{3}},
		}
		evaluation := cover.DecisionEvaluation{
			DecisionID: 1, EvaluationID: 9, RunID: "run", PackagePath: "example.test/p", ProcessID: 12,
			Conditions: []cover.ConditionState{cover.ConditionTrue}, Result: true, Status: cover.EvaluationCompleted,
		}
		observation := func(target cover.DecisionID) cover.DecisionNotEvaluatedObservation {
			return cover.DecisionNotEvaluatedObservation{
				DecisionID: target, CauseDecisionID: 1, CauseEvaluationID: 9,
				RunID: "run", PackagePath: "example.test/p", ProcessID: 12,
			}
		}
		return decisions, clauses, runtimecov.RecordedEvidence{
			Evaluations:           []cover.DecisionEvaluation{evaluation},
			NotEvaluatedDecisions: []cover.DecisionNotEvaluatedObservation{observation(2), observation(3)},
		}
	}
	for _, test := range []struct {
		name    string
		mutate  func([]cover.DecisionMetadata, []cover.ClauseMetadata, *runtimecov.RecordedEvidence)
		wantErr string
	}{
		{name: "valid complete suffix"},
		{name: "unknown target", mutate: func(_ []cover.DecisionMetadata, _ []cover.ClauseMetadata, collection *runtimecov.RecordedEvidence) {
			collection.NotEvaluatedDecisions[0].DecisionID = 99
		}, wantErr: "unknown skipped decision ID"},
		{name: "unknown cause", mutate: func(_ []cover.DecisionMetadata, _ []cover.ClauseMetadata, collection *runtimecov.RecordedEvidence) {
			collection.NotEvaluatedDecisions[0].CauseDecisionID = 99
		}, wantErr: "unknown skip-cause decision ID"},
		{name: "wrong provenance", mutate: func(_ []cover.DecisionMetadata, _ []cover.ClauseMetadata, collection *runtimecov.RecordedEvidence) {
			collection.NotEvaluatedDecisions[0].RunID = "other"
		}, wantErr: "inconsistent run or package provenance"},
		{name: "non-switch target", mutate: func(decisions []cover.DecisionMetadata, _ []cover.ClauseMetadata, _ *runtimecov.RecordedEvidence) {
			decisions[1].Kind = cover.DecisionIf
		}, wantErr: "is not a conditionless-switch decision"},
		{name: "missing cause evaluation", mutate: func(_ []cover.DecisionMetadata, _ []cover.ClauseMetadata, collection *runtimecov.RecordedEvidence) {
			collection.Evaluations = nil
		}, wantErr: "no completed true cause evaluation"},
		{name: "cause is not before target", mutate: func(_ []cover.DecisionMetadata, _ []cover.ClauseMetadata, collection *runtimecov.RecordedEvidence) {
			collection.NotEvaluatedDecisions[0].DecisionID = 1
		}, wantErr: "is not later in the same conditionless switch"},
		{name: "duplicate skipped decision", mutate: func(_ []cover.DecisionMetadata, _ []cover.ClauseMetadata, collection *runtimecov.RecordedEvidence) {
			collection.NotEvaluatedDecisions = append(collection.NotEvaluatedDecisions, collection.NotEvaluatedDecisions[0])
		}, wantErr: "duplicate skipped-decision evidence"},
		{name: "incomplete suffix", mutate: func(_ []cover.DecisionMetadata, _ []cover.ClauseMetadata, collection *runtimecov.RecordedEvidence) {
			collection.NotEvaluatedDecisions = collection.NotEvaluatedDecisions[:1]
		}, wantErr: "want complete suffix"},
	} {
		t.Run(test.name, func(t *testing.T) {
			decisions, clauses, collection := base()
			if test.mutate != nil {
				test.mutate(decisions, clauses, &collection)
			}
			validated, err := acceptRuntimeEvidence(t.Context(), decisions, clauses, collection, "run", nil)
			if test.wantErr == "" {
				if err != nil || len(validated.NotEvaluatedDecisions) != 2 {
					t.Fatalf("valid skip suffix rejected: validated=%#v err=%v", validated, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) || len(validated.NotEvaluatedDecisions) != 0 {
				t.Fatalf("validated=%#v err=%v, want rejection containing %q", validated, err, test.wantErr)
			}
		})
	}
}

func TestEvidenceVerificationRejectsInvalidClauseEvidence(t *testing.T) {
	t.Parallel()
	clauses := []cover.ClauseMetadata{
		{ID: 10, Package: "example.test/p", SwitchID: 100, Kind: cover.ClauseExpressionSwitch, Role: cover.ClauseCase, Expressions: []string{"1", "2"}},
		{ID: 11, Package: "example.test/p", SwitchID: 100, Kind: cover.ClauseExpressionSwitch, Role: cover.ClauseDefault},
		{ID: 12, Package: "example.test/p", SwitchID: 200, Kind: cover.ClauseSelect, Role: cover.ClauseCase},
	}
	noMatches := []cover.NoMatchMetadata{{Package: "example.test/p", SwitchID: 300, Kind: cover.ClauseExpressionSwitch}}
	for _, test := range []struct {
		name        string
		observation cover.ClauseObservation
		wantErr     string
	}{
		{name: "valid direct selection", observation: cover.ClauseObservation{SwitchID: 100, ClauseID: 10, Event: cover.ClauseDirectSelection, AlternativeKnown: true, AlternativeIndex: 0}},
		{name: "valid no match", observation: cover.ClauseObservation{SwitchID: 300, Event: cover.ClauseNoMatchSelection}},
		{name: "unknown no-match switch", observation: cover.ClauseObservation{SwitchID: 999, Event: cover.ClauseNoMatchSelection}, wantErr: "unknown no-match switch ID"},
		{name: "no-match alternative", observation: cover.ClauseObservation{SwitchID: 300, Event: cover.ClauseNoMatchSelection, AlternativeKnown: true}, wantErr: "unknown no-match switch ID"},
		{name: "unknown clause", observation: cover.ClauseObservation{ClauseID: 99, Event: cover.ClauseBodyExecution}, wantErr: "unknown clause ID"},
		{name: "wrong switch", observation: cover.ClauseObservation{SwitchID: 999, ClauseID: 10, Event: cover.ClauseBodyExecution}, wantErr: "inconsistent switch ID"},
		{name: "direct selection missing switch", observation: cover.ClauseObservation{ClauseID: 10, Event: cover.ClauseDirectSelection, AlternativeKnown: true}, wantErr: "inconsistent switch ID"},
		{name: "body event alternative", observation: cover.ClauseObservation{ClauseID: 10, Event: cover.ClauseBodyExecution, AlternativeKnown: true}, wantErr: "body event carries a case alternative"},
		{name: "selection on select clause", observation: cover.ClauseObservation{ClauseID: 12, Event: cover.ClauseDirectSelection, AlternativeKnown: true}, wantErr: "cannot carry direct-selection evidence"},
		{name: "default alternative", observation: cover.ClauseObservation{SwitchID: 100, ClauseID: 11, Event: cover.ClauseDirectSelection, AlternativeKnown: true}, wantErr: "default clause"},
		{name: "case alternative missing", observation: cover.ClauseObservation{SwitchID: 100, ClauseID: 10, Event: cover.ClauseDirectSelection}, wantErr: "invalid case alternative"},
		{name: "case alternative out of range", observation: cover.ClauseObservation{SwitchID: 100, ClauseID: 10, Event: cover.ClauseDirectSelection, AlternativeKnown: true, AlternativeIndex: 2}, wantErr: "invalid case alternative"},
		{name: "unsupported event", observation: cover.ClauseObservation{ClauseID: 10, Event: cover.ClauseEventKind("invented")}, wantErr: "unsupported event"},
	} {
		t.Run(test.name, func(t *testing.T) {
			validated, err := acceptRuntimeEvidence(t.Context(),
				nil, clauses, runtimecov.RecordedEvidence{ClauseEvents: []runtimecov.RecordedClauseEvent{recordedClauseEvent(test.observation)}}, "run", noMatches,
			)
			if test.wantErr == "" {
				if err != nil || len(validated.ClauseObservations) != 1 {
					t.Fatalf("valid clause evidence rejected: validated=%#v err=%v", validated, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) || len(validated.ClauseObservations) != 0 {
				t.Fatalf("validated=%#v err=%v, want rejection containing %q", validated, err, test.wantErr)
			}
		})
	}
}

func recordedClauseEvent(observation cover.ClauseObservation) runtimecov.RecordedClauseEvent {
	return runtimecov.RecordedClauseEvent{
		RunID:            "run",
		PackagePath:      "example.test/p",
		ProcessID:        12,
		SwitchID:         observation.SwitchID,
		ClauseID:         observation.ClauseID,
		Event:            observation.Event,
		AlternativeIndex: observation.AlternativeIndex,
		AlternativeKnown: observation.AlternativeKnown,
	}
}

func TestClauseEvidenceRequiresValidProvenanceBeforeDeduplication(t *testing.T) {
	t.Parallel()
	metadata := []cover.ClauseMetadata{{
		ID: 10, Package: "example.test/p", SwitchID: 100,
		Kind: cover.ClauseExpressionSwitch, Role: cover.ClauseCase, Expressions: []string{"1"},
	}}
	valid := recordedClauseEvent(cover.ClauseObservation{
		SwitchID: 100, ClauseID: 10, Event: cover.ClauseDirectSelection,
		AlternativeKnown: true, AlternativeIndex: 0,
	})

	for _, test := range []struct {
		name    string
		mutate  func(*runtimecov.RecordedClauseEvent)
		wantErr string
	}{
		{name: "wrong run", mutate: func(event *runtimecov.RecordedClauseEvent) { event.RunID = "other" }, wantErr: "invalid provenance"},
		{name: "wrong package", mutate: func(event *runtimecov.RecordedClauseEvent) { event.PackagePath = "example.test/other" }, wantErr: "belongs to package"},
		{name: "zero process", mutate: func(event *runtimecov.RecordedClauseEvent) { event.ProcessID = 0 }, wantErr: "invalid provenance"},
	} {
		t.Run(test.name, func(t *testing.T) {
			event := valid
			test.mutate(&event)
			accepted, err := acceptRuntimeEvidence(t.Context(), nil, metadata, runtimecov.RecordedEvidence{ClauseEvents: []runtimecov.RecordedClauseEvent{event}}, "run", nil)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) || len(accepted.ClauseObservations) != 0 {
				t.Fatalf("accepted=%#v err=%v, want rejection containing %q", accepted, err, test.wantErr)
			}
		})
	}

	secondProcess := valid
	secondProcess.ProcessID++
	accepted, err := acceptRuntimeEvidence(t.Context(), nil, metadata, runtimecov.RecordedEvidence{ClauseEvents: []runtimecov.RecordedClauseEvent{valid, secondProcess}}, "run", nil)
	if err != nil || len(accepted.ClauseObservations) != 1 {
		t.Fatalf("valid cross-process duplicate was not projected idempotently: accepted=%#v err=%v", accepted, err)
	}
}

func TestProcessFileProvenanceIsVerified(t *testing.T) {
	t.Parallel()
	for _, file := range []runtimecov.ProcessFile{
		{Path: "/tmp/wrong-run", RunID: "other", PackagePath: "example.test/p", ProcessID: 1},
		{Path: "/tmp/missing-package", RunID: "run", ProcessID: 1},
		{Path: "/tmp/zero-process", RunID: "run", PackagePath: "example.test/p"},
		{Path: "/tmp/unknown-package", RunID: "run", PackagePath: "example.test/other", ProcessID: 1},
	} {
		accepted, err := acceptRuntimeEvidence(t.Context(), nil, nil, runtimecov.RecordedEvidence{Files: []runtimecov.ProcessFile{file}}, "run", nil)
		if err == nil || !strings.Contains(err.Error(), "invalid provenance") || len(accepted.Files) != 1 {
			t.Fatalf("file=%#v accepted=%#v err=%v", file, accepted, err)
		}
	}
	metadata := []cover.DecisionMetadata{{ID: 1, Package: "example.test/p"}}
	valid := runtimecov.ProcessFile{Path: "/tmp/valid", RunID: "run", PackagePath: "example.test/p", ProcessID: 1}
	if _, err := acceptRuntimeEvidence(t.Context(), metadata, nil, runtimecov.RecordedEvidence{Files: []runtimecov.ProcessFile{valid}}, "run", nil); err != nil {
		t.Fatalf("valid process file provenance was rejected: %v", err)
	}
}

func TestRejectsMeasurementOwnedFlagsFromExplicitFlagsAndGOFLAGS(t *testing.T) {
	for _, test := range []struct {
		name    string
		goFlags string
		args    []string
		want    string
	}{
		{name: "explicit-overlay", args: []string{"test", ".", "--", "-overlay=/tmp/overlay.json"}, want: "-overlay"},
		{name: "GOFLAGS-overlay", goFlags: `-tags=integration "-overlay=/tmp/a b.json"`, args: []string{"test", "."}, want: "-overlay"},
		{name: "explicit-toolexec", args: []string{"test", ".", "--", "-toolexec=/tmp/tool"}, want: "-toolexec"},
		{name: "GOFLAGS-toolexec", goFlags: "-toolexec=/tmp/tool", args: []string{"test", "."}, want: "-toolexec"},
		{name: "explicit-count", args: []string{"test", ".", "--", "-count=2"}, want: "-count"},
		{name: "GOFLAGS-coverprofile", goFlags: "-coverprofile=user.out", args: []string{"test", "."}, want: "-coverprofile"},
		{name: "explicit-json", args: []string{"test", ".", "--", "-json=false"}, want: "-json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("GOFLAGS", test.goFlags)
			var stdout, stderr bytes.Buffer
			code := runAt(t.Context(), t.TempDir(), test.args, &stdout, &stderr)
			if code != ExitInvalidUsage || !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestMeasurementRunRetainsPackageStatuses(t *testing.T) {
	t.Parallel()
	combined := &gotest.Result{
		Status: cover.RunPassed, FailureKind: cover.RunFailureNone,
		Packages: map[string]gotest.PackageStatus{"example.test/p": gotest.PackagePassed},
	}
	measurements := measurementRuns("combined", combined)
	if len(measurements) != 1 || measurements[0].Name != "combined" || measurements[0].Packages["example.test/p"] != "passed" {
		t.Fatalf("measurement package provenance = %#v", measurements)
	}
}

func TestRuntimeDiagnosticSeverityDistinguishesInterruptionFromCorruption(t *testing.T) {
	t.Parallel()
	failed := &gotest.Result{Status: cover.RunFailed}
	passed := &gotest.Result{Status: cover.RunPassed}
	recoverable := []runtimecov.Diagnostic{{Severity: runtimecov.DiagnosticRecoverable, Truncated: true, Message: "truncated final event record"}}
	corrupt := []runtimecov.Diagnostic{{Severity: runtimecov.DiagnosticIntegrity, Message: "decode event JSON"}}
	if invalid, err := runtimeDiagnosticsInvalidate(t.Context(), recoverable, failed); err != nil || invalid {
		t.Fatal("recoverable tail interruption overrode an already failed test run")
	}
	if invalid, err := runtimeDiagnosticsInvalidate(t.Context(), recoverable, passed); err != nil || !invalid {
		t.Fatal("tail interruption in a passed test run was accepted")
	}
	if invalid, err := runtimeDiagnosticsInvalidate(t.Context(), corrupt, failed); err != nil || !invalid {
		t.Fatal("complete-record corruption did not take precedence over test failure")
	}
	failed.RuntimeDiagnostics = []string{"disk unavailable"}
	if invalid, err := runtimeDiagnosticsInvalidate(t.Context(), nil, failed); err != nil || !invalid {
		t.Fatal("runtime recorder failure did not invalidate coverage")
	}
}

func TestRuntimeDiagnosticReportMessageDoesNotExposeEventDetails(t *testing.T) {
	t.Parallel()
	for _, severity := range []runtimecov.DiagnosticSeverity{
		runtimecov.DiagnosticRecoverable,
		runtimecov.DiagnosticIntegrity,
		"future-severity",
	} {
		message := runtimeDiagnosticReportMessage(severity)
		if message == "" || strings.Contains(message, "/") {
			t.Fatalf("report message for %q = %q", severity, message)
		}
	}
}

// v1DualRunOracle deliberately lives in tests. It preserves the former
// standard-cover + AST two-execution model only as a semantic comparison
// oracle; production code must never call it or fall back to it.
type v1DualRunOracle struct {
	c0  report.Report
	ast report.Report
}

func runV1DualRunOracle(t *testing.T, root string) v1DualRunOracle {
	t.Helper()
	c0Report, c0Stderr, code := runFixture(t, root, "--coverage=statement,function", "--format=json", "./...")
	if code != ExitSuccess {
		t.Fatalf("v1 C0 oracle exit = %d\nstderr:\n%s", code, c0Stderr)
	}
	if c0Report.MeasurementMode != report.MeasurementStandardCover || len(c0Report.Measurements) != 1 || c0Report.Measurements[0].Name != "standard-cover" {
		t.Fatalf("v1 C0 oracle provenance = mode %q runs %#v", c0Report.MeasurementMode, c0Report.Measurements)
	}
	astReport, astStderr, code := runFixture(
		t,
		root,
		"--coverage=decision,switch-clause-body,type-switch-clause-body,select-clause-body,switch-clause-selection,type-switch-clause-selection,condition,mcdc-unique,mcdc-masking",
		"--format=json",
		"./...",
	)
	if code != ExitSuccess {
		t.Fatalf("v1 AST oracle exit = %d\nstderr:\n%s", code, astStderr)
	}
	if astReport.MeasurementMode != report.MeasurementSingleRun || len(astReport.Measurements) != 1 || astReport.Measurements[0].Name != "ast" {
		t.Fatalf("v1 AST oracle provenance = mode %q runs %#v", astReport.MeasurementMode, astReport.Measurements)
	}
	return v1DualRunOracle{c0: c0Report, ast: astReport}
}

type c0OracleSummary struct {
	Statement report.MetricSummary
	Function  report.MetricSummary
}

type c0OracleProjection struct {
	Summary  c0OracleSummary
	Packages []c0OraclePackage
}

type c0OraclePackage struct {
	Path    string
	Status  string
	Summary c0OracleSummary
	Files   []c0OracleFile
}

type c0OracleFile struct {
	Path      string
	Summary   c0OracleSummary
	Functions []c0OracleFunction
}

type c0OracleFunction struct {
	Name       string
	Location   *cover.SourceLocation
	Summary    c0OracleSummary
	Statements []report.StatementReport
}

func projectC0Semantics(built report.Report) c0OracleProjection {
	projected := c0OracleProjection{Summary: c0Summary(built.Summary)}
	for _, packageReport := range built.Packages {
		projectedPackage := c0OraclePackage{
			Path: packageReport.Path, Status: packageReport.Status,
			Summary: c0Summary(packageReport.Summary),
		}
		for _, file := range packageReport.Files {
			projectedFile := c0OracleFile{Path: file.Path, Summary: c0Summary(file.Summary)}
			for _, function := range file.Functions {
				if len(function.Statements) == 0 && !metricHasObligation(function.Summary.Statement) && !metricHasObligation(function.Summary.Function) {
					continue
				}
				projectedFile.Functions = append(projectedFile.Functions, c0OracleFunction{
					Name: function.Name, Location: function.Location, Summary: c0Summary(function.Summary),
					Statements: append([]report.StatementReport(nil), function.Statements...),
				})
			}
			projectedPackage.Files = append(projectedPackage.Files, projectedFile)
		}
		projected.Packages = append(projected.Packages, projectedPackage)
	}
	return projected
}

func c0Summary(summary report.Summary) c0OracleSummary {
	return c0OracleSummary{Statement: summary.Statement, Function: summary.Function}
}

func metricHasObligation(metric report.MetricSummary) bool {
	return metric.Total != 0 || metric.Unsupported != 0 || metric.Unknown != 0 || metric.Infeasible != 0 || metric.AnalysisIncomplete != 0
}

type astOracleSummary struct {
	Decision                  report.MetricSummary
	SwitchClauseBody          report.MetricSummary
	TypeSwitchClauseBody      report.MetricSummary
	SelectClauseBody          report.MetricSummary
	SwitchClauseSelection     report.MetricSummary
	TypeSwitchClauseSelection report.MetricSummary
	Condition                 report.MetricSummary
	MCDCUnique                report.MetricSummary
	MCDCMasking               report.MetricSummary
}

type astOracleProjection struct {
	Summary  astOracleSummary
	Packages []astOraclePackage
}

type astOraclePackage struct {
	Path    string
	Status  string
	Summary astOracleSummary
	Files   []astOracleFile
}

type astOracleFile struct {
	Path      string
	Summary   astOracleSummary
	Functions []astOracleFunction
}

type astOracleFunction struct {
	Name      string
	Location  *cover.SourceLocation
	Summary   astOracleSummary
	Decisions []report.DecisionReport
	Clauses   []report.ClauseReport
	NoMatches []report.NoMatchReport
}

func projectASTSemantics(built report.Report) astOracleProjection {
	projected := astOracleProjection{Summary: astSummary(built.Summary)}
	for _, packageReport := range built.Packages {
		projectedPackage := astOraclePackage{
			Path: packageReport.Path, Status: packageReport.Status,
			Summary: astSummary(packageReport.Summary),
		}
		for _, file := range packageReport.Files {
			projectedFile := astOracleFile{Path: file.Path, Summary: astSummary(file.Summary)}
			for _, function := range file.Functions {
				if len(function.Decisions) == 0 && len(function.Clauses) == 0 && len(function.NoMatches) == 0 {
					continue
				}
				decisions := make([]report.DecisionReport, len(function.Decisions))
				for index, decision := range function.Decisions {
					decisions[index] = normalizeOracleDecision(decision)
				}
				projectedFile.Functions = append(projectedFile.Functions, astOracleFunction{
					Name: function.Name, Location: function.Location, Summary: astSummary(function.Summary),
					Decisions: decisions,
					Clauses:   append([]report.ClauseReport(nil), function.Clauses...),
					NoMatches: append([]report.NoMatchReport(nil), function.NoMatches...),
				})
			}
			projectedPackage.Files = append(projectedPackage.Files, projectedFile)
		}
		projected.Packages = append(projected.Packages, projectedPackage)
	}
	return projected
}

func astSummary(summary report.Summary) astOracleSummary {
	return astOracleSummary{
		Decision: summary.Decision, SwitchClauseBody: summary.SwitchClauseBody,
		TypeSwitchClauseBody: summary.TypeSwitchClauseBody, SelectClauseBody: summary.SelectClauseBody,
		SwitchClauseSelection: summary.SwitchClauseSelection, TypeSwitchClauseSelection: summary.TypeSwitchClauseSelection,
		Condition: summary.Condition, MCDCUnique: summary.MCDCUnique, MCDCMasking: summary.MCDCMasking,
	}
}

func normalizeOracleDecision(decision report.DecisionReport) report.DecisionReport {
	decision.Summary.Statement = report.MetricSummary{}
	decision.Summary.Function = report.MetricSummary{}
	decision.Evaluations = normalizeOracleEvaluations(decision.Evaluations)
	decision.MCDCUnique = normalizeOracleMCDCAnalysis(decision.MCDCUnique)
	decision.MCDCMasking = normalizeOracleMCDCAnalysis(decision.MCDCMasking)
	decision.Conditions = append([]report.ConditionReport(nil), decision.Conditions...)
	for index := range decision.Conditions {
		decision.Conditions[index].MCDCUnique = normalizeOracleMCDCCondition(decision.Conditions[index].MCDCUnique)
		decision.Conditions[index].MCDCMasking = normalizeOracleMCDCCondition(decision.Conditions[index].MCDCMasking)
	}
	return decision
}

func normalizeOracleMCDCAnalysis(analysis report.MCDCAnalysisReport) report.MCDCAnalysisReport {
	analysis.Conditions = append([]report.MCDCConditionReport(nil), analysis.Conditions...)
	for index := range analysis.Conditions {
		analysis.Conditions[index] = normalizeOracleMCDCCondition(analysis.Conditions[index])
	}
	return analysis
}

func normalizeOracleMCDCCondition(condition report.MCDCConditionReport) report.MCDCConditionReport {
	condition.Witness = normalizeOracleWitness(condition.Witness)
	return condition
}

func normalizeOracleWitness(witness *report.WitnessReport) *report.WitnessReport {
	if witness == nil {
		return nil
	}
	normalized := *witness
	normalized.First = normalizeOracleEvaluation(normalized.First)
	normalized.Second = normalizeOracleEvaluation(normalized.Second)
	normalized.FirstCompletion = append([]string(nil), normalized.FirstCompletion...)
	normalized.SecondCompletion = append([]string(nil), normalized.SecondCompletion...)
	normalized.UnobservedConditions = append([]uint16(nil), normalized.UnobservedConditions...)
	normalized.MaskedConditions = append([]uint16(nil), normalized.MaskedConditions...)
	if oracleEvaluationKey(normalized.Second) < oracleEvaluationKey(normalized.First) {
		normalized.First, normalized.Second = normalized.Second, normalized.First
		normalized.FirstCompletion, normalized.SecondCompletion = normalized.SecondCompletion, normalized.FirstCompletion
	}
	return &normalized
}

func normalizeOracleEvaluations(evaluations []report.EvaluationReport) []report.EvaluationReport {
	normalized := make([]report.EvaluationReport, len(evaluations))
	for index, evaluation := range evaluations {
		normalized[index] = normalizeOracleEvaluation(evaluation)
	}
	sort.Slice(normalized, func(i, j int) bool {
		return oracleEvaluationKey(normalized[i]) < oracleEvaluationKey(normalized[j])
	})
	return normalized
}

func normalizeOracleEvaluation(evaluation report.EvaluationReport) report.EvaluationReport {
	evaluation.EvaluationID = ""
	evaluation.RunID = ""
	evaluation.ProcessID = 0
	evaluation.TestID = ""
	evaluation.Conditions = append([]string(nil), evaluation.Conditions...)
	return evaluation
}

func oracleEvaluationKey(evaluation report.EvaluationReport) string {
	return evaluation.DecisionID + "\x00" + evaluation.Status + "\x00" + evaluation.PackagePath + "\x00" +
		strings.Join(evaluation.Conditions, "\x00") + "\x00" + strconv.FormatBool(evaluation.Result)
}

func firstSemanticDifference(got, want reflect.Value, path string) string {
	if got.IsValid() != want.IsValid() {
		return path + ": validity differs"
	}
	if !got.IsValid() {
		return ""
	}
	if got.Type() != want.Type() {
		return path + ": type differs"
	}
	switch got.Kind() {
	case reflect.Pointer, reflect.Interface:
		if got.IsNil() != want.IsNil() {
			return path + ": nil differs"
		}
		if got.IsNil() {
			return ""
		}
		return firstSemanticDifference(got.Elem(), want.Elem(), path)
	case reflect.Struct:
		for index := 0; index < got.NumField(); index++ {
			fieldPath := path + "." + got.Type().Field(index).Name
			if difference := firstSemanticDifference(got.Field(index), want.Field(index), fieldPath); difference != "" {
				return difference
			}
		}
	case reflect.Slice, reflect.Array:
		if got.Len() != want.Len() {
			return path + ": length " + strconv.Itoa(got.Len()) + ", want " + strconv.Itoa(want.Len())
		}
		for index := 0; index < got.Len(); index++ {
			if difference := firstSemanticDifference(got.Index(index), want.Index(index), path+"["+strconv.Itoa(index)+"]"); difference != "" {
				return difference
			}
		}
	default:
		if !reflect.DeepEqual(got.Interface(), want.Interface()) {
			return path + ": got " + fmt.Sprint(got.Interface()) + ", want " + fmt.Sprint(want.Interface())
		}
	}
	return ""
}

func runFixture(t *testing.T, root string, arguments ...string) (report.Report, string, int) {
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	args := append([]string{"test", "--timeout=2m"}, arguments...)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	code := runAt(ctx, root, args, &stdout, &stderr)
	var built report.Report
	if err := json.Unmarshal(stdout.Bytes(), &built); err != nil {
		t.Fatalf("decode report (exit %d): %v\nstdout:\n%s\nstderr:\n%s", code, err, stdout.String(), stderr.String())
	}
	return built, stderr.String(), code
}

func configureIntegrationEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("GOWORK", "off")
}

func fixturePath(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func writeIntegrationFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func reportMetrics(summary report.Summary) map[string]report.MetricSummary {
	return map[string]report.MetricSummary{
		"statement": summary.Statement, "function": summary.Function,
		"decision":                  summary.Decision,
		"switchClauseBody":          summary.SwitchClauseBody,
		"typeSwitchClauseBody":      summary.TypeSwitchClauseBody,
		"selectClauseBody":          summary.SelectClauseBody,
		"switchClauseSelection":     summary.SwitchClauseSelection,
		"typeSwitchClauseSelection": summary.TypeSwitchClauseSelection,
		"condition":                 summary.Condition, "mcdcUnique": summary.MCDCUnique,
		"mcdcMasking": summary.MCDCMasking,
	}
}

func assertPackageSums(t *testing.T, built report.Report) {
	t.Helper()
	for name, moduleMetric := range reportMetrics(built.Summary) {
		covered, total := 0, 0
		for _, packageReport := range built.Packages {
			metric := reportMetrics(packageReport.Summary)[name]
			covered += metric.Covered
			total += metric.Total
		}
		if covered != moduleMetric.Covered || total != moduleMetric.Total {
			t.Fatalf("%s module=%d/%d package sum=%d/%d", name, moduleMetric.Covered, moduleMetric.Total, covered, total)
		}
	}
}

func findDecision(t *testing.T, built report.Report, expression string) report.DecisionReport {
	t.Helper()
	for _, packageReport := range built.Packages {
		for _, file := range packageReport.Files {
			for _, function := range file.Functions {
				for _, decision := range function.Decisions {
					if decision.Expression == expression {
						return decision
					}
				}
			}
		}
	}
	t.Fatalf("decision %q not found", expression)
	return report.DecisionReport{}
}

func findFile(t *testing.T, built report.Report, path string) report.FileReport {
	t.Helper()
	for _, packageReport := range built.Packages {
		for _, file := range packageReport.Files {
			if file.Path == path {
				return file
			}
		}
	}
	t.Fatalf("file %q not found", path)
	return report.FileReport{}
}

func findDecisionInFunction(t *testing.T, built report.Report, functionName, expression string) report.DecisionReport {
	t.Helper()
	for _, packageReport := range built.Packages {
		for _, file := range packageReport.Files {
			for _, function := range file.Functions {
				if function.Name != functionName {
					continue
				}
				for _, decision := range function.Decisions {
					if decision.Expression == expression {
						return decision
					}
				}
			}
		}
	}
	t.Fatalf("decision %s:%q not found", functionName, expression)
	return report.DecisionReport{}
}

func findFunction(t *testing.T, built report.Report, name string) report.FunctionReport {
	t.Helper()
	for _, packageReport := range built.Packages {
		for _, file := range packageReport.Files {
			for _, function := range file.Functions {
				if function.Name == name {
					return function
				}
			}
		}
	}
	t.Fatalf("function %q not found", name)
	return report.FunctionReport{}
}

func hasAbortedEvaluation(built report.Report) bool {
	for _, packageReport := range built.Packages {
		for _, file := range packageReport.Files {
			for _, function := range file.Functions {
				for _, decision := range function.Decisions {
					for _, evaluation := range decision.Evaluations {
						if evaluation.Status == "aborted" {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

func hasEvaluationStatus(decision report.DecisionReport, status string) bool {
	for _, evaluation := range decision.Evaluations {
		if evaluation.Status == status {
			return true
		}
	}
	return false
}

func hasCoveredSelectClause(built report.Report) bool {
	for _, packageReport := range built.Packages {
		for _, file := range packageReport.Files {
			for _, function := range file.Functions {
				for _, clause := range function.Clauses {
					if clause.Kind == cover.ClauseSelect && clause.BodyCoverage.Covered > 0 {
						return true
					}
				}
			}
		}
	}
	return false
}

func findPackage(t *testing.T, built report.Report, path string) report.PackageReport {
	t.Helper()
	for _, packageReport := range built.Packages {
		if packageReport.Path == path {
			return packageReport
		}
	}
	t.Fatalf("package %q not found", path)
	return report.PackageReport{}
}

func findProducerOutcome(t *testing.T, built report.Report, producer report.ProducerName) report.ProducerOutcome {
	t.Helper()
	for _, outcome := range built.ProducerOutcomes {
		if outcome.Producer == producer {
			return outcome
		}
	}
	t.Fatalf("producer outcome %q not found in %#v", producer, built.ProducerOutcomes)
	return report.ProducerOutcome{}
}
