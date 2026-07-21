package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

func TestIntegratedFixtureWritesPackageCenteredHTML(t *testing.T) {
	configureIntegrationEnvironment(t)
	t.Setenv("GOMCDC_ISOLATION_FIXTURE", "1")
	root := fixturePath(t, "integration")
	output := filepath.Join(t.TempDir(), "coverage-html")
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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
	for _, required := range [][]byte{[]byte("Package navigation"), []byte("example.test/gomcdc-fixture/allow"), []byte("allow/allow.go"), []byte("Allow"), []byte("Original source"), []byte("source-code"), []byte("metric-condition"), []byte("No-match selection"), []byte("a &amp;&amp; b"), []byte("UC MC/DC"), []byte("Mask MC/DC"), []byte("Masking witness")} {
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
	t.Setenv("GOMCDC_ISOLATION_FIXTURE", "1")
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

	c0Only, c0Stderr, code := runFixture(t, root, "--coverage=statement,function", "--format=json", "./...")
	if code != ExitSuccess {
		t.Fatalf("C0-only exit = %d\nstderr:\n%s", code, c0Stderr)
	}
	if c0Only.MeasurementMode != report.MeasurementStandardCover || len(c0Only.Measurements) != 1 {
		t.Fatalf("C0 measurement provenance = mode %q runs %#v", c0Only.MeasurementMode, c0Only.Measurements)
	}
	if all.Summary.Statement.Covered != c0Only.Summary.Statement.Covered ||
		all.Summary.Statement.Total != c0Only.Summary.Statement.Total ||
		all.Summary.Function.Covered != c0Only.Summary.Function.Covered ||
		all.Summary.Function.Total != c0Only.Summary.Function.Total {
		t.Fatalf(
			"integrated C0 differs from uninstrumented C0: all statement=%d/%d function=%d/%d; c0-only statement=%d/%d function=%d/%d",
			all.Summary.Statement.Covered, all.Summary.Statement.Total,
			all.Summary.Function.Covered, all.Summary.Function.Total,
			c0Only.Summary.Statement.Covered, c0Only.Summary.Statement.Total,
			c0Only.Summary.Function.Covered, c0Only.Summary.Function.Total,
		)
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
			"--coverage=decision", "--strict", "--fail-under-decision=100", "--format=json", "./unstable",
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
		assertPartialDecisionEvidence(t, built)
		for _, code := range []string{"go-test-test", "coverage-threshold-failed"} {
			if !hasReportErrorCode(built, code) {
				t.Fatalf("report errors omit %q: %#v", code, built.Errors)
			}
		}
	})

	t.Run("truncated tail is recoverable after test failure", func(t *testing.T) {
		t.Setenv("GOMCDC_FAILURE_MODE", "truncate")
		built, stderr, code := runFixture(t, root, "--coverage=decision", "--format=json", "./unstable")
		if code != ExitTestsFailed {
			t.Fatalf("exit=%d, want %d\nstderr:\n%s", code, ExitTestsFailed, stderr)
		}
		if built.Run.Results.Test != report.ResultFailed || built.Run.Results.Integrity != report.ResultPassed {
			t.Fatalf("truncated-tail results = %#v", built.Run.Results)
		}
		assertPartialDecisionEvidence(t, built)
		if !hasReportErrorCode(built, "runtime-recoverable-interruption") {
			t.Fatalf("recoverable diagnostic is missing: %#v", built.Errors)
		}
	})

	t.Run("integrity failure takes precedence over test and threshold", func(t *testing.T) {
		t.Setenv("GOMCDC_FAILURE_MODE", "corrupt")
		built, stderr, code := runFixture(
			t, root,
			"--coverage=decision", "--fail-under-decision=100", "--format=json", "./unstable",
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
			"--coverage=decision", "--strict", "--fail-under-decision=100", "--format=json", "./unstable",
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
		assertPartialDecisionEvidence(t, built)
		if !hasReportErrorCode(built, "go-test-timeout") {
			t.Fatalf("timeout error is missing: %#v", built.Errors)
		}
	})
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
	validated, err := acceptRuntimeEvidence(context.Background(), []cover.DecisionMetadata{decision}, nil, recorded, "run", nil)
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
	validated, err := acceptRuntimeEvidence(context.Background(),
		[]cover.DecisionMetadata{first, second},
		clauses,
		runtimecov.RecordedEvidence{Evaluations: []cover.DecisionEvaluation{evaluation}, NotEvaluatedDecisions: []cover.DecisionNotEvaluatedObservation{observation}},
		"run",
		nil,
	)
	if err != nil || len(validated.NotEvaluatedDecisions) != 1 {
		t.Fatalf("validated=%#v err=%v", validated, err)
	}
	withoutSuffix, err := acceptRuntimeEvidence(context.Background(),
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
			validated, err := acceptRuntimeEvidence(context.Background(),
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

	accepted, err := acceptRuntimeEvidence(context.Background(),
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
			validated, err := acceptRuntimeEvidence(context.Background(), decisions, clauses, collection, "run", nil)
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
			validated, err := acceptRuntimeEvidence(context.Background(),
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
			accepted, err := acceptRuntimeEvidence(context.Background(), nil, metadata, runtimecov.RecordedEvidence{ClauseEvents: []runtimecov.RecordedClauseEvent{event}}, "run", nil)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) || len(accepted.ClauseObservations) != 0 {
				t.Fatalf("accepted=%#v err=%v, want rejection containing %q", accepted, err, test.wantErr)
			}
		})
	}

	secondProcess := valid
	secondProcess.ProcessID++
	accepted, err := acceptRuntimeEvidence(context.Background(), nil, metadata, runtimecov.RecordedEvidence{ClauseEvents: []runtimecov.RecordedClauseEvent{valid, secondProcess}}, "run", nil)
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
		accepted, err := acceptRuntimeEvidence(context.Background(), nil, nil, runtimecov.RecordedEvidence{Files: []runtimecov.ProcessFile{file}}, "run", nil)
		if err == nil || !strings.Contains(err.Error(), "invalid provenance") || len(accepted.Files) != 1 {
			t.Fatalf("file=%#v accepted=%#v err=%v", file, accepted, err)
		}
	}
	metadata := []cover.DecisionMetadata{{ID: 1, Package: "example.test/p"}}
	valid := runtimecov.ProcessFile{Path: "/tmp/valid", RunID: "run", PackagePath: "example.test/p", ProcessID: 1}
	if _, err := acceptRuntimeEvidence(context.Background(), metadata, nil, runtimecov.RecordedEvidence{Files: []runtimecov.ProcessFile{valid}}, "run", nil); err != nil {
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
			code := runAt(context.Background(), t.TempDir(), test.args, &stdout, &stderr)
			if code != ExitInvalidUsage || !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestCombinedRunRetainsPackageStatuses(t *testing.T) {
	t.Parallel()
	combined := &gotest.Result{
		Status: cover.RunPassed, FailureKind: cover.RunFailureNone,
		Packages: map[string]gotest.PackageStatus{"example.test/p": gotest.PackagePassed},
	}
	measurements := measurementRuns(nil, combined, true)
	if len(measurements) != 1 || measurements[0].Name != "combined" || measurements[0].Packages["example.test/p"] != "passed" {
		t.Fatalf("measurement package provenance = %#v", measurements)
	}
	if got := mergePackageStatus("build-failed", "passed"); got != "build-failed" {
		t.Fatalf("combined package status = %q", got)
	}
}

func TestRuntimeDiagnosticSeverityDistinguishesInterruptionFromCorruption(t *testing.T) {
	t.Parallel()
	failed := &gotest.Result{Status: cover.RunFailed}
	passed := &gotest.Result{Status: cover.RunPassed}
	recoverable := []runtimecov.Diagnostic{{Severity: runtimecov.DiagnosticRecoverable, Truncated: true, Message: "truncated final event record"}}
	corrupt := []runtimecov.Diagnostic{{Severity: runtimecov.DiagnosticIntegrity, Message: "decode event JSON"}}
	if invalid, err := runtimeDiagnosticsInvalidate(context.Background(), recoverable, failed); err != nil || invalid {
		t.Fatal("recoverable tail interruption overrode an already failed test run")
	}
	if invalid, err := runtimeDiagnosticsInvalidate(context.Background(), recoverable, passed); err != nil || !invalid {
		t.Fatal("tail interruption in a passed test run was accepted")
	}
	if invalid, err := runtimeDiagnosticsInvalidate(context.Background(), corrupt, failed); err != nil || !invalid {
		t.Fatal("complete-record corruption did not take precedence over test failure")
	}
	failed.RuntimeDiagnostics = []string{"disk unavailable"}
	if invalid, err := runtimeDiagnosticsInvalidate(context.Background(), nil, failed); err != nil || !invalid {
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

func runFixture(t *testing.T, root string, arguments ...string) (report.Report, string, int) {
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	args := append([]string{"test", "--timeout=2m"}, arguments...)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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
