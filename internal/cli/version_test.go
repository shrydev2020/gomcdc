package cli

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/shrydev2020/gomcdc/internal/buildinfo"
)

func TestVersionCommand(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"version"}, &stdout, &stderr)
	if code != ExitSuccess || stdout.String() != fmt.Sprintf("gomcdc %s\n", buildinfo.Version()) || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestVersionCommandRejectsArguments(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"version", "unexpected"}, &stdout, &stderr)
	if code != ExitInvalidUsage || stdout.Len() != 0 || stderr.String() != "gomcdc: version does not accept arguments\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
