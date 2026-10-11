package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/shrydev2020/gomcdc/v2/internal/c0"
	"github.com/shrydev2020/gomcdc/v2/internal/config"
	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
	"github.com/shrydev2020/gomcdc/v2/internal/evidence"
	"github.com/shrydev2020/gomcdc/v2/internal/mcdc"
	"github.com/shrydev2020/gomcdc/v2/internal/measurement"
)

// FromMeasurement analyzes original typed evidence. Even after merging, the
// Boolean tree comes from the plan, never from a formatted report expression.
func FromMeasurement(ctx context.Context, archive measurement.Archive, budget mcdc.AnalysisBudget) (Result, error) {
	return FromMeasurementMetrics(ctx, archive, budget, nil)
}

// FromMeasurementMetrics analyzes only the selected measured metrics. A nil
// selection means all recorded metrics. It validates the entire archive and
// never rewrites the measurement Plan to change the analysis selection.
func FromMeasurementMetrics(ctx context.Context, archive measurement.Archive, budget mcdc.AnalysisBudget, selected config.CoverageSet) (Result, error) {
	if err := measurement.Validate(ctx, archive); err != nil {
		return Result{}, fmt.Errorf("validate measurement for analysis: %w", err)
	}
	metrics, err := config.ParseCoverage(strings.Join(archive.Plan.Settings.Coverage, ","))
	if err != nil {
		return Result{}, fmt.Errorf("parse analysis metrics: %w", err)
	}
	if selected != nil {
		if len(selected.Names()) == 0 {
			return Result{}, fmt.Errorf("no analysis metrics selected")
		}
		for metric, enabled := range selected {
			if enabled && !metrics.Enabled(metric) {
				return Result{}, fmt.Errorf("%s was not measured", metric)
			}
		}
		metrics = selected
	}
	snapshots := make([]runProjection, 0, len(archive.Runs))
	for _, run := range archive.Runs {
		input, err := measurementInput(ctx, archive.Plan, run, metrics, budget, len(archive.Runs) > 1)
		if err != nil {
			return Result{}, fmt.Errorf("analyze execution %q: %w", run.ID, err)
		}
		if len(archive.Runs) == 1 {
			return Analyze(input), nil
		}
		input.deferMCDC = true
		indexed := newBuildContext(input)
		buildHierarchy(indexed, input)
		finalizeReport(indexed, input)
		snapshots = append(snapshots, runProjection{ID: run.ID, Report: indexed.report, input: &input, context: indexed})
	}
	result, err := analyzeRuns(ctx, snapshots, metrics, budget)
	if err != nil {
		return Result{}, fmt.Errorf("combine execution coverage: %w", err)
	}
	// Probe generation is an entity union, not a sum of per-run percentages.
	union := *snapshots[0].input
	probes := measurement.Probes{}
	for _, snapshot := range snapshots {
		p := snapshot.input.Probes
		probes.Decisions = append(probes.Decisions, p.Decisions...)
		probes.ClauseBodies = append(probes.ClauseBodies, p.ClauseBodies...)
		probes.ClauseSelections = append(probes.ClauseSelections, p.ClauseSelections...)
		probes.NoMatches = append(probes.NoMatches, p.NoMatches...)
	}
	union.Probes = probes
	// Own the C0 projection before combining presence of original counters.
	if union.C0 != nil {
		data, err := json.Marshal(union.C0)
		if err != nil {
			return Result{}, fmt.Errorf("clone statement coverage: %w", err)
		}
		union.C0 = new(c0.Report)
		if err := json.Unmarshal(data, union.C0); err != nil {
			return Result{}, fmt.Errorf("decode cloned statement coverage: %w", err)
		}
		for pi := range union.C0.Packages {
			for fi := range union.C0.Packages[pi].Files {
				for ni := range union.C0.Packages[pi].Files[fi].Functions {
					f := &union.C0.Packages[pi].Files[fi].Functions[ni]
					for _, snapshot := range snapshots[1:] {
						if snapshot.input.C0 == nil {
							continue
						}
						other := snapshot.input.C0.Packages[pi].Files[fi].Functions[ni]
						for bi := range f.Blocks {
							f.Blocks[bi].Evidence = f.Blocks[bi].Evidence || other.Blocks[bi].Evidence
						}
					}
					f.CompleteEvidence = true
					for _, block := range f.Blocks {
						if !block.Evidence {
							f.CompleteEvidence = false
						}
					}
				}
			}
		}
	}
	result.Instrumentation = buildInstrumentationReport(union, result.Capabilities)
	return result, nil
}

