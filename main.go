// Command gomcdc runs Go tests and reports statement, function, decision,
// condition, clause, Unique-Cause MC/DC, and Masking MC/DC coverage.
package main

import (
	"context"
	"os"

	"github.com/shrydev2020/gomcdc/internal/cli"
)

func main() {
	os.Exit(cli.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
