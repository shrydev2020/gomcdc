package report

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/shrydev2020/gomcdc/internal/backend"
	cover "github.com/shrydev2020/gomcdc/internal/coverage"
)

// RenderText builds input and returns a deterministic current-schema text report
// ending in a newline.
func RenderText(input Input) string {
	return RenderTextReport(Build(input))
}

// RenderTextReport renders an already-built report without rebuilding it.
func RenderTextReport(report Report) string { return renderText(report) }

// WriteText builds input and writes a deterministic current-schema text report.
func WriteText(writer io.Writer, input Input) error {
	_, err := io.WriteString(writer, RenderText(input))
	return err
}

// WriteTextReport writes an already-built report without rebuilding it.
func WriteTextReport(writer io.Writer, report Report) error {
	_, err := io.WriteString(writer, RenderTextReport(report))
	return err
}

func renderText(report Report) string {
	var output strings.Builder
	fmt.Fprintf(&output, "gomcdc %s report schema %s\n", report.ToolVersion, report.SchemaVersion)
	fmt.Fprintf(&output, "Module: %s\n", report.Module)
	fmt.Fprintf(&output, "Run: %s failure-kind=%s (%s)\n", report.Run.Status, report.Run.FailureKind, completeness(report.Run.Complete))
	fmt.Fprintf(
		&output,
		"Results: test=%s measurement=%s integrity=%s strict=%s threshold=%s\n",
		report.Run.Results.Test,
		report.Run.Results.Measurement,
		report.Run.Results.Integrity,
		report.Run.Results.Strict,
		report.Run.Results.Threshold,
	)
	fmt.Fprintf(&output, "Measurement mode: %s\n", report.MeasurementMode)
	for _, measurement := range report.Measurements {
		fmt.Fprintf(
			&output,
			"Measurement: %s status=%s failure-kind=%s complete=%t\n",
			measurement.Name,
			measurement.Run.Status,
			measurement.Run.FailureKind,
			measurement.Run.Complete,
		)
		packagePaths := make([]string, 0, len(measurement.Packages))
		for packagePath := range measurement.Packages {
			packagePaths = append(packagePaths, packagePath)
		}
		sort.Strings(packagePaths)
		for _, packagePath := range packagePaths {
			fmt.Fprintf(&output, "  Measurement package: %s status=%s\n", packagePath, measurement.Packages[packagePath])
		}
	}
	for _, outcome := range report.ProducerOutcomes {
		fmt.Fprintf(
			&output,
			"Producer: %s integrity=%s completeness=%s mapping=%s usability=%s\n",
			outcome.Producer,
			outcome.Integrity,
			outcome.Completeness,
			outcome.Mapping,
			outcome.Usability,
		)
	}
	output.WriteString("Backend capabilities:\n")
	for _, producer := range report.Backends {
		fmt.Fprintf(&output, "  Backend: %s\n", producer.Backend)
		writeCapabilities(&output, "    ", producer.Capabilities)
	}
	output.WriteString("Aggregate capabilities:\n")
	writeCapabilities(&output, "  ", report.Capabilities)
	writeInstrumentationCoverage(&output, "Instrumentation coverage (requested metric entities)", report.Instrumentation.Total)
	for _, metric := range report.Instrumentation.Metrics {
		writeInstrumentationCoverage(&output, "  "+metric.Metric, metric.Coverage)
	}
	output.WriteString("Summary:\n")
	writeSummary(&output, "  ", report.Summary)

	if len(report.Packages) == 0 {
		output.WriteString("\nNo packages.\n")
		return output.String()
	}
	for _, packageReport := range report.Packages {
		fmt.Fprintf(&output, "\nPackage: %s status=%s evidence=%t\n", packageReport.Path, packageReport.Status, packageReport.Evidence)
		writeSummary(&output, "  ", packageReport.Summary)
		for _, file := range packageReport.Files {
			fmt.Fprintf(&output, "  File: %s\n", file.Path)
			writeSummary(&output, "    ", file.Summary)
			for _, function := range file.Functions {
				fmt.Fprintf(&output, "    Function: %s", function.Name)
				if function.Location != nil {
					fmt.Fprintf(&output, " at %s", formatLocation(*function.Location))
				}
				output.WriteByte('\n')
				writeSummary(&output, "      ", function.Summary)
				for _, statement := range function.Statements {
					fmt.Fprintf(
						&output,
						"      Statement %s: statements=%d count=%d covered=%t ",
						formatLocation(statement.Location),
						statement.Statements,
						statement.Count,
						statement.Covered,
					)
					writeMetricInline(&output, statement.Metric)
					output.WriteByte('\n')
				}
				for _, decision := range function.Decisions {
					writeDecision(&output, decision)
				}
				for _, clause := range function.Clauses {
					fmt.Fprintf(
						&output,
						"      Clause %s (%s %s index=%d) at %s\n",
						clause.ClauseID,
						clause.Kind,
						clause.Role,
						clause.Index,
						formatLocation(clause.Location),
					)
					fmt.Fprintf(&output, "        Body coverage: executions=%d ", clause.BodyExecutions)
					writeMetricInline(&output, clause.BodyCoverage)
					output.WriteByte('\n')
					fmt.Fprint(&output, "        Selection coverage: ")
					writeMetricInline(&output, clause.SelectionCoverage)
					fmt.Fprintf(
						&output,
						" direct-selections=%d selected-alternatives=%v\n",
						clause.DirectSelections,
						clause.SelectedAlternatives,
					)
				}
				for _, noMatch := range function.NoMatches {
					fmt.Fprintf(
						&output,
						"      No-match selection %s (%s) at %s: ",
						noMatch.SwitchID,
						noMatch.Kind,
						formatLocation(noMatch.Location),
					)
					writeMetricInline(&output, noMatch.SelectionCoverage)
					output.WriteByte('\n')
				}
			}
		}
	}
	return output.String()
}

