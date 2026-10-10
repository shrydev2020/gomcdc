package resultfile

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/shrydev2020/gomcdc/v2/internal/config"
	"github.com/shrydev2020/gomcdc/v2/internal/goflags"
	"github.com/shrydev2020/gomcdc/v2/internal/gotestargs"
	"github.com/shrydev2020/gomcdc/v2/internal/mcdc"
	"github.com/shrydev2020/gomcdc/v2/internal/report"
)

// Snapshot is an original measurement, never the output of an offline
// analysis. Its content identity makes nested merges idempotent. Source bytes
// are held once in Result.Sources, verified against every input's manifest.
type Snapshot struct {
	ID       string        `json:"id"`
	Settings Settings      `json:"settings"`
	Report   report.Report `json:"report"`
}

func snapshotID(settings Settings, r report.Report) (string, error) {
	data, err := json.Marshal(struct {
		Settings Settings      `json:"settings"`
		Report   report.Report `json:"report"`
	}{settings, r})
	if err != nil {
		return "", err
	}
	canonical, err := canonicalJSON(data)
	if err != nil {
		return "", err
	}
	return Digest(canonical), nil
}

func validateInputs(result Result) error {
	if len(result.Inputs) == 0 {
		if result.Report.SchemaVersion != report.SchemaVersion || len(result.Report.ContributingRuns) > 0 {
			return errors.New("derived report requires original measurement inputs")
		}
		return nil
	}
	if result.Report.SchemaVersion != report.OfflineSchemaVersion || len(result.Report.ContributingRuns) != len(result.Inputs) {
		return errors.New("analysis provenance does not match its original inputs")
	}
	previous := ""
	for i, input := range result.Inputs {
		id, err := snapshotID(input.Settings, input.Report)
		if err != nil {
			return err
		}
		if id != input.ID || id <= previous || result.Report.ContributingRuns[i].ID != id {
			return errors.New("invalid, duplicate, or unordered analysis input identity")
		}
		previous = id
		if err := validate(Result{Settings: input.Settings, Report: input.Report, Sources: result.Sources}); err != nil {
			return fmt.Errorf("analysis input %s: %w", id, err)
		}
	}
	return nil
}

// Analyze flattens previous derived archives back to their original snapshots,
// checks compatibility, then unions observations and reanalyzes MC/DC. The
// report is a projection; only original input snapshots authorize evidence.
func Analyze(ctx context.Context, results []Result, budget mcdc.AnalysisBudget) (Result, error) {
	if len(results) == 0 {
		return Result{}, errors.New("no saved results")
	}
	byID := make(map[string]Snapshot)
	sourcesByKey := make(map[string]Source)
	for _, result := range results {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		if err := validate(result); err != nil {
			return Result{}, err
		}
		for _, source := range result.Sources {
			sourcesByKey[source.PackagePath+"\x00"+source.Path] = source
		}
		inputs := result.Inputs
		if len(inputs) == 0 {
			id, err := snapshotID(result.Settings, result.Report)
			if err != nil {
				return Result{}, err
			}
			inputs = []Snapshot{{ID: id, Settings: result.Settings, Report: result.Report}}
		}
		for _, input := range inputs {
			byID[input.ID] = input
		}
	}
	inputs := make([]Snapshot, 0, len(byID))
	for _, input := range byID {
		inputs = append(inputs, input)
	}
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].ID < inputs[j].ID })
	first := inputs[0]
	common, err := measurementSettings(first.Settings)
	if err != nil {
		return Result{}, err
	}
	snapshots := make([]report.AnalysisSnapshot, len(inputs))
	runOwners := make(map[string]string)
	invocationOwners := make(map[string]string)
	for i, input := range inputs {
		settings, err := measurementSettings(input.Settings)
		if err != nil {
			return Result{}, err
		}
		if !reflect.DeepEqual(common, settings) || input.Report.Module != first.Report.Module || input.Report.ToolVersion != first.Report.ToolVersion || input.Report.MeasurementMode != first.Report.MeasurementMode {
			return Result{}, errors.New("incompatible saved results: sources, packages, coverage selection, Go/build settings, measurement mode, and tool version must match")
		}
		if id := input.Settings.InvocationID; id != "" {
			if owner, exists := invocationOwners[id]; exists && owner != input.ID {
				return Result{}, errors.New("conflicting snapshots of the same saved invocation")
			}
			invocationOwners[id] = input.ID
		}
		// Partly overlapping exports of one physical run are ambiguous for C0
		// counters and absence observations. Never silently count them twice.
		for _, pkg := range input.Report.Packages {
			for _, file := range pkg.Files {
				for _, fn := range file.Functions {
					for _, decision := range fn.Decisions {
						for _, evaluation := range decision.Evaluations {
							if evaluation.RunID == "" {
								return Result{}, errors.New("saved evaluation is missing its run identity")
							}
							if owner, exists := runOwners[evaluation.RunID]; exists && owner != input.ID {
								return Result{}, errors.New("different snapshots overlap the same physical run; use one original snapshot")
							}
							runOwners[evaluation.RunID] = input.ID
						}
					}
				}
			}
		}
		snapshots[i] = report.AnalysisSnapshot{ID: input.ID, Report: input.Report}
	}
	metrics, err := config.ParseCoverage(strings.Join(common.Coverage, ","))
	if err != nil {
		return Result{}, err
	}
	built, err := report.AnalyzeSnapshots(ctx, snapshots, metrics, budget)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	sources := make([]Source, 0, len(sourcesByKey))
	for _, source := range sourcesByKey {
		sources = append(sources, source)
	}
	slices.SortFunc(sources, func(a, b Source) int {
		if order := cmp.Compare(a.PackagePath, b.PackagePath); order != 0 {
			return order
		}
		return cmp.Compare(a.Path, b.Path)
	})
	result := Result{Settings: common, Report: built, Sources: sources, Inputs: inputs}
	if err := validate(result); err != nil {
		return Result{}, err
	}
	return result, nil
}

// Test-selection and runtime scheduling flags can differ between shards. All
// other flags (including unknown flags and binary arguments) must match; they
// cannot accidentally weaken build/source compatibility checks.
func measurementSettings(settings Settings) (Settings, error) {
	settings.InvocationID = ""
	runtimeFlags := []string{"run", "skip", "count", "shuffle", "parallel", "cpu", "timeout", "failfast", "short", "bench", "benchtime", "benchmem", "v", "json", "list", "fullpath"}
	arguments, err := gotestargs.Parse(settings.GoTestArgs)
	if err != nil {
		return Settings{}, err
	}
	arguments = arguments.Without(runtimeFlags...)
	settings.GoTestArgs = arguments.Prefix()
	if binary, boundary := arguments.BinaryArgs(); boundary {
		settings.GoTestArgs = append(settings.GoTestArgs, "-args")
		settings.GoTestArgs = append(settings.GoTestArgs, binary...)
	}
	words, err := goflags.Split(settings.GOFLAGS)
	if err != nil {
		return Settings{}, err
	}
	flags, err := gotestargs.Parse(words)
	if err != nil {
		return Settings{}, err
	}
	settings.GOFLAGS = goflags.Join(flags.Without(runtimeFlags...).Prefix())
	settings.PackagePatterns = nil
	settings.Timeout = ""
	settings.Strict = false
	settings.FailUnder = nil
	settings.Packages = slices.Sorted(slices.Values(settings.Packages))
	settings.Coverage = slices.Sorted(slices.Values(settings.Coverage))
	settings.Excludes = slices.Sorted(slices.Values(settings.Excludes))
	return settings, nil
}
