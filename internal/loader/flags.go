package loader

import (
	"fmt"
	"strings"
)

// BuildFlags extracts go command flags that can change the selected source
// files. Keeping package loading and go test on the same build configuration is
// required for an honest denominator.
func BuildFlags(goTestArgs []string) ([]string, error) {
	var flags []string
	for i := 0; i < len(goTestArgs); i++ {
		arg := goTestArgs[i]
		if arg == "-args" || arg == "--args" {
			break
		}
		if isBooleanBuildFlag(arg) {
			flags = append(flags, arg)
			continue
		}
		name, hasValue := buildFlagName(arg)
		if name == "" {
			continue
		}
		if hasValue {
			flags = append(flags, arg)
			continue
		}
		if i+1 >= len(goTestArgs) {
			return nil, fmt.Errorf("go test argument %s requires a value", arg)
		}
		// Canonicalize valued build flags so each returned element is one
		// semantic flag. This prevents a value beginning with "-" from being
		// mistaken for a following build flag by module-context discovery.
		flags = append(flags, arg+"="+goTestArgs[i+1])
		i++
	}
	return flags, nil
}

func isBooleanBuildFlag(arg string) bool {
	name := strings.TrimLeft(arg, "-")
	base, value, hasValue := strings.Cut(name, "=")
	switch base {
	case "race", "msan", "asan":
		return !hasValue || value == "true" || value == "false"
	}
	return false
}

func buildFlagName(arg string) (name string, hasValue bool) {
	trimmed := strings.TrimLeft(arg, "-")
	for _, candidate := range []string{"tags", "mod", "modfile", "overlay"} {
		if trimmed == candidate {
			return candidate, false
		}
		if strings.HasPrefix(trimmed, candidate+"=") {
			return candidate, true
		}
	}
	return "", false
}
