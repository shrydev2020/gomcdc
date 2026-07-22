package loader

import (
	"github.com/shrydev2020/gomcdc/v2/internal/gotestargs"
)

// BuildFlags extracts go command flags that can change the selected source
// files. Keeping package loading and go test on the same build configuration is
// required for an honest denominator.
func BuildFlags(arguments gotestargs.Arguments) []string {
	var flags []string
	for _, flag := range arguments.Flags() {
		switch flag.CanonicalName() {
		case "race", "msan", "asan", "tags", "mod", "modfile", "overlay":
			flags = append(flags, flag.Normalized())
		}
	}
	return flags
}
