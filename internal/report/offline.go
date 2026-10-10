package report

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"sort"
	"strconv"

	"github.com/shrydev2020/gomcdc/v2/internal/analyzer"
	"github.com/shrydev2020/gomcdc/v2/internal/config"
	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
	"github.com/shrydev2020/gomcdc/v2/internal/mcdc"
)

// AnalysisSnapshot is an original, unmodified measurement report. ID is the
// saved snapshot identity, not a physical test-run ID.
type AnalysisSnapshot struct {
	ID     string
	Report Report
}

// AnalyzeSnapshots unions accepted observations against an identical static
// inventory and reruns both MC/DC strategies. Callers enforce source/build
// compatibility and supply unique snapshots in deterministic identity order.
// Original policy results remain in ContributingRuns; this operation makes no
// new strict/threshold policy decision.
func AnalyzeSnapshots(ctx context.Context, snapshots []AnalysisSnapshot, metrics config.CoverageSet, budget mcdc.AnalysisBudget) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	if len(snapshots) == 0 {
		return Report{}, errors.New("no measurement snapshots")
	}
	first := snapshots[0].Report
	inventory, err := staticInventory(first)
	if err != nil {
		return Report{}, err
	}
	for _, snapshot := range snapshots[1:] {
		other, err := staticInventory(snapshot.Report)
		if err != nil {
			return Report{}, err
		}
		if !bytes.Equal(inventory, other) || !reflect.DeepEqual(first.Capabilities, snapshot.Report.Capabilities) || !reflect.DeepEqual(first.Backends, snapshot.Report.Backends) || !reflect.DeepEqual(first.Instrumentation, snapshot.Report.Instrumentation) {
			return Report{}, errors.New("measurement inventories or instrumentation capabilities differ")
		}
	}
	// Own every mutable hierarchy slice; input snapshots remain authoritative.
	encoded, err := json.Marshal(first)
	if err != nil {
		return Report{}, err
	}
	var result Report
	if err := json.Unmarshal(encoded, &result); err != nil {
		return Report{}, err
	}
	result.SchemaVersion = OfflineSchemaVersion
	if len(snapshots) > 1 {
		result.MeasurementMode = MeasurementMerged
	}
	result.ContributingRuns = make([]ContributingRun, 0, len(snapshots))
	result.Measurements = []MeasurementRun{}
	// Outcomes belong to individual producers observing individual executions;
	// there is deliberately no invented aggregate producer acceptance decision.
	result.ProducerOutcomes = []ProducerOutcome{}
	result.Errors = []ReportError{}
	result.Run = first.Run
	result.Run.Results.Strict, result.Run.Results.Threshold = ResultNotRequested, ResultNotRequested
	for _, snapshot := range snapshots {
		r := snapshot.Report
		result.ContributingRuns = append(result.ContributingRuns, ContributingRun{ID: snapshot.ID, Run: r.Run, MeasurementMode: r.MeasurementMode, Measurements: cloneMeasurementRuns(r.Measurements), ProducerOutcomes: cloneProducerOutcomes(r.ProducerOutcomes), Errors: append([]ReportError{}, r.Errors...)})
		result.Measurements = append(result.Measurements, cloneMeasurementRuns(r.Measurements)...)
		result.Run.Complete = result.Run.Complete && r.Run.Complete
		result.Run.Status = combinedRunStatus(result.Run.Status, r.Run.Status)
		if result.Run.FailureKind == cover.RunFailureNone {
			result.Run.FailureKind = r.Run.FailureKind
		} else if r.Run.FailureKind != cover.RunFailureNone && result.Run.FailureKind != r.Run.FailureKind {
			result.Run.FailureKind = cover.RunFailureMixed
		}
		result.Run.Results.Test = combinedResult(result.Run.Results.Test, r.Run.Results.Test)
		result.Run.Results.Measurement = combinedResult(result.Run.Results.Measurement, r.Run.Results.Measurement)
		result.Run.Results.Integrity = combinedResult(result.Run.Results.Integrity, r.Run.Results.Integrity)
		for _, failure := range r.Errors {
			if failure.Code != "strict-coverage-gap" && failure.Code != "coverage-threshold-failed" && !slices.Contains(result.Errors, failure) {
				result.Errors = append(result.Errors, failure)
			}
		}
	}
	if metrics.Enabled(config.MetricMCDCMasking) {
		effective := mcdc.EffectiveMaskingAnalysisBudget(budget)
		result.MaskingAnalysisLimits = &MaskingAnalysisLimits{effective.MaxEvaluationPairs, effective.MaxSearchStates, effective.MaxSolverBytes}
	}
	for pi := range result.Packages {
		pkg := &result.Packages[pi]
		for _, snapshot := range snapshots[1:] {
			other := snapshot.Report.Packages[pi]
			pkg.Status = combinedPackageStatus(pkg.Status, other.Status)
			pkg.Evidence = pkg.Evidence || other.Evidence
		}
		for fi := range pkg.Files {
			for ni := range pkg.Files[fi].Functions {
				if err := ctx.Err(); err != nil {
					return Report{}, err
				}
				function := &pkg.Files[fi].Functions[ni]
				inputs := make([]FunctionReport, len(snapshots))
				for i, snapshot := range snapshots {
					inputs[i] = snapshot.Report.Packages[pi].Files[fi].Functions[ni]
				}
				if err := mergeFunction(ctx, function, inputs, snapshots, metrics, budget); err != nil {
					return Report{}, fmt.Errorf("%s/%s: %w", pkg.Path, pkg.Files[fi].Path, err)
				}
			}
		}
	}
	rebuildSummaries(&result, metrics)
	return result, nil
}

