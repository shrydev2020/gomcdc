package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/report"
)

func TestIncludeTestsMeasuresStatementsAndFunctionsInOneRun(t *testing.T) {
	configureIntegrationEnvironment(t)
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "runs")
	t.Setenv("TEST_COVER_MARKER", marker)
	writeIntegrationFile(t, filepath.Join(root, "go.mod"), "module example.test/testcover\n\ngo 1.26\n")
	writeIntegrationFile(t, filepath.Join(root, "logic.go"), "package logic\nvar __gomcdcTestCover0, __gomcdcTestCover1Runtime = 0, 0\nfunc Value() bool { return true }\n")
	source := `package logic
import ("testing"; "os"; "sync")
func helper(value bool) int { if value { return 1 }; return 0 }
func unused() int { return 7 }
func TestIncluded(t *testing.T) {
 file, err := os.OpenFile(os.Getenv("TEST_COVER_MARKER"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
 if err != nil { t.Fatal(err) }; file.WriteString("x"); file.Close()
 var workers sync.WaitGroup
 for range 8 { workers.Go(func() { for range 100 { helper(true) } }) }; workers.Wait()
 t.Run("parallel", func(t *testing.T) { t.Parallel(); if helper(Value()) != 1 { t.Fatal("bad") } })
}
`
	writeIntegrationFile(t, filepath.Join(root, "logic_test.go"), source)
	writeIntegrationFile(t, filepath.Join(root, "external_test.go"), `package logic_test
import "testing"
func externalUnused() int { return 0 }
func TestExternal(t *testing.T) { t.Log("external") }
`)
	for _, metrics := range []string{"statement,function", "all"} {
		t.Run(metrics, func(t *testing.T) {
			if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			built, stderr, code := runFixture(t, root, "--coverage="+metrics, "--include-tests", "--format=json", "./...", "--", "-race")
			if code != ExitSuccess {
				t.Fatalf("exit=%d\n%s", code, stderr)
			}
			if len(built.Measurements) != 1 {
				t.Fatalf("measurements=%v", built.Measurements)
			}
			runs, err := os.ReadFile(marker)
			if err != nil || string(runs) != "x" {
				t.Fatalf("test executions=%q error=%v", runs, err)
			}
			seen := map[string]bool{}
			for _, pkg := range built.Packages {
				for _, file := range pkg.Files {
					if !strings.HasSuffix(file.Path, "_test.go") {
						continue
					}
					for _, function := range file.Functions {
						seen[function.Name] = true
						switch function.Name {
						case "helper", "TestIncluded", "TestExternal":
							if function.Summary.Function.Covered != 1 {
								t.Errorf("%s not covered: %+v", function.Name, function.Summary)
							}
						case "unused", "externalUnused":
							if function.Summary.Function.Total != 1 || function.Summary.Function.Covered != 0 {
								t.Errorf("%s wrongly covered: %+v", function.Name, function.Summary)
							}
						}
					}
				}
			}
			for _, name := range []string{"helper", "unused", "TestIncluded", "externalUnused", "TestExternal"} {
				if !seen[name] {
					t.Errorf("missing test function %s", name)
				}
			}
			if built.Summary.Statement.Total == 0 || built.Summary.Statement.Covered == 0 {
				t.Fatalf("statement=%+v", built.Summary.Statement)
			}
			assertPackageSums(t, built)
		})
	}
	actual, err := os.ReadFile(filepath.Join(root, "logic_test.go"))
	if err != nil || string(actual) != source {
		t.Fatal("original test file was modified")
	}
	baseline, stderr, code := runFixture(t, root, "--coverage=statement,function", "--format=json", "./...")
	if code != ExitSuccess {
		t.Fatalf("default exit=%d\n%s", code, stderr)
	}
	if baseline.Summary.Function.Covered != 1 || baseline.Summary.Function.Total != 1 {
		t.Fatalf("default function summary: %+v", baseline.Summary.Function)
	}
	for _, pkg := range baseline.Packages {
		for _, file := range pkg.Files {
			if strings.HasSuffix(file.Path, "_test.go") {
				t.Fatalf("test source included by default: %s", file.Path)
			}
		}
	}
}