func writeCapabilities(output *strings.Builder, indent string, capabilities backend.CapabilitySet) {
	capabilityNames := make([]string, 0, len(capabilities))
	for capability := range capabilities {
		capabilityNames = append(capabilityNames, string(capability))
	}
	sort.Strings(capabilityNames)
	for _, name := range capabilityNames {
		fmt.Fprintf(output, "%s%s: %s\n", indent, name, capabilities[backendCapability(name)])
	}
}

func backendCapability(name string) backend.Capability { return backend.Capability(name) }

func writeInstrumentationCoverage(output *strings.Builder, label string, coverage backend.InstrumentationCoverage) {
	fmt.Fprintf(
		output,
		"%s: discovered=%d supported=%d instrumented=%d unsupported=%d unknown=%d (%s)\n",
		label,
		coverage.Discovered,
		coverage.Supported,
		coverage.Instrumented,
		coverage.Unsupported,
		coverage.Unknown,
		strconv.FormatFloat(coverage.Percentage, 'f', 2, 64)+"%",
	)
}

func writeDecision(output *strings.Builder, decision DecisionReport) {
	fmt.Fprintf(
		output,
		"      Decision %s (%s) at %s\n",
		decision.DecisionID,
		decision.Kind,
		formatLocation(decision.Location),
	)
	fmt.Fprintf(output, "        Expression: %s\n", decision.Expression)
	fmt.Fprintf(
		output,
		"        Decision Coverage: true=%t false=%t not-evaluated=%d ",
		decision.DecisionCoverage.True,
		decision.DecisionCoverage.False,
		decision.NotEvaluated,
	)
	writeMetricInline(output, decision.DecisionCoverage.Metric)
	output.WriteByte('\n')
	for _, condition := range decision.Conditions {
		fmt.Fprintf(
			output,
			"        Condition [%d] %s at %s: true=%t false=%t not-evaluated=%d ",
			condition.Index,
			condition.Expression,
			formatLocation(condition.Location),
			condition.True,
			condition.False,
			condition.NotEvaluated,
		)
		writeMetricInline(output, condition.Metric)
		output.WriteByte('\n')
		writeMCDCCondition(output, "          Unique", condition.MCDCUnique)
		writeMCDCCondition(output, "          Masking", condition.MCDCMasking)
	}
	writeMCDCAnalysis(output, "        Unique-Cause MC/DC", decision.MCDCUnique)
	writeMCDCAnalysis(output, "        Masking MC/DC", decision.MCDCMasking)
	for _, evaluation := range decision.Evaluations {
		fmt.Fprintf(
			output,
			"        Evaluation %s status=%s run=%q package=%q process=%d test=%q result=%t vector=%s\n",
			evaluation.EvaluationID,
			evaluation.Status,
			evaluation.RunID,
			evaluation.PackagePath,
			evaluation.ProcessID,
			evaluation.TestID,
			evaluation.Result,
			formatVector(evaluation.Conditions),
		)
	}
}