func mergeFunction(ctx context.Context, output *FunctionReport, inputs []FunctionReport, snapshots []AnalysisSnapshot, metrics config.CoverageSet, budget mcdc.AnalysisBudget) error {
	for _, input := range inputs[1:] {
		output.Summary.Function = unionObligation(output.Summary.Function, input.Summary.Function)
		for i := range output.Statements {
			a, b := &output.Statements[i], input.Statements[i]
			if math.MaxUint64-a.Count < b.Count {
				return errors.New("statement counter overflow")
			}
			a.Count += b.Count
			a.Covered = a.Covered || b.Covered
			a.Metric = unionObligation(a.Metric, b.Metric)
		}
		for i := range output.Clauses {
			a, b := &output.Clauses[i], input.Clauses[i]
			if a.BodyExecutions < 0 || b.BodyExecutions < 0 || math.MaxInt-a.BodyExecutions < b.BodyExecutions || a.DirectSelections < 0 || b.DirectSelections < 0 || math.MaxInt-a.DirectSelections < b.DirectSelections {
				return errors.New("clause counter overflow")
			}
			a.BodyExecutions += b.BodyExecutions
			a.DirectSelections += b.DirectSelections
			a.BodyCoverage = unionObligation(a.BodyCoverage, b.BodyCoverage)
			a.SelectionCoverage = unionObligation(a.SelectionCoverage, b.SelectionCoverage)
			a.SelectedAlternatives = append(a.SelectedAlternatives, b.SelectedAlternatives...)
			slices.Sort(a.SelectedAlternatives)
			a.SelectedAlternatives = slices.Compact(a.SelectedAlternatives)
		}
		for i := range output.NoMatches {
			output.NoMatches[i].SelectionCoverage = unionObligation(output.NoMatches[i].SelectionCoverage, input.NoMatches[i].SelectionCoverage)
		}
	}
	tasks := make([]decisionBuildTask, len(output.Decisions))
	for di, decision := range output.Decisions {
		metadata, err := restoreMetadata(decision)
		if err != nil {
			return err
		}
		task := decisionBuildTask{metadata: metadata, state: entityUnknown}
		seen := make(map[cover.EvaluationIdentity]cover.DecisionEvaluation)
		for i, input := range inputs {
			d := input.Decisions[di]
			state := archivedDecisionState(d, snapshots[i].Report.ProducerOutcomes)
			if state != entityNormal {
				if task.state != entityNormal && state == entityUnsupported {
					task.state = state
				}
				continue
			}
			task.state = entityNormal
			if d.NotEvaluated < 0 || math.MaxInt-task.notEvaluated < d.NotEvaluated {
				return errors.New("decision counter overflow")
			}
			task.notEvaluated += d.NotEvaluated
			for _, saved := range d.Evaluations {
				evaluation, err := restoreEvaluation(metadata, saved)
				if err != nil {
					return fmt.Errorf("decision %s: %w", d.DecisionID, err)
				}
				if previous, exists := seen[evaluation.Identity()]; exists {
					if !reflect.DeepEqual(previous, evaluation) {
						return errors.New("conflicting evaluation identity")
					}
					continue
				}
				seen[evaluation.Identity()] = evaluation
				task.evaluations = append(task.evaluations, evaluation)
			}
		}
		sort.Slice(task.evaluations, func(i, j int) bool { return lessEvaluation(task.evaluations[i], task.evaluations[j]) })
		tasks[di] = task
	}
	output.Decisions = buildDecisionReports(ctx, tasks, metrics, budget, effectiveMaskingDecisionWorkers(0, len(tasks)))
	return nil
}

