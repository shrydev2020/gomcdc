package cli

import (
	"go/version"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/c0"
	"github.com/shrydev2020/gomcdc/v2/internal/report"
)

func TestAccessReportAllMetricsIdentifiesMissingExpiredCase(t *testing.T) {
	root := t.TempDir()
	writeIntegrationFile(t, filepath.Join(root, "go.mod"), "module example.test/access\n\ngo 1.26.0\n")
	writeIntegrationFile(t, filepath.Join(root, "access.go"), `package access

func CanAccess(signedIn, expired bool) bool {
	if signedIn && !expired {
		return true
	}
	return false
}
`)
	writeIntegrationFile(t, filepath.Join(root, "access_test.go"), `package access

import "testing"

func TestAccess(t *testing.T) {
	if !CanAccess(true, false) {
		t.Fatal("active session denied")
	}
	if CanAccess(false, false) {
		t.Fatal("signed-out visitor allowed")
	}
}
`)
	built, stderr, code := runFixture(t, root, "--strict", "--format=json", "./...")
	if code != ExitSuccess {
		t.Fatalf("all-metric access report exit=%d\n%s", code, stderr)
	}
	if built.Run.Results.Integrity != report.ResultPassed || built.Run.Results.Strict != report.ResultPassed {
		t.Fatalf("access evidence was not fully accepted: %#v", built.Run.Results)
	}
	for name, metric := range map[string]report.MetricSummary{
		"statement": built.Summary.Statement, "function": built.Summary.Function,
		"decision": built.Summary.Decision, "condition": built.Summary.Condition,
		"unique": built.Summary.MCDCUnique, "masking": built.Summary.MCDCMasking,
		"switch body": built.Summary.SwitchClauseBody, "type switch body": built.Summary.TypeSwitchClauseBody,
		"select body": built.Summary.SelectClauseBody, "switch selection": built.Summary.SwitchClauseSelection,
		"type switch selection": built.Summary.TypeSwitchClauseSelection,
	} {
		if !metric.Enabled || metric.Unknown != 0 || metric.AnalysisIncomplete != 0 {
			t.Errorf("%s metric = %#v", name, metric)
		}
	}
	if built.Summary.Statement.Covered != 3 || built.Summary.Statement.Total != 3 ||
		built.Summary.Function.Covered != 1 || built.Summary.Function.Total != 1 ||
		built.Summary.Decision.Covered != 2 || built.Summary.Decision.Total != 2 ||
		built.Summary.Condition.Covered != 3 || built.Summary.Condition.Total != 4 ||
		built.Summary.MCDCMasking.Covered != 1 || built.Summary.MCDCMasking.Total != 2 ||
		built.Summary.MCDCUnique.Covered != 0 || built.Summary.MCDCUnique.Total != 1 || built.Summary.MCDCUnique.Infeasible != 1 {
		t.Fatalf("access report counts = %#v", built.Summary)
	}
	decision := built.Packages[0].Files[0].Functions[0].Decisions[0]
	for _, evaluation := range decision.Evaluations {
		if evaluation.TestID != "TestAccess" {
			t.Fatalf("access evaluation test = %q", evaluation.TestID)
		}
	}
	witness := decision.Conditions[0].MCDCMasking.Witness
	if witness == nil || witness.First.TestID != "TestAccess" || witness.Second.TestID != "TestAccess" {
		t.Fatalf("access witness lost its test: %#v", witness)
	}
	expired := decision.Conditions[1]
	if expired.Expression != "expired" || expired.True || !expired.False || expired.NotEvaluated != 1 ||
		expired.MCDCUnique.Status != "not-covered" || expired.MCDCMasking.Status != "not-covered" ||
		expired.MCDCMasking.Reason == "" {
		t.Fatalf("missing expired case was not identified: %#v", expired)
	}
}

func TestAllMetricsMeasureGenericMethod(t *testing.T) {
	command := exec.CommandContext(t.Context(), "go", "env", "GOVERSION")
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	if version.Compare(strings.TrimSpace(string(output)), "go1.27") < 0 {
		t.Skip("generic methods require Go 1.27 or later")
	}
	root := t.TempDir()
	writeIntegrationFile(t, filepath.Join(root, "go.mod"), "module example.test/genericmethod\n\ngo 1.27.0\n")
	writeIntegrationFile(t, filepath.Join(root, "method.go"), `package genericmethod

type Access struct{}

func (Access) Check[T ~int | ~int64](value T, enabled bool) bool {
	if value > 0 && enabled {
		return true
	}
	return false
}
`)
	writeIntegrationFile(t, filepath.Join(root, "method_test.go"), `package genericmethod

import "testing"

func TestCheck(t *testing.T) {
	a := Access{}
	if !a.Check(1, true) || a.Check(0, true) || a.Check(int64(1), false) {
		t.Fatal("unexpected access result")
	}
}
`)
	built, stderr, code := runFixture(t, root, "--strict", "--format=json", "./...")
	if code != ExitSuccess {
		t.Fatalf("generic method report exit=%d\n%s", code, stderr)
	}
	if built.Summary.Statement.Covered != 3 || built.Summary.Statement.Total != 3 ||
		built.Summary.Function.Covered != 1 || built.Summary.MCDCMasking.Covered != 2 || built.Summary.MCDCMasking.Total != 2 {
		t.Fatalf("generic method report counts = %#v", built.Summary)
	}
}

func TestStatementCoverageDoesNotCreditUnreachedSplitRange(t *testing.T) {
	layout, err := c0.DetectCoverLayout(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeIntegrationFile(t, filepath.Join(root, "go.mod"), "module example.test/splitrange\n\ngo 1.26.0\n")
	writeIntegrationFile(t, filepath.Join(root, "work.go"), `package splitrange

func Work() {
	Stop()

	_ = 1
}

func Stop() { panic("stopped") }
`)
	writeIntegrationFile(t, filepath.Join(root, "work_test.go"), `package splitrange

import "testing"

func TestWork(t *testing.T) {
	defer func() {
		if recover() != "stopped" {
			t.Fatal("expected panic")
		}
	}()
	Work()
}
`)
	built, stderr, code := runFixture(t, root, "--coverage=statement,function", "--strict", "--format=json", "./...")
	if code != ExitSuccess {
		t.Fatalf("split range report exit=%d\n%s", code, stderr)
	}
	wantCovered := 3
	if layout.SplitCodeRanges {
		wantCovered = 2
	}
	if built.Summary.Statement.Total != 3 || built.Summary.Statement.Covered != wantCovered ||
		built.Summary.Statement.Unknown != 0 || built.Summary.Function.Covered != 2 {
		t.Fatalf("unreached statement was credited: %#v", built.Summary)
	}
}
