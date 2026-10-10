package cli

import (
	"path/filepath"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/report"
)

func TestEvaluationTestIDsIncludeParallelSubtestsWorkersAndCleanup(t *testing.T) {
	root := t.TempDir()
	writeIntegrationFile(t, filepath.Join(root, "go.mod"), "module example.test/attribution\n\ngo 1.26.0\n")
	writeIntegrationFile(t, filepath.Join(root, "logic.go"), `package attribution

func Check(value bool) bool {
	if value { return true }
	return false
}

func init() { Check(false) }
`)
	writeIntegrationFile(t, filepath.Join(root, "logic_test.go"), `package attribution_test

import (
	"sync"
	tt "testing"
	logic "example.test/attribution"
)

type TestContext = tt.T

func namedChild(t *TestContext) {
	t.Parallel()
	var workers sync.WaitGroup
	workers.Go(func() { logic.Check(true) })
	workers.Wait()
	t.Cleanup(func() { logic.Check(false) })
}

func TestFirst(t *tt.T) {
	t.Parallel()
	logic.Check(true)
	t.Run("child", namedChild)
	t.Cleanup(func() { logic.Check(false) })
}

func TestSecond(t *tt.T) {
	t.Parallel()
	var workers sync.WaitGroup
	workers.Go(func() { logic.Check(true) })
	workers.Wait()
	t.Run("child", func(t *tt.T) {
		t.Parallel()
		logic.Check(false)
		t.Run("leaf", func(t *tt.T) { logic.Check(true) })
	})
}

func TestUnnamed(*tt.T) { logic.Check(true) }
func TestBlank(_ *tt.T) { logic.Check(true) }

func ExampleCheck() {
	logic.Check(true)
	// Output:
}
`)
	for _, includeTests := range []bool{false, true} {
		t.Run(map[bool]string{false: "production metrics", true: "including test metrics"}[includeTests], func(t *testing.T) {
			args := []string{"--strict", "--format=json", "./...", "--", "-race", "-parallel=8"}
			if includeTests {
				args = append([]string{"--include-tests"}, args...)
			}
			built, stderr, code := runFixture(t, root, args...)
			if code != ExitSuccess || built.Run.Results.Strict != report.ResultPassed {
				t.Fatalf("attribution failed: exit=%d results=%+v\n%s", code, built.Run.Results, stderr)
			}
			got := make(map[string]map[bool]bool)
			for _, pkg := range built.Packages {
				for _, file := range pkg.Files {
					for _, function := range file.Functions {
						if function.Name != "Check" {
							continue
						}
						for _, decision := range function.Decisions {
							for _, evaluation := range decision.Evaluations {
								if got[evaluation.TestID] == nil {
									got[evaluation.TestID] = make(map[bool]bool)
								}
								got[evaluation.TestID][evaluation.Result] = true
							}
							for _, condition := range decision.Conditions {
								if condition.MCDCMasking.Witness != nil {
									witness := condition.MCDCMasking.Witness
									if witness.First.TestID == "" || witness.Second.TestID == "" {
										t.Fatal("witness lost test identity")
									}
								}
							}
						}
					}
				}
			}
			want := map[string][]bool{
				"unknown": {false}, "TestFirst": {true, false}, "TestFirst/child": {true, false},
				"TestSecond": {true}, "TestSecond/child": {false}, "TestSecond/child/leaf": {true},
				"TestUnnamed": {true}, "TestBlank": {true}, "ExampleCheck": {true},
			}
			if len(got) != len(want) {
				t.Fatalf("test identities = %v, want %v", got, want)
			}
			for name, results := range want {
				if len(got[name]) != len(results) {
					t.Errorf("%s results = %v, want %v", name, got[name], results)
				}
				for _, result := range results {
					if !got[name][result] {
						t.Errorf("%s missing result %t: %v", name, result, got[name])
					}
				}
			}
		})
	}
}
