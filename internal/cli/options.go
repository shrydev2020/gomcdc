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

	"github.com/shrydev2020/gomcdc/v2/internal/config"
	"github.com/shrydev2020/gomcdc/v2/internal/gotestargs"
	"github.com/shrydev2020/gomcdc/v2/internal/mcdc"
)

type options struct {
	coverage                           string
	metrics                            config.CoverageSet
	format                             string
	output                             string
	excludes                           stringList
	includeTests                       bool
	keepWorkDir                        bool
	strict                             bool
	workDirParent                      string
	timeout                            time.Duration
	failUnderStatement                 optionalFloat
	failUnderFunction                  optionalFloat
	failUnderDecision                  optionalFloat
	failUnderSwitchClauseBody          optionalFloat
	failUnderTypeSwitchClauseBody      optionalFloat
	failUnderSelectClauseBody          optionalFloat
	failUnderSwitchClauseSelection     optionalFloat
	failUnderTypeSwitchClauseSelection optionalFloat
	failUnderCondition                 optionalFloat
	failUnderMCDCUnique                optionalFloat
	failUnderMCDCMasking               optionalFloat
	maskingMaxEvaluationPairs          optionalUint64
	maskingMaxSearchStates             optionalUint64
	maskingMaxSolverBytes              optionalUint64
	patterns                           []string
	goTestArgs                         gotestargs.Arguments
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

type optionalUint64 struct {
	value uint64
	set   bool
}

func (u *optionalUint64) String() string {
	if !u.set {
		return ""
	}
	return strconv.FormatUint(u.value, 10)
}

func (u *optionalUint64) Set(value string) error {
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return err
	}
	u.value, u.set = parsed, true
	return nil
}

