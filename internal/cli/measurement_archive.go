package cli

import (
	"fmt"
	"github.com/shrydev2020/gomcdc/v2/internal/analysis"
	"github.com/shrydev2020/gomcdc/v2/internal/measurement"
)

func archiveMeasurement(settings measurement.Settings, input analysis.Input, outcome measurementOutcome) (measurement.Archive, error) {
	common, err := measurement.BuildSettings(settings)
	if err != nil {
		return measurement.Archive{}, fmt.Errorf("capture build settings for execution %q: %w", settings.InvocationID, err)
	}
	plan := measurement.Plan{Definition: measurement.Definition, ToolVersion: input.ToolVersion, Module: input.ModulePath, Settings: common,
		Decisions: input.Decisions, Clauses: input.Clauses, NoMatches: input.NoMatches, UnknownSources: input.InstrumentationUnknown}
	for _, s := range input.SourceFiles {
		plan.Sources = append(plan.Sources, measurement.Source{PackagePath: s.PackagePath, Path: s.Path, Content: s.Source, SHA256: measurement.Digest(s.Source)})
	}
	run := measurement.Run{ID: settings.InvocationID, RuntimeID: outcome.runtimeID, Settings: settings, Status: input.RunStatus, FailureKind: input.FailureKind, Complete: input.Complete,
		Results: input.Results, Mode: string(input.MeasurementMode), Executions: input.Measurements, Producers: input.ProducerOutcomes,
		Packages: input.PackageStatuses, ASTPackages: input.ASTPackageStatuses, Probes: outcome.probes, Runtime: outcome.recorded, Cover: outcome.coverEvidence}
	for _, d := range input.Errors {
		run.Diagnostics = append(run.Diagnostics, measurement.Diagnostic{Phase: d.Phase, Code: d.Code, Message: d.Message, Package: d.Package, Path: d.Path})
	}
	return measurement.Archive{Plan: plan, Runs: []measurement.Run{run}}, nil
}