func writeMCDCAnalysis(output *strings.Builder, label string, analysis MCDCAnalysisReport) {
	fmt.Fprintf(
		output,
		"%s: enabled=%t status=%s analyzed=%d aborted=%d invalid=%d ",
		label,
		analysis.Enabled,
		analysis.Status,
		analysis.EvaluationsAnalyzed,
		analysis.AbortedEvaluations,
		analysis.InvalidEvaluations,
	)
	writeMetricInline(output, analysis.Metric)
	if analysis.Reason != "" {
		fmt.Fprintf(output, " reason=%q", analysis.Reason)
	}
	output.WriteByte('\n')
	for _, condition := range analysis.Conditions {
		writeMCDCCondition(output, fmt.Sprintf("          [%d]", condition.Index), condition)
	}
}

func writeMCDCCondition(output *strings.Builder, label string, condition MCDCConditionReport) {
	fmt.Fprintf(output, "%s: %s", label, condition.Status)
	if condition.Reason != "" {
		fmt.Fprintf(output, " reason=%q", condition.Reason)
	}
	if condition.Witness != nil {
		fmt.Fprintf(
			output,
			" witness=(%s %s) vectors=(%s -> %t; %s -> %t) completions=(%s %s)",
			condition.Witness.First.EvaluationID,
			condition.Witness.Second.EvaluationID,
			formatVector(condition.Witness.First.Conditions),
			condition.Witness.First.Result,
			formatVector(condition.Witness.Second.Conditions),
			condition.Witness.Second.Result,
			formatVector(condition.Witness.FirstCompletion),
			formatVector(condition.Witness.SecondCompletion),
		)
		if len(condition.Witness.UnobservedConditions) > 0 {
			fmt.Fprintf(output, " unobserved=%v", condition.Witness.UnobservedConditions)
		}
		if len(condition.Witness.MaskedConditions) > 0 {
			fmt.Fprintf(output, " masked=%v", condition.Witness.MaskedConditions)
		}
	}
	output.WriteByte('\n')
}

func writeSummary(output *strings.Builder, indent string, summary Summary) {
	metrics := []struct {
		name   string
		metric MetricSummary
	}{
		{"Statement Coverage", summary.Statement},
		{"Function Coverage", summary.Function},
		{"Decision Coverage", summary.Decision},
		{"Switch Clause Body Coverage", summary.SwitchClauseBody},
		{"Type Switch Clause Body Coverage", summary.TypeSwitchClauseBody},
		{"Select Clause Body Coverage", summary.SelectClauseBody},
		{"Switch Clause Selection Coverage", summary.SwitchClauseSelection},
		{"Type Switch Clause Selection Coverage", summary.TypeSwitchClauseSelection},
		{"Condition Coverage", summary.Condition},
		{"Unique-Cause MC/DC", summary.MCDCUnique},
		{"Masking MC/DC", summary.MCDCMasking},
	}
	for _, item := range metrics {
		fmt.Fprintf(output, "%s%s: ", indent, item.name)
		writeMetricInline(output, item.metric)
		output.WriteByte('\n')
	}
}

func writeMetricInline(output *strings.Builder, metric MetricSummary) {
	coverage := "n/a"
	if metric.Percentage != nil {
		coverage = fmt.Sprintf(
			"%d / %d = %s",
			metric.Covered,
			metric.Total,
			formatPercentage(metric.Percentage),
		)
	}
	fmt.Fprintf(
		output,
		"enabled=%t %s unsupported=%d unknown=%d infeasible=%d analysis-incomplete=%d",
		metric.Enabled,
		coverage,
		metric.Unsupported,
		metric.Unknown,
		metric.Infeasible,
		metric.AnalysisIncomplete,
	)
}

func formatPercentage(percentage *float64) string {
	if percentage == nil {
		return "n/a"
	}
	return strconv.FormatFloat(*percentage, 'f', 2, 64) + "%"
}

func formatLocation(location cover.SourceLocation) string {
	return fmt.Sprintf(
		"%s:%d:%d-%d:%d",
		location.File,
		location.Start.Line,
		location.Start.Column,
		location.End.Line,
		location.End.Column,
	)
}

func formatVector(vector []string) string {
	return "[" + strings.Join(vector, ",") + "]"
}

func completeness(complete bool) string {
	if complete {
		return "complete"
	}
	return "partial"
}
