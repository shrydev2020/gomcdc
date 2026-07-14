package instrument

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/internal/compileraware"
	cover "github.com/shrydev2020/gomcdc/internal/coverage"
	"github.com/shrydev2020/gomcdc/internal/runtimecov"
)

const semanticFixtureModule = "example.test/gomcdc-semantic-fixture"

// TestInstrumentationModesPreserveProgramSemantics is the common semantic
// contract for both instrumentation producers. The transcript deliberately
// observes values, side-effect order, panic/recover/defer behavior, switch
// selection, fallthrough, and labelled control flow. Runtime evidence is then
// checked separately: instrumentation may add observations, but it may not
// change the program transcript.
func TestInstrumentationModesPreserveProgramSemantics(t *testing.T) {
	originalRoot := t.TempDir()
	originalPath := writeSemanticFixture(t, originalRoot)
	analysis := analyze(
		t,
		originalPath,
		originalRoot,
		semanticFixtureModule,
		semanticFixtureModule+"/logic",
	)
	wantTranscript := runSemanticFixture(t, originalRoot, nil, "")

	tests := []struct {
		name                 string
		compilerAware        bool
		wantDirectSelection  bool
		wantNoMatchSelection bool
		wantBodyExecution    bool
	}{
		{
			name:              "AST",
			wantBodyExecution: true,
		},
		{
			name:                 "compiler-aware",
			compilerAware:        true,
			wantDirectSelection:  true,
			wantNoMatchSelection: true,
			wantBodyExecution:    true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			copyPath := writeSemanticFixture(t, workspace)
			injected, err := runtimecov.Inject(workspace, semanticFixtureModule)
			if err != nil {
				t.Fatal(err)
			}
			_, err = InstrumentPackage(PackageOptions{
				Directory:               filepath.Dir(copyPath),
				PackageName:             "logic",
				PackagePath:             semanticFixtureModule + "/logic",
				RuntimeImportPath:       injected.ImportPath,
				CompilerClauseSelection: test.compilerAware,
				ActiveFiles:             []string{copyPath},
				Files:                   []FileMapping{{CopyPath: copyPath, Analysis: analysis}},
			})
			if err != nil {
				t.Fatal(err)
			}

			dataDir := t.TempDir()
			environment := map[string]string{
				runtimecov.DataDirEnv: dataDir,
				runtimecov.RunIDEnv:   "semantic-differential-" + test.name,
			}
			toolexec := ""
			if test.compilerAware {
				toolchain, err := compileraware.Prepare(context.Background(), t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				toolexec = toolchain.Toolexec
				for key, value := range toolchain.Environment {
					environment[key] = value
				}
			}

			gotTranscript := runSemanticFixture(t, workspace, environment, toolexec)
			if !bytes.Equal(gotTranscript, wantTranscript) {
				t.Fatalf("semantic transcript changed\noriginal:     %s\ninstrumented: %s", wantTranscript, gotTranscript)
			}

			collected, err := runtimecov.CollectDetailed(dataDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(collected.Diagnostics) != 0 {
				t.Fatalf("runtime diagnostics = %#v", collected.Diagnostics)
			}
			if len(collected.Evaluations) == 0 {
				t.Fatal("instrumentation produced no decision evaluations")
			}
			assertSemanticClauseEvents(
				t,
				collected.Clauses,
				test.wantDirectSelection,
				test.wantNoMatchSelection,
				test.wantBodyExecution,
			)
		})
	}
}

func writeSemanticFixture(t *testing.T, root string) string {
	t.Helper()
	writeFile(t, root, "go.mod", "module "+semanticFixtureModule+"\n\ngo 1.26.0\n")
	logicPath := writeFile(t, root, "logic/logic.go", semanticFixtureLogic)
	writeFile(t, root, "cmd/transcript/main.go", semanticFixtureMain)
	return logicPath
}

func runSemanticFixture(t *testing.T, root string, extraEnvironment map[string]string, toolexec string) []byte {
	t.Helper()
	arguments := []string{"run"}
	if toolexec != "" {
		arguments = append(arguments, "-toolexec="+toolexec)
	}
	arguments = append(arguments, "./cmd/transcript")
	command := exec.Command("go", arguments...)
	command.Dir = root
	command.Env = semanticEnvironment(os.Environ(), "GOWORK", "off")
	for key, value := range extraEnvironment {
		command.Env = semanticEnvironment(command.Env, key, value)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err != nil {
		t.Fatalf("semantic fixture failed: %v\n%s", err, stderr.Bytes())
	}
	return bytes.TrimSpace(stdout.Bytes())
}

func semanticEnvironment(environment []string, key, value string) []string {
	result := append([]string(nil), environment...)
	prefix := key + "="
	for index, item := range result {
		if strings.HasPrefix(item, prefix) {
			result[index] = prefix + value
			return result
		}
	}
	return append(result, prefix+value)
}

func assertSemanticClauseEvents(t *testing.T, observations []cover.ClauseObservation, wantDirect, wantNoMatch, wantBody bool) {
	t.Helper()
	got := map[cover.ClauseEventKind]bool{}
	for _, observation := range observations {
		got[observation.Event] = true
	}
	for event, want := range map[cover.ClauseEventKind]bool{
		cover.ClauseDirectSelection:  wantDirect,
		cover.ClauseNoMatchSelection: wantNoMatch,
		cover.ClauseBodyExecution:    wantBody,
	} {
		if got[event] != want {
			t.Errorf("clause event %q present = %t, want %t; observations = %#v", event, got[event], want, observations)
		}
	}
}

const semanticFixtureLogic = `package logic

import "fmt"

var events []string

func Reset() { events = nil }

func Events() []string { return append([]string(nil), events...) }

func mark(label string, value bool) bool {
	events = append(events, fmt.Sprintf("%s=%t", label, value))
	return value
}

func markInt(label string, value int) int {
	events = append(events, fmt.Sprintf("%s=%d", label, value))
	return value
}

func ShortCircuit(left, right bool) bool {
	return mark("and-left", left) && mark("and-right", right)
}

func ExpressionSwitch(value int) int {
	switch markInt("switch-tag", value) {
	case markInt("case-one", 1), markInt("case-two", 2):
		events = append(events, "switch-pair")
		return 10
	case markInt("case-three", 3):
		fallthrough
	default:
		events = append(events, "switch-tail")
		return 20
	}
}

func NoMatch(value int) int {
	switch value {
	case 1:
		return 1
	case 2:
		return 2
	}
	events = append(events, "switch-no-match")
	return 0
}

func TypeSwitch(value any) string {
	switch typed := value.(type) {
	case nil:
		return "nil"
	case int:
		return fmt.Sprintf("int:%d", typed)
	case string, bool:
		return fmt.Sprintf("scalar:%v", typed)
	default:
		return fmt.Sprintf("other:%T", typed)
	}
}

func Labelled(value int) int {
	total := 0
outer:
	for index := 0; index < 4; index++ {
		switch value + index {
		case 1:
			total++
			continue outer
		case 2:
			total += 2
			break outer
		default:
			total += 4
		}
	}
	return total
}

func PanicFlow(shouldPanic bool) (result string) {
	defer func() {
		events = append(events, "defer")
		if recovered := recover(); recovered != nil {
			result = fmt.Sprintf("recovered:%v", recovered)
		}
	}()
	if mark("panic-condition", shouldPanic) {
		panic("boom")
	}
	return "returned"
}
`

const semanticFixtureMain = `package main

import (
	"encoding/json"
	"os"

	"example.test/gomcdc-semantic-fixture/logic"
)

type transcript struct {
	ShortCircuit []bool   ` + "`json:\"shortCircuit\"`" + `
	Switch       []int    ` + "`json:\"switch\"`" + `
	NoMatch      int      ` + "`json:\"noMatch\"`" + `
	Types        []string ` + "`json:\"types\"`" + `
	Labelled     []int    ` + "`json:\"labelled\"`" + `
	Panics       []string ` + "`json:\"panics\"`" + `
	Events       []string ` + "`json:\"events\"`" + `
}

func main() {
	logic.Reset()
	result := transcript{
		ShortCircuit: []bool{
			logic.ShortCircuit(false, true),
			logic.ShortCircuit(true, false),
			logic.ShortCircuit(true, true),
		},
		Switch: []int{
			logic.ExpressionSwitch(2),
			logic.ExpressionSwitch(3),
			logic.ExpressionSwitch(9),
		},
		NoMatch: logic.NoMatch(9),
		Types: []string{
			logic.TypeSwitch(nil),
			logic.TypeSwitch(7),
			logic.TypeSwitch("text"),
			logic.TypeSwitch(1.5),
		},
		Labelled: []int{logic.Labelled(0), logic.Labelled(3)},
		Panics:   []string{logic.PanicFlow(true), logic.PanicFlow(false)},
	}
	result.Events = logic.Events()
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		panic(err)
	}
}
`
