package cli

import (
	"bytes"
	"context"
	"encoding/json"
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
	if all.Version != report.SchemaVersion || all.Module != "example.test/gomcdc-fixture" {
		t.Fatalf("report identity = version %q module %q", all.Version, all.Module)
	}
	if all.Run.Status != cover.RunPassed || !all.Run.Complete {
		t.Fatalf("run = %#v", all.Run)
	}
	if all.MeasurementMode != report.MeasurementDualRunStandardCover || len(all.Measurements) != 2 {
		t.Fatalf("dual measurement provenance = mode %q runs %#v", all.MeasurementMode, all.Measurements)
	}
	for _, measurement := range all.Measurements {
		if len(measurement.Packages) != 4 {
			t.Fatalf("measurement %q package statuses = %#v, want 4 packages", measurement.Name, measurement.Packages)
		}
		if got := measurement.Packages["example.test/gomcdc-fixture/shared"]; got != string(gotest.PackageSkipped) {
			t.Fatalf("measurement %q shared package status = %q, want %q", measurement.Name, got, gotest.PackageSkipped)
		}
	}
	if all.Capabilities.Status(backend.CapabilityDirectCaseSelection) != backend.CapabilityUnsupportedByBackend || all.Instrumentation.Total.Discovered == 0 || !all.Instrumentation.HasGaps() {
		t.Fatalf("backend capability/instrumentation accounting = capabilities %#v instrumentation %#v", all.Capabilities, all.Instrumentation)
	}
	if len(all.Packages) != 4 {
		t.Fatalf("packages = %d, want 4", len(all.Packages))
	}
	for name, metric := range reportMetrics(all.Summary) {
		if !metric.Enabled {
			t.Fatalf("default all metric %s = %#v", name, metric)
		}
		if name == "switchClauseSelection" || name == "typeSwitchClauseSelection" {
			if metric.Total != 0 || metric.Unsupported == 0 {
				t.Fatalf("unsupported selection metric %s = %#v", name, metric)
			}
		} else if metric.Total == 0 {
			t.Fatalf("default all metric %s has empty denominator: %#v", name, metric)
		}
	}
	assertPackageSums(t, all)
	shared := findPackage(t, all, "example.test/gomcdc-fixture/shared")
	if shared.Summary.Statement.Covered == 0 || shared.Summary.Function.Covered == 0 || shared.Summary.Decision.Covered != shared.Summary.Decision.Total {
		t.Fatalf("cross-package calls did not align C0 and AST scope: %#v", shared.Summary)
	}

	allow := findDecisionInFunction(t, all, "Allow", "a && b")
	if !allow.DecisionCoverage.True || !allow.DecisionCoverage.False {
		t.Fatalf("Allow decision outcomes = %#v", allow.DecisionCoverage)
	}
	if got := allow.Conditions[1].NotEvaluated; got == 0 {
		t.Fatalf("short-circuited b not-evaluated count = %d", got)
	}
	if allow.Conditions[0].MCDCUnique.Status != string(cover.CoveragePossiblyInfeasible) ||
		allow.Conditions[0].MCDCMasking.Status != string(cover.CoverageCovered) {
		t.Fatalf("Allow(a) MC/DC unique=%q masking=%q", allow.Conditions[0].MCDCUnique.Status, allow.Conditions[0].MCDCMasking.Status)
	}
	if allow.Conditions[0].MCDCMasking.Witness == nil {
		t.Fatal("masking MC/DC witness is missing")
	}
	generated := findDecisionInFunction(t, all, "GeneratedGate", "a && b")
	if !generated.DecisionCoverage.True || !generated.DecisionCoverage.False {
		t.Fatalf("user-generated source decision outcomes = %#v", generated.DecisionCoverage)
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
	if !hasCoveredSelectClause(all) {
		t.Fatal("select clause body coverage is missing")
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
	built, stderr, code := runFixture(t, root, "--coverage=decision", "--include-tests", "--fail-under-decision=100", "--format=json", "./...")
	if code != ExitCoverageThreshold {
		t.Fatalf("exit = %d, want %d\nstderr:\n%s", code, ExitCoverageThreshold, stderr)
	}
	if !strings.Contains(stderr, "decision") || !strings.Contains(stderr, "below 100.00%") {
		t.Fatalf("threshold diagnostic missing: %s", stderr)
	}
	if !hasTestSourceDecision(built) {
		t.Fatal("--include-tests did not add _test.go decisions")
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
	built, stderr, code := runFixture(t, root, "--format=json", "./...")
	if code != ExitMeasurementFailed {
		t.Fatalf("exit = %d, want measurement failure %d\nstderr:\n%s", code, ExitMeasurementFailed, stderr)
	}
	if built.Run.Status != cover.RunFailed || built.Run.FailureKind != cover.RunFailureBuild || built.Run.Complete {
		t.Fatalf("partial run = %#v", built.Run)
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

func TestValidationDropsImpossibleCompletedEvidence(t *testing.T) {
	t.Parallel()
	decision := cover.DecisionMetadata{
		ID: 1, Package: "example.test/p",
		Conditions: []cover.ConditionMetadata{{Index: 0}, {Index: 1}},
		ExpressionTree: cover.NewAndExpression(
			cover.NewConditionExpression(0),
			cover.NewConditionExpression(1),
		),
	}
	collection := runtimecov.Collection{Evaluations: []cover.DecisionEvaluation{{
		DecisionID: 1, EvaluationID: 1, RunID: "run", PackagePath: "example.test/p",
		Conditions: []cover.ConditionState{cover.ConditionFalse, cover.ConditionTrue},
		Result:     false, Status: cover.EvaluationCompleted,
	}}}
	validated, err := validateObservations([]cover.DecisionMetadata{decision}, nil, collection, "run", nil)
	if err == nil || len(validated.Evaluations) != 0 {
		t.Fatalf("validated=%#v err=%v; impossible vector became coverage evidence", validated, err)
	}
}

func TestValidationKeepsConditionlessSwitchNotEvaluatedEvidence(t *testing.T) {
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
		{ID: 10, GroupID: 100, Kind: cover.ClauseConditionlessSwitch, Role: cover.ClauseCase, Index: 0, DecisionIDs: []cover.DecisionID{1}},
		{ID: 11, GroupID: 100, Kind: cover.ClauseConditionlessSwitch, Role: cover.ClauseCase, Index: 1, DecisionIDs: []cover.DecisionID{2}},
	}
	validated, err := validateObservations(
		[]cover.DecisionMetadata{first, second},
		clauses,
		runtimecov.Collection{Evaluations: []cover.DecisionEvaluation{evaluation}, NotEvaluatedDecisions: []cover.DecisionNotEvaluatedObservation{observation}},
		"run",
		nil,
	)
	if err != nil || len(validated.NotEvaluatedDecisions) != 1 {
		t.Fatalf("validated=%#v err=%v", validated, err)
	}
	withoutSuffix, err := validateObservations(
		[]cover.DecisionMetadata{first, second},
		clauses,
		runtimecov.Collection{Evaluations: []cover.DecisionEvaluation{evaluation}},
		"run",
		nil,
	)
	if err == nil || len(withoutSuffix.NotEvaluatedDecisions) != 0 {
		t.Fatalf("missing complete skip suffix was accepted: validated=%#v err=%v", withoutSuffix, err)
	}
}

func TestRejectsOverlayFromExplicitFlagsAndGOFLAGS(t *testing.T) {
	for _, test := range []struct {
		name    string
		goFlags string
		args    []string
	}{
		{name: "explicit", args: []string{"test", ".", "--", "-overlay=/tmp/overlay.json"}},
		{name: "GOFLAGS", goFlags: `-tags=integration "-overlay=/tmp/a b.json"`, args: []string{"test", "."}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("GOFLAGS", test.goFlags)
			var stdout, stderr bytes.Buffer
			code := runAt(context.Background(), t.TempDir(), test.args, &stdout, &stderr)
			if code != ExitInvalidUsage || !strings.Contains(stderr.String(), "-overlay is unsupported") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestDualRunPackageStatusesRetainDivergentMeasurements(t *testing.T) {
	t.Parallel()
	standard := &gotest.Result{
		Status: cover.RunFailed, FailureKind: cover.RunFailureBuild,
		Packages: map[string]gotest.PackageStatus{"example.test/p": gotest.PackageBuildFailed},
	}
	ast := &gotest.Result{
		Status: cover.RunPassed, FailureKind: cover.RunFailureNone,
		Packages: map[string]gotest.PackageStatus{"example.test/p": gotest.PackagePassed},
	}
	measurements := measurementRuns(standard, ast)
	if len(measurements) != 2 || measurements[0].Packages["example.test/p"] != "build-failed" || measurements[1].Packages["example.test/p"] != "passed" {
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
	if runtimeDiagnosticsInvalidate(recoverable, failed) {
		t.Fatal("recoverable tail interruption overrode an already failed test run")
	}
	if !runtimeDiagnosticsInvalidate(recoverable, passed) {
		t.Fatal("tail interruption in a passed test run was accepted")
	}
	if !runtimeDiagnosticsInvalidate(corrupt, failed) {
		t.Fatal("complete-record corruption did not take precedence over test failure")
	}
	failed.RuntimeDiagnostics = []string{"disk unavailable"}
	if !runtimeDiagnosticsInvalidate(nil, failed) {
		t.Fatal("runtime recorder failure did not invalidate coverage")
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
