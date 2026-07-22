package cli

import (
	"testing"

	"github.com/shrydev2020/gomcdc/v2/internal/gotestargs"
)

func parseGoTestArguments(t *testing.T, arguments ...string) gotestargs.Arguments {
	t.Helper()
	parsed, err := gotestargs.Parse(arguments)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
