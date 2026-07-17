package report_test

import (
	"fmt"
	"testing"

	"github.com/shrydev2020/gomcdc/internal/config"
	cover "github.com/shrydev2020/gomcdc/internal/coverage"
	"github.com/shrydev2020/gomcdc/internal/report"
)

var benchmarkReportSink report.Report

func BenchmarkReportPolicyFinalization(b *testing.B) {
	input := reportBuildBenchmarkInput()
	results := report.RunResults{
		Test:        report.ResultPassed,
		Measurement: report.ResultPassed,
		Integrity:   report.ResultPassed,
		Strict:      report.ResultFailed,
		Threshold:   report.ResultPassed,
	}
	errors := []report.ReportError{{
		Phase: "validation", Code: "strict-coverage-gap", Message: "requested coverage contains a gap",
	}}

	b.Run("rebuild-coverage-hierarchy", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			benchmarkReportSink = report.Build(input)
			updated := input
			updated.Results = results
			updated.Errors = errors
			benchmarkReportSink = report.Build(updated)
		}
	})
	b.Run("update-run-results-and-errors", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			built := report.Build(input)
			benchmarkReportSink = report.WithRunResultsAndErrors(built, results, errors)
		}
	})
}

func BenchmarkReportBuildMetricSelection(b *testing.B) {
	input := reportBuildBenchmarkInput()
	cases := []struct {
		name     string
		coverage config.CoverageSet
	}{
		{name: "all-metrics", coverage: config.AllCoverage()},
		{name: "decision-condition-unique", coverage: config.CoverageSet{
			config.MetricDecision:   true,
			config.MetricCondition:  true,
			config.MetricMCDCUnique: true,
		}},
		{name: "decision-condition-masking", coverage: config.CoverageSet{
			config.MetricDecision:    true,
			config.MetricCondition:   true,
			config.MetricMCDCMasking: true,
		}},
		{name: "decision-condition-only", coverage: config.CoverageSet{
			config.MetricDecision:  true,
			config.MetricCondition: true,
		}},
	}
	for _, test := range cases {
		b.Run(test.name, func(b *testing.B) {
			input.Coverage = test.coverage
			b.ReportAllocs()
			for range b.N {
				benchmarkReportSink = report.Build(input)
			}
		})
	}
}

func reportBuildBenchmarkInput() report.Input {
	const (
		decisionCount  = 24
		conditionCount = 8
	)
	packagePath := "example.com/benchmark/logic"
	expression := balancedBenchmarkExpression(0, conditionCount)
	decisions := make([]cover.DecisionMetadata, 0, decisionCount)
	evaluations := make([]cover.DecisionEvaluation, 0, decisionCount*(1<<conditionCount))
	var evaluationID cover.EvaluationID
	for decisionIndex := 0; decisionIndex < decisionCount; decisionIndex++ {
		decisionID := cover.DecisionID(decisionIndex + 1)
		conditions := make([]cover.ConditionMetadata, conditionCount)
		for conditionIndex := range conditions {
			conditions[conditionIndex] = cover.ConditionMetadata{
				ID:         cover.ConditionID(decisionIndex*conditionCount + conditionIndex + 1),
				Index:      uint16(conditionIndex),
				Expression: fmt.Sprintf("c%d", conditionIndex),
				Location: cover.SourceLocation{File: "logic.go",
					Start: cover.Position{Line: decisionIndex + 1, Column: conditionIndex + 1},
					End:   cover.Position{Line: decisionIndex + 1, Column: conditionIndex + 2}},
			}
		}
		decisions = append(decisions, cover.DecisionMetadata{
			ID: decisionID, ModulePath: "example.com/benchmark", Package: packagePath,
			Function: fmt.Sprintf("Decision%d", decisionIndex), Kind: cover.DecisionIf,
			Location: cover.SourceLocation{File: "logic.go",
				Start: cover.Position{Line: decisionIndex + 1, Column: 1},
				End:   cover.Position{Line: decisionIndex + 1, Column: conditionCount + 1}},
			Expression: "balanced boolean expression", Conditions: conditions, ExpressionTree: expression,
		})
		for vector := 0; vector < 1<<conditionCount; vector++ {
			evaluationID++
			states := make([]cover.ConditionState, conditionCount)
			result := evaluateBenchmarkExpression(expression, vector, states)
			evaluations = append(evaluations, cover.DecisionEvaluation{
				DecisionID: decisionID, EvaluationID: evaluationID, RunID: "benchmark-run",
				PackagePath: packagePath, ProcessID: 1, TestID: "benchmark",
				Conditions: states, Result: result, Status: cover.EvaluationCompleted,
			})
		}
	}
	return report.Input{
		ModulePath: "example.com/benchmark", Coverage: config.AllCoverage(),
		Decisions: decisions, Evaluations: evaluations, RunStatus: cover.RunPassed, Complete: true,
		PackageStatuses:    map[string]string{packagePath: "passed"},
		ASTPackageStatuses: map[string]string{packagePath: "passed"},
	}
}

func balancedBenchmarkExpression(start, end int) *cover.BooleanExpression {
	if end-start == 1 {
		return cover.NewConditionExpression(uint16(start))
	}
	middle := start + (end-start)/2
	left := balancedBenchmarkExpression(start, middle)
	right := balancedBenchmarkExpression(middle, end)
	if end-start == 4 {
		return cover.NewOrExpression(left, right)
	}
	return cover.NewAndExpression(left, right)
}

func evaluateBenchmarkExpression(expression *cover.BooleanExpression, vector int, states []cover.ConditionState) bool {
	switch expression.Kind {
	case cover.BooleanExpressionCondition:
		value := vector&(1<<expression.ConditionIndex) != 0
		if value {
			states[expression.ConditionIndex] = cover.ConditionTrue
		} else {
			states[expression.ConditionIndex] = cover.ConditionFalse
		}
		return value
	case cover.BooleanExpressionAnd:
		return evaluateBenchmarkExpression(expression.Left, vector, states) &&
			evaluateBenchmarkExpression(expression.Right, vector, states)
	case cover.BooleanExpressionOr:
		return evaluateBenchmarkExpression(expression.Left, vector, states) ||
			evaluateBenchmarkExpression(expression.Right, vector, states)
	case cover.BooleanExpressionNot:
		return !evaluateBenchmarkExpression(expression.Left, vector, states)
	case cover.BooleanExpressionConstant:
		return expression.Constant
	default:
		panic("unsupported benchmark expression")
	}
}
