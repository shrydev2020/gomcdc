# gomcdc

[日本語](README.ja.md) · [DeepWiki](https://deepwiki.com/shrydev2020/gomcdc)

Measure Statement, Function, Decision, Condition, switch-clause, Unique-Cause
MC/DC, and Masking MC/DC coverage from Go tests. MC/DC witness pairs include
evaluation values and the originating test or subtest names.

## Install

Requires Go 1.26 or later, Go Modules, and Linux or macOS.

```sh
go install github.com/shrydev2020/gomcdc/v2@latest
gomcdc version
```

## Measure and report

Run in the target module. All 11 metrics are enabled by default, with text output.
`./...` also selects repositories containing multiple main packages.

```sh
gomcdc test ./...
gomcdc test --format json --output coverage.json ./...
gomcdc test --format html --output coverage-html ./...
```

HTML is written to `coverage-html/index.html`. Save the result, including its
original sources and measurement evidence, to render several formats from one run:

```sh
gomcdc test --save coverage.gomcdc.json ./...
gomcdc report --format html --output coverage-html coverage.gomcdc.json
gomcdc report coverage.gomcdc.json
```

`report` renders saved results offline. JSON reports are for external consumers;
`--save` produces the artifact used for rendering and reanalysis.

## Parallel CI jobs

Use the same sources, package set, metrics, and Go/build settings in every job.
Each job selects its tests and uses a separate checkout and output path.

```sh
# Run in separate CI jobs
gomcdc record --output a.measurement.json ./... -- -run '^TestA'
gomcdc record --output b.measurement.json ./... -- -run '^TestB'

# Collect artifacts, then merge and analyze
gomcdc merge --expect-runs 2 --output combined.json a.measurement.json b.measurement.json
gomcdc analyze --output coverage.gomcdc.json combined.json
gomcdc report --format html --output coverage-html coverage.gomcdc.json
gomcdc require --complete --mcdc-masking 100 coverage.gomcdc.json
```

`record` saves execution evidence. `merge` combines executions and deduplicates
by execution ID. `--expect-runs` requires the given number of independent runs.
`analyze` finds MC/DC witness pairs across the combined evaluations, including
pairs from different executions. It also accepts multiple measurement files
directly. Collect failed jobs' artifacts to include their evidence.

Reanalyze a saved result with a different search limit. Omitted limits retain
their recorded values:

```sh
gomcdc reanalyze --output expanded.json --mcdc-masking-max-search-states 8000000 coverage.gomcdc.json
```

`require --complete` demands complete execution, evidence, instrumentation, and
analysis. `--METRIC PERCENT` sets a metric minimum. A minimum requires measured
obligations; unknown, unsupported, or analysis-incomplete obligations make it unsatisfied.

## Metrics

| CLI name | Measures |
| --- | --- |
| `statement` | Executed Go statements |
| `function` | Functions whose bodies executed |
| `decision` | True/false outcomes of `if`, Boolean `for`, and conditionless-switch cases |
| `condition` | True/false outcomes of atomic Boolean conditions |
| `switch-clause-body` | Executed expression-switch clause bodies |
| `type-switch-clause-body` | Executed type-switch clause bodies |
| `select-clause-body` | Executed `select` clause bodies |
| `switch-clause-selection` | Directly selected expression-switch clauses and alternatives |
| `type-switch-clause-selection` | Directly selected type-switch clauses and type alternatives |
| `mcdc-unique` | Pairs where changing only the target condition changes the decision |
| `mcdc-masking` | Pairs where the target independently changes the decision while other changes are masked |

Clause Body includes execution through fallthrough. Clause Selection counts direct dispatch.

## Read the result

Each metric shows covered obligations, the denominator, percentage, and analysis
states. Details are grouped by package, file, function, and decision. Evaluations
outside a test context display `unknown` as their test name.

| State | Meaning |
| --- | --- |
| `not-covered` | Valid evidence contains no required witness |
| `infeasible` | Unachievable under the selected MC/DC strategy and Go evaluation rules |
| `unsupported-by-backend` | Outside the measurement backend's capabilities |
| `unknown` | Insufficient evidence to decide coverage |
| `analysis-incomplete` | Analysis stopped, for example at a search limit |

Failed tests, panics, timeouts, and interruptions retain recovered evidence and
original failures. Default Masking MC/DC limits per condition are 1,000,000
evaluation pairs, 4,000,000 search states, and 64 MiB of solver arrays.
Reports record effective limits in `maskingAnalysisLimits`.

## Common options

| Option | Purpose |
| --- | --- |
| `--coverage=<list>` | Comma-separated metrics; default `all` |
| `--exclude=<glob>` | Exclude module-relative paths; repeatable, supports `**` |
| `--include-tests` | Include active `_test.go` files in all selected metrics |
| `--strict` | Fail on unknown, unsupported, unfinished, or uninstrumented obligations |
| `--fail-under-<metric>=<percent>` | Minimum coverage for a selected metric |
| `--mcdc-masking-max-evaluation-pairs=<count>` | Evaluation-pair limit per condition |
| `--mcdc-masking-max-search-states=<count>` | Search-state limit per condition |
| `--mcdc-masking-max-solver-bytes=<bytes>` | Solver-array limit per condition |
| `--timeout=<duration>` | Test timeout; default 10 minutes |
| `--keep-workdir` | Retain the instrumented workspace for diagnosis |
| `--workdir=<directory>` | Parent directory for the temporary workspace |

Arguments after `--` are passed to `go test`. See `gomcdc test --help` for all options.
Targets are packages in the main module. Generated sources and vendor are excluded.
An active `go.work` is supported when it contains one main module.

Exit codes: success 0, test failure 1, measurement/read/output failure 2, unmet
requirement 3, invalid arguments 4, interruption 130. Offline operations return
their own processing outcome; original test outcomes appear in the saved result.

## Development and specification

```sh
go test ./...
go test -race ./...
go vet ./...
```

- [Normative specification (Japanese)](docs/specification.ja.md) · [English reference](docs/specification.md)
- [Report schema](schema/report-v2.0.schema.json) · [Multi-run and reanalysis schema](schema/report-v2.1.schema.json)
- [Measurement archive schema](schema/measurement-v1.0.schema.json) · [Saved-result schema](schema/saved-result-v2.1.schema.json)
- [Releasing](docs/releasing.md) · [MIT License](LICENSE)
