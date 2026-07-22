// Package gotestargs interprets the arguments that follow gomcdc's `--`
// separator using the Go 1.26 `go test` flag grammar.
package gotestargs

import (
	"fmt"
	"strings"
)

type flagArity uint8

const (
	booleanFlag flagArity = iota
	valuedFlag
)

type element struct {
	raw  []string
	flag *Flag
}

// Flag is one semantic flag from the go test argument stream. A valued flag
// owns its following token even when that token starts with a hyphen.
type Flag struct {
	name      string
	value     string
	hasValue  bool
	canonical string
}

// Name returns the normalized Go flag name without leading hyphens.
func (flag Flag) Name() string { return flag.name }

// CanonicalName removes the `test.` prefix only for aliases registered by the
// Go 1.26 test command. Unknown flags retain their exact name.
func (flag Flag) CanonicalName() string { return flag.canonical }

// Value returns the flag value and whether it was supplied. Bare Boolean flags
// have no supplied value.
func (flag Flag) Value() (string, bool) { return flag.value, flag.hasValue }

// Normalized returns a one-token spelling suitable for forwarding as a build
// flag. Valued flags use the unambiguous -name=value form.
func (flag Flag) Normalized() string {
	if flag.hasValue {
		return "-" + flag.name + "=" + flag.value
	}
	return "-" + flag.name
}

// Arguments is a parsed go test argument stream. Its prefix may contain both
// Go flags and unknown test-binary flags because cmd/go continues scanning for
// known flags after an unknown flag. BinaryArgs begins only at the boundary
// selected by cmd/go's grammar.
type Arguments struct {
	elements       []element
	binaryArgs     []string
	binaryBoundary bool
}

// Parse interprets arguments that will be placed after the package list in a
// Go 1.26 `go test` invocation.
func Parse(arguments []string) (Arguments, error) {
	parsed := Arguments{}
	afterUnknownFlagWithoutValue := false
	for index := 0; index < len(arguments); index++ {
		raw := arguments[index]
		wasAfterUnknownFlagWithoutValue := afterUnknownFlagWithoutValue
		afterUnknownFlagWithoutValue = false

		if raw == "--" {
			parsed.binaryBoundary = true
			parsed.binaryArgs = append([]string{"--"}, arguments[index+1:]...)
			return parsed, nil
		}

		name, value, hasValue, isFlag := splitFlag(raw)
		if !isFlag {
			if wasAfterUnknownFlagWithoutValue {
				parsed.elements = append(parsed.elements, element{raw: []string{raw}})
				continue
			}
			parsed.binaryBoundary = true
			parsed.binaryArgs = append([]string(nil), arguments[index:]...)
			return parsed, nil
		}

		arity, known := go126FlagArities[name]
		if !known {
			if raw == "-args" || raw == "--args" {
				parsed.binaryBoundary = true
				parsed.binaryArgs = append([]string(nil), arguments[index+1:]...)
				return parsed, nil
			}
			parsed.elements = append(parsed.elements, newFlagElement(raw, name, value, hasValue, false))
			afterUnknownFlagWithoutValue = !hasValue
			continue
		}

		if arity == booleanFlag {
			parsed.elements = append(parsed.elements, newFlagElement(raw, name, value, hasValue, true))
			continue
		}
		if hasValue {
			parsed.elements = append(parsed.elements, newFlagElement(raw, name, value, true, true))
			continue
		}
		if index+1 >= len(arguments) {
			return Arguments{}, fmt.Errorf("go test argument %s requires a value", raw)
		}
		index++
		value = arguments[index]
		parsed.elements = append(parsed.elements, newValuedFlagElement(raw, value, name))
	}
	return parsed, nil
}

func newFlagElement(raw, name, value string, hasValue, known bool) element {
	return element{
		raw: []string{raw},
		flag: &Flag{
			name: name, value: value, hasValue: hasValue,
			canonical: canonicalName(name, known),
		},
	}
}

func newValuedFlagElement(raw, value, name string) element {
	return element{
		raw: []string{raw, value},
		flag: &Flag{
			name: name, value: value, hasValue: true,
			canonical: canonicalName(name, true),
		},
	}
}