// Each input here represents the same indivisible obligation (a statement
// block, function, or clause). A known observation supersedes an unavailable
// one; a raw hit count never supersedes the producer's acceptance decision.
func unionObligation(a, b MetricSummary) MetricSummary {
	if !a.Enabled {
		return a
	}
	if b.Covered > a.Covered || (a.Total == 0 && b.Total > 0) || (a.Unknown > 0 && b.Unsupported > 0) {
		return b
	}
	return a
}

func archivedDecisionState(d DecisionReport, outcomes []ProducerOutcome) entityState {
	if producerRejectsEvidence(outcomes, ProducerASTRuntime) {
		return entityUnknown
	}
	for _, metric := range []MetricSummary{d.DecisionCoverage.Metric, d.Summary.Condition, d.MCDCUnique.Metric, d.MCDCMasking.Metric} {
		if metric.Enabled && metric.Unknown > 0 {
			return entityUnknown
		}
		if metric.Enabled && metric.Unsupported > 0 {
			return entityUnsupported
		}
	}
	return entityNormal
}

func restoreMetadata(d DecisionReport) (cover.DecisionMetadata, error) {
	id, err := strconv.ParseUint(d.DecisionID, 0, 64)
	if err != nil {
		return cover.DecisionMetadata{}, err
	}
	metadata := cover.DecisionMetadata{ID: cover.DecisionID(id), Kind: d.Kind, Location: d.Location, Expression: d.Expression}
	atoms := make([]string, len(d.Conditions))
	for i, c := range d.Conditions {
		if int(c.Index) != i {
			return metadata, errors.New("saved condition indexes are not contiguous")
		}
		cid, err := strconv.ParseUint(c.ConditionID, 0, 64)
		if err != nil {
			return metadata, err
		}
		metadata.Conditions = append(metadata.Conditions, cover.ConditionMetadata{ID: cover.ConditionID(cid), Index: c.Index, Expression: c.Expression, Location: c.Location})
		atoms[i] = c.Expression
	}
	metadata.ExpressionTree, err = analyzer.RestoreExpression(d.Expression, atoms)
	return metadata, err
}

func restoreEvaluation(metadata cover.DecisionMetadata, saved EvaluationReport) (cover.DecisionEvaluation, error) {
	id, err := strconv.ParseUint(saved.EvaluationID, 0, 64)
	if err != nil {
		return cover.DecisionEvaluation{}, err
	}
	if saved.DecisionID != formatID(uint64(metadata.ID)) {
		return cover.DecisionEvaluation{}, errors.New("evaluation has a different decision ID")
	}
	e := cover.DecisionEvaluation{DecisionID: metadata.ID, EvaluationID: cover.EvaluationID(id), RunID: saved.RunID, PackagePath: saved.PackagePath, ProcessID: saved.ProcessID, TestID: saved.TestID, Result: saved.Result}
	switch saved.Status {
	case "completed":
		e.Status = cover.EvaluationCompleted
	case "aborted":
		e.Status = cover.EvaluationAborted
	default:
		return e, errors.New("unknown evaluation status")
	}
	for _, state := range saved.Conditions {
		switch state {
		case "true":
			e.Conditions = append(e.Conditions, cover.ConditionTrue)
		case "false":
			e.Conditions = append(e.Conditions, cover.ConditionFalse)
		case "not-evaluated":
			e.Conditions = append(e.Conditions, cover.ConditionNotEvaluated)
		default:
			return e, errors.New("unknown condition state")
		}
	}
	return e, mcdc.ValidateCompletedEvaluation(metadata, e)
}

