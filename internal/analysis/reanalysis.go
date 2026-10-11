package analysis

import (
	"context"

	"github.com/shrydev2020/gomcdc/v2/internal/mcdc"
	"github.com/shrydev2020/gomcdc/v2/internal/measurement"
)

func Reanalyze(ctx context.Context, archive measurement.Archive, budget mcdc.AnalysisBudget) (Result, error) {
	built, err := FromMeasurement(ctx, archive, budget)
	if err != nil {
		return Result{}, err
	}
	// Keep original outcomes visible even for a single reanalyzed execution.
	if len(archive.Runs) == 1 {
		built.SchemaVersion = OfflineSchemaVersion
		built.ContributingRuns = []ContributingRun{{ID: archive.Runs[0].ID, Run: built.Run, MeasurementMode: built.MeasurementMode, Measurements: built.Measurements, ProducerOutcomes: built.ProducerOutcomes, Errors: built.Errors}}
		built.ProducerOutcomes = []ProducerOutcome{}
		built.Errors = nil
		for _, d := range built.ContributingRuns[0].Errors {
			if d.Code != "strict-coverage-gap" && d.Code != "coverage-threshold-failed" {
				built.Errors = append(built.Errors, d)
			}
		}
	}
	// Offline analysis makes no new policy decision.
	built.Run.Results.Strict, built.Run.Results.Threshold = ResultNotRequested, ResultNotRequested
	return built, nil
}
