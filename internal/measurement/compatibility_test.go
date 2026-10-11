package measurement

import (
	"reflect"
	"strings"
	"testing"
)

func TestBuildSettingsPreservesBuildAndBinaryArguments(t *testing.T) {
	for _, test := range []struct {
		name              string
		left, right       []string
		flags, diagnostic string
		equal             bool
	}{
		{name: "test selection", left: []string{"-run=First"}, right: []string{"-test.run=Second"}, equal: true},
		{name: "binary selection", left: []string{"-args", "-run=First"}, right: []string{"-args", "-run=Second"}},
		{name: "unknown option", left: []string{"-custom=first"}, right: []string{"-custom=second"}},
		{name: "build value resembles runtime option", left: []string{"-ldflags", "-run=First"}, right: []string{"-ldflags", "-run=Second"}},
		{name: "missing argument", right: []string{"-run"}, diagnostic: "requires a value"},
		{name: "missing GOFLAGS value", flags: "-run", diagnostic: "requires a value"},
		{name: "unclosed GOFLAGS quote", flags: `-tags="open`, diagnostic: "unterminated"},
	} {
		t.Run(test.name, func(t *testing.T) {
			left, err := BuildSettings(Settings{GoTestArgs: test.left})
			if err != nil {
				t.Fatal(err)
			}
			right, err := BuildSettings(Settings{GoTestArgs: test.right, GOFLAGS: test.flags})
			if test.diagnostic != "" {
				if err == nil || !strings.Contains(err.Error(), test.diagnostic) {
					t.Fatalf("expected %q: %v", test.diagnostic, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if reflect.DeepEqual(left, right) != test.equal {
				t.Fatalf("unexpected normalized flags: %+v %+v", left, right)
			}
		})
	}
}
