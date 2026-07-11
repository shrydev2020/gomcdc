package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/shrydev2020/gomcdc/internal/config"
)

type options struct {
	coverage             string
	metrics              config.CoverageSet
	format               string
	output               string
	excludes             stringList
	includeTests         bool
	keepWorkDir          bool
	strict               bool
	workDirParent        string
	timeout              time.Duration
	specialDenom         string
	failUnderStatement   optionalFloat
	failUnderFunction    optionalFloat
	failUnderDecision    optionalFloat
	failUnderClause      optionalFloat
	failUnderCondition   optionalFloat
	failUnderMCDCUnique  optionalFloat
	failUnderMCDCMasking optionalFloat
	patterns             []string
	goTestArgs           []string
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

type optionalFloat struct {
	value float64
	set   bool
}

func (f *optionalFloat) String() string {
	if !f.set {
		return ""
	}
	return strconv.FormatFloat(f.value, 'f', -1, 64)
}

func (f *optionalFloat) Set(value string) error {
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return err
	}
	f.value, f.set = parsed, true
	return nil
}

func parseOptions(args []string, errOut io.Writer) (options, error) {
	toolArgs, goTestArgs := splitGoTestArgs(args)
	var opts options
	fs := flag.NewFlagSet("gocoverage test", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.StringVar(&opts.coverage, "coverage", "all", "coverage metrics or aliases: all,c0,c1,c2,mcdc")
	fs.StringVar(&opts.format, "format", "text", "report format: text or json")
	fs.StringVar(&opts.output, "output", "", "report output file (default: stdout)")
	fs.Var(&opts.excludes, "exclude", "exclude module-relative glob; repeatable; ** is supported")
	fs.BoolVar(&opts.includeTests, "include-tests", false, "include active _test.go decisions in the denominator")
	fs.BoolVar(&opts.keepWorkDir, "keep-workdir", false, "keep the instrumented temporary workspace")
	fs.BoolVar(&opts.strict, "strict", false, "fail when a requested source entity is unsupported, unknown, or not instrumented")
	fs.StringVar(&opts.workDirParent, "workdir", "", "parent directory for the temporary workspace")
	fs.DurationVar(&opts.timeout, "timeout", 10*time.Minute, "maximum duration of the go test subprocess (0 disables)")
	fs.StringVar(&opts.specialDenom, "special-denominator", "exclude", "special-state denominator policy: exclude or include")
	fs.Var(&opts.failUnderStatement, "fail-under-statement", "minimum statement coverage percentage")
	fs.Var(&opts.failUnderFunction, "fail-under-function", "minimum function coverage percentage")
	fs.Var(&opts.failUnderDecision, "fail-under-decision", "minimum decision coverage percentage")
	fs.Var(&opts.failUnderClause, "fail-under-clause", "minimum clause coverage percentage")
	fs.Var(&opts.failUnderCondition, "fail-under-condition", "minimum condition coverage percentage")
	fs.Var(&opts.failUnderMCDCUnique, "fail-under-mcdc-unique", "minimum Unique-Cause MC/DC percentage")
	fs.Var(&opts.failUnderMCDCMasking, "fail-under-mcdc-masking", "minimum Masking MC/DC percentage")
	fs.Usage = func() { writeTestUsage(errOut, fs) }
	if err := fs.Parse(toolArgs); err != nil {
		return options{}, err
	}
	opts.patterns = fs.Args()
	if len(opts.patterns) == 0 {
		opts.patterns = []string{"."}
	}
	for _, pattern := range opts.patterns {
		if strings.HasPrefix(pattern, "-") {
			return options{}, fmt.Errorf("tool flags must appear before package patterns; put go test flags after --: %q", pattern)
		}
	}
	opts.goTestArgs = goTestArgs

	metrics, err := config.ParseCoverage(opts.coverage)
	if err != nil {
		return options{}, err
	}
	opts.metrics = metrics
	if err := validateOptions(opts); err != nil {
		return options{}, err
	}
	return opts, nil
}

func splitGoTestArgs(args []string) (tool, goTest []string) {
	for i, arg := range args {
		if arg == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

func validateOptions(opts options) error {
	if opts.format != "text" && opts.format != "json" {
		return fmt.Errorf("unsupported --format=%q; use text or json", opts.format)
	}
	if opts.timeout < 0 {
		return errors.New("--timeout must be non-negative")
	}
	if opts.specialDenom != "exclude" && opts.specialDenom != "include" {
		return fmt.Errorf("unsupported --special-denominator=%q; use exclude or include", opts.specialDenom)
	}
	thresholds := []struct {
		name   string
		metric config.Metric
		value  optionalFloat
	}{
		{"--fail-under-statement", config.MetricStatement, opts.failUnderStatement},
		{"--fail-under-function", config.MetricFunction, opts.failUnderFunction},
		{"--fail-under-decision", config.MetricDecision, opts.failUnderDecision},
		{"--fail-under-clause", config.MetricClause, opts.failUnderClause},
		{"--fail-under-condition", config.MetricCondition, opts.failUnderCondition},
		{"--fail-under-mcdc-unique", config.MetricMCDCUnique, opts.failUnderMCDCUnique},
		{"--fail-under-mcdc-masking", config.MetricMCDCMasking, opts.failUnderMCDCMasking},
	}
	for _, threshold := range thresholds {
		if err := validateThreshold(threshold.name, threshold.value); err != nil {
			return err
		}
		if threshold.value.set && !opts.metrics.Enabled(threshold.metric) {
			return fmt.Errorf("%s requires --coverage to include %s", threshold.name, threshold.metric)
		}
	}
	return nil
}

func validateThreshold(name string, value optionalFloat) error {
	if value.set && (math.IsNaN(value.value) || math.IsInf(value.value, 0) || value.value < 0 || value.value > 100) {
		return fmt.Errorf("%s must be between 0 and 100", name)
	}
	return nil
}

func writeTopUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: gocoverage test [options] [package patterns] [-- go test arguments]")
	fmt.Fprintln(w, "\nBy default all statement, function, decision, clause, condition, and MC/DC metrics are measured.")
}

func writeTestUsage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprintln(w, "Usage: gocoverage test [options] [package patterns] [-- go test arguments]")
	fmt.Fprintln(w, "\nOptions:")
	fs.PrintDefaults()
}
