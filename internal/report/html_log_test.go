package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/analysis"
)

func TestHTMLEvaluationLogRetainsAllObservationsWithoutPerEventElements(t *testing.T) {
	t.Parallel()
	value := analysis.Result{Packages: []analysis.PackageReport{{Path: "m/p", Files: []analysis.FileReport{{Path: "p.go", Functions: []analysis.FunctionReport{{
		Decisions: []analysis.DecisionReport{{DecisionID: "d", Conditions: []analysis.ConditionReport{{Index: 0, Expression: "enabled"}}}},
	}}}}}}}
	decision := &value.Packages[0].Files[0].Functions[0].Decisions[0]
	var smallElementCount int
	for _, count := range []int{1, 10_000} {
		decision.Evaluations = make([]analysis.EvaluationReport, count)
		for index := range count {
			// Distinct test and process identities exercise per-observation output.
			decision.Evaluations[index] = analysis.EvaluationReport{
				EvaluationID: fmt.Sprintf("0x%016x", index+1), Status: "completed", TestID: fmt.Sprintf("TestCase%d<script>", index),
				Conditions: []string{"true"}, Result: true, RunID: "run", PackagePath: "m/p", ProcessID: index,
			}
		}
		original, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		if err := WriteHTMLReport(&output, value); err != nil {
			t.Fatal(err)
		}
		text := output.String()
		if strings.Contains(text, "<script>") {
			t.Fatal("log fields were not escaped")
		}
		log := regexp.MustCompile(`(?s)<pre class="evaluation-log">(.*?)</pre>`).FindStringSubmatch(text)
		if len(log) != 2 {
			t.Fatal("evaluation log missing")
		}
		lines := strings.Split(html.UnescapeString(log[1]), "\n")
		if len(lines) != count+1 || !strings.Contains(lines[0], "#0 enabled") {
			t.Fatalf("log contains %d lines, want %d observations and a named header", len(lines), count)
		}
		for index, line := range lines[1:] {
			fields := strings.Split(line, "\t")
			want := []string{fmt.Sprintf("0x%016x", index+1), fmt.Sprintf("TestCase%d<script>", index), "completed", "true", "true", "run", "m/p", strconv.Itoa(index)}
			if strings.Join(fields, "|") != strings.Join(want, "|") {
				t.Fatalf("observation %d lost its values or provenance: %q", index, line)
			}
		}
		got := len(regexp.MustCompile(`<\w+\b[^>]*>`).FindAllString(text, -1))
		if count == 1 {
			smallElementCount = got
		} else if got != smallElementCount {
			t.Fatalf("HTML elements grew from %d to %d with evaluation count", smallElementCount, got)
		}
		after, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(original, after) {
			t.Fatal("HTML rendering changed the original evidence")
		}
	}
}

func TestHTMLLogFieldsPreserveEmbeddedDelimiters(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"normal", "test\tname", "line\nbreak", "carriage\rreturn", `quote"name`, `back\slash`, "日本語"} {
		encoded := htmlLogField(value)
		if strings.ContainsAny(encoded, "\t\r\n") {
			t.Fatalf("field introduced a log delimiter: %q", encoded)
		}
		decoded := encoded
		if strings.HasPrefix(encoded, `"`) {
			var err error
			decoded, err = strconv.Unquote(encoded)
			if err != nil {
				t.Fatal(err)
			}
		}
		if decoded != value {
			t.Fatalf("field round trip = %q, want %q", decoded, value)
		}
	}
}