func splitFlag(raw string) (name, value string, hasValue, ok bool) {
	argument := raw
	if strings.HasPrefix(argument, "--") {
		argument = argument[1:]
	}
	if len(argument) < 2 || argument[0] != '-' || argument[1] == '-' || argument[1] == '=' {
		return "", "", false, false
	}
	name, value, hasValue = strings.Cut(argument[1:], "=")
	return name, value, hasValue, true
}

func canonicalName(name string, known bool) string {
	if known && strings.HasPrefix(name, "test.") {
		return strings.TrimPrefix(name, "test.")
	}
	return name
}

// Flags returns the semantic flags before the test-binary argument boundary.
func (arguments Arguments) Flags() []Flag {
	flags := make([]Flag, 0, len(arguments.elements))
	for _, item := range arguments.elements {
		if item.flag != nil {
			flags = append(flags, *item.flag)
		}
	}
	return flags
}

// Prefix returns the original tokens that cmd/go continues to scan before the
// test-binary argument boundary.
func (arguments Arguments) Prefix() []string {
	var result []string
	for _, item := range arguments.elements {
		result = append(result, item.raw...)
	}
	return result
}

// BinaryArgs returns the test-binary arguments and whether cmd/go observed an
// explicit or implicit binary-argument boundary.
func (arguments Arguments) BinaryArgs() ([]string, bool) {
	return append([]string(nil), arguments.binaryArgs...), arguments.binaryBoundary
}

// Without removes semantic flags by canonical name without reinterpreting any
// remaining raw token.
func (arguments Arguments) Without(names ...string) Arguments {
	remove := make(map[string]struct{}, len(names))
	for _, name := range names {
		remove[name] = struct{}{}
	}
	result := Arguments{
		binaryArgs:     append([]string(nil), arguments.binaryArgs...),
		binaryBoundary: arguments.binaryBoundary,
	}
	for _, item := range arguments.elements {
		if item.flag != nil {
			if _, excluded := remove[item.flag.canonical]; excluded {
				continue
			}
		}
		result.elements = append(result.elements, item)
	}
	return result
}

// WithValue replaces every semantic occurrence of name and appends one
// request-owned valued flag before the test-binary argument boundary.
func (arguments Arguments) WithValue(name, value string) Arguments {
	if arity, known := go126FlagArities[name]; !known || arity != valuedFlag {
		panic(fmt.Sprintf("gotestargs: WithValue requires a known valued Go 1.26 flag, got %q", name))
	}
	result := arguments.Without(name)
	result.elements = append(result.elements, newFlagElement(
		"-"+name+"="+value, name, value, true, true,
	))
	return result
}

var go126FlagArities = func() map[string]flagArity {
	flags := map[string]flagArity{}
	add := func(arity flagArity, names ...string) {
		for _, name := range names {
			flags[name] = arity
		}
	}
	add(booleanFlag,
		"n", "x", "a", "asan", "buildvcs", "modcacherw", "linkshared", "msan", "race", "trimpath", "work",
		"c", "json", "cover", "artifacts", "benchmem", "failfast", "fullpath", "short", "v",
	)
	add(valuedFlag,
		"C", "p", "asmflags", "compiler", "buildmode", "gcflags", "gccgoflags", "mod", "modfile", "overlay",
		"installsuffix", "ldflags", "pgo", "pkgdir", "tags", "toolexec", "debug-actiongraph", "debug-runtime-trace", "debug-trace",
		"o", "covermode", "coverpkg", "coverprofile", "exec", "vet", "bench", "benchtime", "blockprofile",
		"blockprofilerate", "count", "cpu", "cpuprofile", "fuzz", "list", "memprofile", "memprofilerate",
		"mutexprofile", "mutexprofilefraction", "outputdir", "parallel", "run", "skip", "timeout", "fuzztime",
		"fuzzminimizetime", "trace", "shuffle",
	)
	for _, name := range []string{
		"artifacts", "bench", "benchmem", "benchtime", "blockprofile", "blockprofilerate", "count", "coverprofile",
		"cpu", "cpuprofile", "failfast", "fullpath", "fuzz", "fuzzminimizetime", "fuzztime", "list", "memprofile",
		"memprofilerate", "mutexprofile", "mutexprofilefraction", "outputdir", "parallel", "run", "short", "shuffle",
		"skip", "timeout", "trace", "v",
	} {
		flags["test."+name] = flags[name]
	}
	return flags
}()
