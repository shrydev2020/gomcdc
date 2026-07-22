package cli

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestREADMEForwardedGoTestExampleUsesOnlyAllowedFlags(t *testing.T) {
	t.Parallel()
	const example = "gomcdc test ./... -- -run TestCritical"
	for _, path := range []string{"../../README.md", "../../README.ja.md"} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(contents), example) {
			t.Fatalf("%s does not contain the checked forwarding example %q", path, example)
		}

		arguments := strings.Fields(strings.TrimPrefix(example, "gomcdc "))
		opts, err := parseOptions(arguments[1:], io.Discard)
		if err != nil {
			t.Fatalf("parse %s example: %v", path, err)
		}
		if conflict := measurementFlag(opts.goTestArgs); conflict != "" {
			t.Fatalf("%s example forwards forbidden measurement flag -%s", path, conflict)
		}
	}
}

func TestREADMEInstallsVersionedV2Module(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"../../README.md", "../../README.ja.md"} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(contents)
		if !strings.Contains(text, "go install github.com/shrydev2020/gomcdc/v2@v2.0.0") {
			t.Fatalf("%s does not install the versioned v2 module", path)
		}
	}
}