func rebuildSummaries(r *Report, metrics config.CoverageSet) {
	r.Summary = newSummary(metrics)
	for pi := range r.Packages {
		p := &r.Packages[pi]
		p.Summary = newSummary(metrics)
		for fi := range p.Files {
			f := &p.Files[fi]
			f.Summary = newSummary(metrics)
			for ni := range f.Functions {
				n := &f.Functions[ni]
				function := n.Summary.Function
				n.Summary = newSummary(metrics)
				n.Summary.Function = function
				for _, s := range n.Statements {
					addMetric(&n.Summary.Statement, s.Metric)
				}
				for _, d := range n.Decisions {
					addSummary(&n.Summary, d.Summary)
				}
				for _, c := range n.Clauses {
					addClauseMetrics(&n.Summary, c.Kind, c.BodyCoverage, c.SelectionCoverage)
				}
				for _, m := range n.NoMatches {
					addNoMatchMetric(&n.Summary, m.Kind, m.SelectionCoverage)
				}
				addSummary(&f.Summary, n.Summary)
			}
			addSummary(&p.Summary, f.Summary)
		}
		addSummary(&r.Summary, p.Summary)
	}
}

// staticInventory strips only observations and derived fields. Comparing the
// remaining hierarchy detects missing entities as well as changed IDs/layouts.
func staticInventory(r Report) ([]byte, error) {
	data, err := json.Marshal(r.Packages)
	if err != nil {
		return nil, err
	}
	var packages []PackageReport
	if err := json.Unmarshal(data, &packages); err != nil {
		return nil, err
	}
	for pi := range packages {
		p := &packages[pi]
		p.Status = ""
		p.Evidence = false
		p.Summary = Summary{}
		for fi := range p.Files {
			f := &p.Files[fi]
			f.Summary = Summary{}
			for ni := range f.Functions {
				n := &f.Functions[ni]
				n.Summary = Summary{}
				for i := range n.Statements {
					s := &n.Statements[i]
					s.Count = 0
					s.Covered = false
					s.Metric = MetricSummary{}
				}
				for i := range n.Decisions {
					d := &n.Decisions[i]
					d.NotEvaluated = 0
					d.Summary = Summary{}
					d.DecisionCoverage = DecisionCoverage{}
					d.MCDCUnique = MCDCAnalysisReport{}
					d.MCDCMasking = MCDCAnalysisReport{}
					d.Evaluations = nil
					for ci := range d.Conditions {
						c := &d.Conditions[ci]
						c.True = false
						c.False = false
						c.NotEvaluated = 0
						c.Metric = MetricSummary{}
						c.MCDCUnique = MCDCConditionReport{}
						c.MCDCMasking = MCDCConditionReport{}
					}
				}
				for i := range n.Clauses {
					c := &n.Clauses[i]
					c.BodyExecutions = 0
					c.DirectSelections = 0
					c.SelectedAlternatives = nil
					c.BodyCoverage = MetricSummary{}
					c.SelectionCoverage = MetricSummary{}
				}
				for i := range n.NoMatches {
					n.NoMatches[i].SelectionCoverage = MetricSummary{}
				}
			}
		}
	}
	return json.Marshal(packages)
}

func combinedResult(a, b ResultStatus) ResultStatus {
	rank := map[ResultStatus]int{ResultNotRequested: 0, ResultPassed: 1, ResultNotRun: 2, ResultFailed: 3, ResultTimeout: 4}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func combinedRunStatus(a, b cover.RunStatus) cover.RunStatus {
	if a == cover.RunTimeout || b == cover.RunTimeout {
		return cover.RunTimeout
	}
	if a == cover.RunFailed || b == cover.RunFailed {
		return cover.RunFailed
	}
	return cover.RunPassed
}

func combinedPackageStatus(a, b string) string {
	rank := map[string]int{"skipped": 0, "passed": 1, "started": 2, "failed": 3, "build-failed": 4, "timeout": 5}
	if rank[b] > rank[a] {
		return b
	}
	return a
}