func measurementInput(ctx context.Context, plan measurement.Plan, run measurement.Run, metrics config.CoverageSet, budget mcdc.AnalysisBudget, separateStatements bool) (Input, error) {
	accepted, _ := evidence.AcceptRuntimeByProducer(ctx, plan.Decisions, plan.Clauses, run.Runtime, run.RuntimeID, plan.NoMatches)
	input := Input{Context: ctx, ToolVersion: plan.ToolVersion, ModulePath: plan.Module, Coverage: metrics, MaskingAnalysisBudget: budget,
		Decisions: plan.Decisions, Clauses: plan.Clauses, NoMatches: plan.NoMatches,
		Evaluations: accepted.Evaluations, NotEvaluatedDecisions: accepted.NotEvaluatedDecisions, ClauseObservations: accepted.ClauseObservations,
		RunStatus: run.Status, FailureKind: run.FailureKind, Results: run.Results, Complete: run.Complete,
		MeasurementMode: MeasurementMode(run.Mode), Measurements: run.Executions, ProducerOutcomes: run.Producers,
		PackageStatuses: run.Packages, ASTPackageStatuses: run.ASTPackages, InstrumentationUnknown: plan.UnknownSources, Probes: run.Probes}
	for _, s := range plan.Sources {
		input.SourceFiles = append(input.SourceFiles, SourceFileInput{PackagePath: s.PackagePath, Path: s.Path, Source: s.Content})
	}
	for _, d := range run.Diagnostics {
		input.Errors = append(input.Errors, ReportError{Phase: d.Phase, Code: d.Code, Message: d.Message, Package: d.Package, Path: d.Path})
	}
	if run.Cover != nil && (metrics.Enabled(config.MetricStatement) || metrics.Enabled(config.MetricFunction)) {
		projected, err := c0.ProjectAcceptedEvidence(ctx, plan.Module, *run.Cover, c0.Options{SeparateStatements: separateStatements})
		if err != nil {
			return Input{}, fmt.Errorf("project execution %q counters: %w", run.ID, err)
		}
		input.C0 = &projected
	}
	return input, nil
}

// sourceDecision retrieves decision metadata from the original source plan.
func sourceDecision(snapshot runProjection, d DecisionReport) (cover.DecisionMetadata, error) {
	id, err := strconv.ParseUint(d.DecisionID, 0, 64)
	if err != nil {
		return cover.DecisionMetadata{}, fmt.Errorf("parse decision ID %q: %w", d.DecisionID, err)
	}
	if metadata, ok := snapshot.context.decisionByID[cover.DecisionID(id)]; ok {
		return metadata, nil
	}
	return cover.DecisionMetadata{}, fmt.Errorf("decision %s absent from source plan", d.DecisionID)
}

func sourceDecisionState(snapshot runProjection, metadata cover.DecisionMetadata) entityState {
	input := *snapshot.input
	if producerRejectsEvidence(input.ProducerOutcomes, ProducerASTRuntime) {
		return entityUnknown
	}
	observed := snapshot.context.astPackageEvidence[metadata.Package]
	if packageUnknown(astPackageStatus(input, metadata.Package, input.PackageStatuses[metadata.Package]), observed) {
		return entityUnknown
	}
	if !slices.Contains(input.Probes.Decisions, metadata.ID) {
		return entityUnknown
	}
	if !supportedDecision(metadata.Kind) {
		return entityUnsupported
	}
	return entityNormal
}