func TestIncludeTestsRetainsFailedAndTestOnlyPackageEvidence(t *testing.T) {
	configureIntegrationEnvironment(t)
	for _, termination := range []string{"t.FailNow()", `panic("stop")`, "runtime.Goexit()"} {
		t.Run(termination, func(t *testing.T) {
			root := t.TempDir()
			writeIntegrationFile(t, filepath.Join(root, "go.mod"), "module example.test/testonly\n\ngo 1.26\n")
			writeIntegrationFile(t, filepath.Join(root, "only_test.go"), `package only
import ("testing";"runtime")
var _ = runtime.Goexit
func reached() int { return 1 }
func unreached() int { return 2 }
func TestFailure(t *testing.T) { reached(); `+termination+`; unreached() }
`)
			built, stderr, code := runFixture(t, root, "--coverage=statement,function", "--include-tests", "--format=json", "./...")
			if code != ExitTestsFailed {
				t.Fatalf("exit=%d\n%s", code, stderr)
			}
			if built.Summary.Function.Covered != 2 || built.Summary.Function.Total != 3 {
				t.Fatalf("functions=%+v\n%s", built.Summary.Function, stderr)
			}
			if built.Summary.Statement.Covered == 0 {
				t.Fatalf("statements=%+v", built.Summary.Statement)
			}
			assertPackageSums(t, built)
		})
	}
}

func TestIncludeTestsRespectsExclusionsAndBuildTags(t *testing.T) {
	configureIntegrationEnvironment(t)
	root := t.TempDir()
	writeIntegrationFile(t, filepath.Join(root, "go.mod"), "module example.test/testscope\n\ngo 1.26\n")
	writeIntegrationFile(t, filepath.Join(root, "only_test.go"), "package only\nimport \"testing\"\nfunc TestIncluded(t *testing.T) { t.Log(\"ok\") }\n")
	writeIntegrationFile(t, filepath.Join(root, "excluded_test.go"), "package only\nfunc excluded() int { return 1 }\n")
	writeIntegrationFile(t, filepath.Join(root, "inactive_test.go"), "//go:build inactive\n\npackage only\nfunc inactive() int { return 1 }\n")
	writeIntegrationFile(t, filepath.Join(root, "empty_test.go"), "package only\nvar empty = 1\n")
	built, stderr, code := runFixture(t, root, "--coverage=statement,function", "--include-tests", "--exclude=excluded_test.go", "--strict", "--format=json", "./...")
	if code != ExitSuccess {
		t.Fatalf("exit=%d\n%s", code, stderr)
	}
	if built.Summary.Function.Covered != 1 || built.Summary.Function.Total != 1 {
		t.Fatalf("functions=%+v", built.Summary.Function)
	}
}

func TestTestCounterFailureDoesNotInvalidateASTEvidence(t *testing.T) {
	configureIntegrationEnvironment(t)
	root := t.TempDir()
	writeIntegrationFile(t, filepath.Join(root, "go.mod"), "module example.test/counterfailure\n\ngo 1.26\n")
	writeIntegrationFile(t, filepath.Join(root, "only_test.go"), `package only
import ("testing";"os";"path/filepath")
func TestCounterFailure(t *testing.T) {
 dir:=os.Getenv("GOMCDC_TEST_COVER_DIR")
 files,_:=os.ReadDir(dir)
 for _,file:=range files { os.Remove(filepath.Join(dir,file.Name())) }
 if len(files)==0 { t.Fatal("missing counter files") }
 t.Log("still passes")
}
`)
	built, stderr, code := runFixture(t, root, "--coverage=statement,function,decision", "--include-tests", "--format=json", "./...")
	if code != ExitMeasurementFailed {
		t.Fatalf("exit=%d\n%s", code, stderr)
	}
	if built.Summary.Statement.Unknown == 0 || built.Summary.Statement.Covered != 0 {
		t.Fatalf("counter failure accepted: %+v", built.Summary.Statement)
	}
	ast := findProducerOutcome(t, built, report.ProducerASTRuntime)
	if ast.Usability != report.ProducerUsabilityAccepted {
		t.Fatalf("unrelated AST rejected: %+v", ast)
	}
}
