package measurement

import (
	"fmt"
	"slices"

	"github.com/shrydev2020/gomcdc/v2/internal/goflags"
	"github.com/shrydev2020/gomcdc/v2/internal/gotestargs"
)

// Test-selection and runtime scheduling flags can differ between shards. All
// other flags (including unknown flags and binary arguments) must match; they
// cannot accidentally weaken build/source compatibility checks.
func BuildSettings(settings Settings) (Settings, error) {
	settings.InvocationID = ""
	runtimeFlags := []string{"run", "skip", "count", "shuffle", "parallel", "cpu", "timeout", "failfast", "short", "bench", "benchtime", "benchmem", "v", "json", "list", "fullpath"}
	arguments, err := gotestargs.Parse(settings.GoTestArgs)
	if err != nil {
		return Settings{}, fmt.Errorf("parse test arguments: %w", err)
	}
	arguments = arguments.Without(runtimeFlags...)
	settings.GoTestArgs = arguments.Prefix()
	if binary, boundary := arguments.BinaryArgs(); boundary {
		settings.GoTestArgs = append(settings.GoTestArgs, "-args")
		settings.GoTestArgs = append(settings.GoTestArgs, binary...)
	}
	words, err := goflags.Split(settings.GOFLAGS)
	if err != nil {
		return Settings{}, fmt.Errorf("split GOFLAGS: %w", err)
	}
	flags, err := gotestargs.Parse(words)
	if err != nil {
		return Settings{}, fmt.Errorf("parse GOFLAGS: %w", err)
	}
	settings.GOFLAGS = goflags.Join(flags.Without(runtimeFlags...).Prefix())
	settings.PackagePatterns = nil
	settings.Timeout = ""
	settings.Strict = false
	settings.FailUnder = nil
	settings.Packages = slices.Sorted(slices.Values(settings.Packages))
	settings.Coverage = slices.Sorted(slices.Values(settings.Coverage))
	settings.Excludes = slices.Sorted(slices.Values(settings.Excludes))
	return settings, nil
}
