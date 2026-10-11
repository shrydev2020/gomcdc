package cli

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/goflags"
)

func TestREADMEForwardedGoTestExampleUsesOnlyAllowedFlags(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"../../README.md", "../../README.ja.md"} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		examples := 0
		for line := range strings.SplitSeq(string(contents), "\n") {
			if !(strings.HasPrefix(line, "gomcdc test ") || strings.HasPrefix(line, "gomcdc record ")) || !strings.Contains(line, " -- ") {
				continue
			}
			examples++
			args, err := goflags.Split(strings.TrimPrefix(line, "gomcdc "))
			if err != nil {
				t.Fatal(err)
			}
			opts, err := parseOptions(args[1:], io.Discard)
			if err != nil {
				t.Fatalf("%s: %v", line, err)
			}
			if conflict := measurementFlag(opts.goTestArgs); conflict != "" {
				t.Fatalf("%s forwards forbidden measurement flag -%s", path, conflict)
			}
		}
		if examples == 0 {
			t.Fatalf("%s has no test-selection example", path)
		}
	}
}

func TestREADMEInstallsLatestV2Module(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"../../README.md", "../../README.ja.md"} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(contents)
		if !strings.Contains(text, "go install github.com/shrydev2020/gomcdc/v2@latest") {
			t.Fatalf("%s does not install the latest v2 module", path)
		}
	}
}