func parseOptions(args []string, errOut io.Writer) (options, error) {
	toolArgs, goTestArgs := splitGoTestArgs(args)
	var opts options
	fs := flag.NewFlagSet("gomcdc test", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.StringVar(&opts.coverage, "coverage", "all", "coverage metrics: all,statement,function,decision,switch-clause-body,type-switch-clause-body,select-clause-body,switch-clause-selection,type-switch-clause-selection,condition,mcdc-unique,mcdc-masking")
	fs.StringVar(&opts.format, "format", "text", "report format: text, json, or html")
	fs.StringVar(&opts.output, "output", "", "report output file, or directory for html (default: stdout)")
	fs.Var(&opts.excludes, "exclude", "exclude module-relative glob; repeatable; ** is supported")
	fs.BoolVar(&opts.includeTests, "include-tests", false, "include active _test.go decisions in the denominator")
	fs.BoolVar(&opts.keepWorkDir, "keep-workdir", false, "keep the instrumented temporary workspace")
	fs.BoolVar(&opts.strict, "strict", false, "fail when a requested source entity is unsupported, unknown, analysis-incomplete, or not instrumented")
	fs.StringVar(&opts.workDirParent, "workdir", "", "parent directory for the temporary workspace")
	fs.DurationVar(&opts.timeout, "timeout", 10*time.Minute, "maximum duration of the go test subprocess (0 disables)")
	fs.Var(&opts.failUnderStatement, "fail-under-statement", "minimum statement coverage percentage")
	fs.Var(&opts.failUnderFunction, "fail-under-function", "minimum function coverage percentage")
	fs.Var(&opts.failUnderDecision, "fail-under-decision", "minimum decision coverage percentage")
	fs.Var(&opts.failUnderSwitchClauseBody, "fail-under-switch-clause-body", "minimum switch clause body coverage percentage")
	fs.Var(&opts.failUnderTypeSwitchClauseBody, "fail-under-type-switch-clause-body", "minimum type switch clause body coverage percentage")
	fs.Var(&opts.failUnderSelectClauseBody, "fail-under-select-clause-body", "minimum select clause body coverage percentage")
	fs.Var(&opts.failUnderSwitchClauseSelection, "fail-under-switch-clause-selection", "minimum switch clause selection coverage percentage")
	fs.Var(&opts.failUnderTypeSwitchClauseSelection, "fail-under-type-switch-clause-selection", "minimum type switch clause selection coverage percentage")
	fs.Var(&opts.failUnderCondition, "fail-under-condition", "minimum condition coverage percentage")
	fs.Var(&opts.failUnderMCDCUnique, "fail-under-mcdc-unique", "minimum Unique-Cause MC/DC percentage")
	fs.Var(&opts.failUnderMCDCMasking, "fail-under-mcdc-masking", "minimum Masking MC/DC percentage")
	fs.Var(&opts.maskingMaxEvaluationPairs, "mcdc-masking-max-evaluation-pairs", "maximum candidate evaluation pairs per Masking MC/DC condition obligation")
	fs.Var(&opts.maskingMaxSearchStates, "mcdc-masking-max-search-states", "maximum newly expanded search states per Masking MC/DC condition obligation")
	fs.Var(&opts.maskingMaxSolverBytes, "mcdc-masking-max-solver-bytes", "maximum primary solver backing-array bytes per Masking MC/DC condition obligation")
	fs.Usage = func() { writeTestUsage(errOut, fs) }
	if err := fs.Parse(toolArgs); err != nil {
		return options{}, err
	}
	opts.patterns = fs.Args()
	if len(opts.patterns) == 0 {
		return options{}, errors.New("at least one package pattern is required")
	}
	for _, pattern := range opts.patterns {
		if strings.HasPrefix(pattern, "-") {
			return options{}, fmt.Errorf("tool flags must appear before package patterns; put go test flags after --: %q", pattern)
		}
	}
	parsedGoTestArgs, err := gotestargs.Parse(goTestArgs)
	if err != nil {
		return options{}, err
	}
	opts.goTestArgs = parsedGoTestArgs

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
	if opts.format != "text" && opts.format != "json" && opts.format != "html" {
		return fmt.Errorf("unsupported --format=%q; use text, json, or html", opts.format)
	}
	if opts.format == "html" && (opts.output == "" || opts.output == "-") {
		return errors.New("--format=html requires --output to name a directory")
	}
	if opts.timeout < 0 {
		return errors.New("--timeout must be non-negative")
	}
	thresholds := []struct {
		name   string
		metric config.Metric
		value  optionalFloat
	}{
		{"--fail-under-statement", config.MetricStatement, opts.failUnderStatement},
		{"--fail-under-function", config.MetricFunction, opts.failUnderFunction},
		{"--fail-under-decision", config.MetricDecision, opts.failUnderDecision},
		{"--fail-under-switch-clause-body", config.MetricSwitchClauseBody, opts.failUnderSwitchClauseBody},
		{"--fail-under-type-switch-clause-body", config.MetricTypeSwitchClauseBody, opts.failUnderTypeSwitchClauseBody},
		{"--fail-under-select-clause-body", config.MetricSelectClauseBody, opts.failUnderSelectClauseBody},
		{"--fail-under-switch-clause-selection", config.MetricSwitchClauseSelection, opts.failUnderSwitchClauseSelection},
		{"--fail-under-type-switch-clause-selection", config.MetricTypeSwitchClauseSelection, opts.failUnderTypeSwitchClauseSelection},
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
	maskingLimits := []struct {
		name  string
		value optionalUint64
	}{
		{"--mcdc-masking-max-evaluation-pairs", opts.maskingMaxEvaluationPairs},
		{"--mcdc-masking-max-search-states", opts.maskingMaxSearchStates},
		{"--mcdc-masking-max-solver-bytes", opts.maskingMaxSolverBytes},
	}
	for _, limit := range maskingLimits {
		if limit.value.set && limit.value.value == 0 {
			return fmt.Errorf("%s must be greater than zero", limit.name)
		}
		if limit.value.set && !opts.metrics.Enabled(config.MetricMCDCMasking) {
			return fmt.Errorf("%s requires --coverage to include %s", limit.name, config.MetricMCDCMasking)
		}
	}
	return nil
}

func (opts options) maskingAnalysisBudget() mcdc.AnalysisBudget {
	return mcdc.AnalysisBudget{
		MaxEvaluationPairs: opts.maskingMaxEvaluationPairs.value,
		MaxSearchStates:    opts.maskingMaxSearchStates.value,
		MaxSolverBytes:     opts.maskingMaxSolverBytes.value,
	}
}

func validateThreshold(name string, value optionalFloat) error {
	if value.set && (math.IsNaN(value.value) || math.IsInf(value.value, 0) || value.value < 0 || value.value > 100) {
		return fmt.Errorf("%s must be between 0 and 100", name)
	}
	return nil
}

func writeTopUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  gomcdc test [options] [package patterns] [-- go test arguments]")
	fmt.Fprintln(w, "  gomcdc version")
	fmt.Fprintln(w, "\nBy default all canonical coverage metrics are measured.")
}

func writeTestUsage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprintln(w, "Usage: gomcdc test [options] [package patterns] [-- go test arguments]")
	fmt.Fprintln(w, "\nOptions:")
	fs.PrintDefaults()
}
