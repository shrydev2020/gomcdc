# gomcdc

[![Ask DeepWiki](https://deepwiki.com/badge.svg)](https://deepwiki.com/shrydev2020/gomcdc)

[日本語](README.ja.md)

`gomcdc` runs Go tests and produces one coverage report containing statement,
function, decision, condition, switch-clause, Unique-Cause MC/DC, and Masking
MC/DC results.

Go's standard coverage answers whether statements and functions ran. `gomcdc`
also records boolean evaluation vectors, so it can show whether each condition
independently affected its decision. It distinguishes clause body execution
from direct switch/type-switch selection and preserves partial results when a
test run fails or is interrupted.

One measurement session executes each selected package test binary once. Go
cover, AST runtime instrumentation, and compiler-aware selection hooks observe
that same execution.

## Requirements

- Go 1.26.0 or later
- A target using Go Modules
- Linux or macOS

## Install

Install the v2 release with Go 1.26 or later:

```sh
go install github.com/shrydev2020/gomcdc/v2@latest
```

Verify the installed build with `gomcdc version`.

## Quick start

Run `gomcdc` from the module you want to measure:

```sh
cd /path/to/your/module
gomcdc test ./...
```

All eleven metrics are enabled by default and the text report is written to
standard output. Each enabled summary has a covered count, denominator,
percentage, and separate counts for each support and analysis state:

```text
Summary:
  Decision Coverage: enabled=true 22 / 30 = 73.33% unsupported=0 unknown=0 infeasible=0 analysis-incomplete=0
  Condition Coverage: enabled=true 34 / 46 = 73.91% unsupported=0 unknown=0 infeasible=0 analysis-incomplete=0
  Unique-Cause MC/DC: enabled=true 8 / 15 = 53.33% unsupported=0 unknown=0 infeasible=8 analysis-incomplete=0
  Masking MC/DC: enabled=true 14 / 23 = 60.87% unsupported=0 unknown=0 infeasible=0 analysis-incomplete=0
```

Below the module summary, the text report expands packages, files, functions,
decisions, conditions, MC/DC witnesses, and observed evaluation vectors.
Evaluation records and MC/DC witnesses include the executing test or subtest
name. Initializers and other execution outside a test context use `unknown`.

## Metrics

| CLI name | What it measures |
| --- | --- |
| `statement` | Executed Go statements |
| `function` | Functions whose bodies executed |
| `decision` | True and false outcomes of `if`, boolean `for`, and conditionless-switch case decisions |
| `switch-clause-body` | Executed expression-switch clause bodies |
| `type-switch-clause-body` | Executed type-switch clause bodies |
| `select-clause-body` | Executed `select` clause bodies |
| `switch-clause-selection` | Directly selected expression-switch clauses and matched alternatives |
| `type-switch-clause-selection` | Directly selected type-switch clauses and matched type alternatives |
| `condition` | True and false outcomes of atomic boolean conditions |
| `mcdc-unique` | Conditions with a pair where only the target condition changes the decision result |
| `mcdc-masking` | Conditions with a pair where the target is independently decisive after masked values are accounted for |

Clause body and clause selection are intentionally separate. A body may execute
because control falls through from an earlier clause, while selection identifies
the clause chosen by dispatch.

## Reports

Text is the default. JSON follows the checked-in report schema. HTML writes a
self-contained report to the requested directory.

Schema 2.0 exposes an outcome for every requested evidence producer. Integrity,
execution completeness, source mapping, and the final usability decision remain
separate. Valid AST and Go-cover evidence remains available even when the
compiler-selection stream is rejected.

```sh
# Text on stdout
gomcdc test ./...

# JSON file
gomcdc test --format json --output coverage.json ./...

# HTML at coverage-html/index.html
gomcdc test --format html --output coverage-html ./...
```

Use `--save` to create several reports from one measurement. `report` reads
its results and original sources entirely from the saved file and renders
them offline.

```sh
gomcdc test --save coverage.gomcdc.json --format json --output coverage.json ./...
gomcdc report --format html --output coverage-html coverage.gomcdc.json
gomcdc report coverage.gomcdc.json
```

The saved file includes original sources, source hashes, the Go version and
build environment, selected metrics, exclusions, and test arguments. Sharing
it also shares those sources and arguments. Supply a file created by `--save`
to `report`. `--format json` produces a rendered report for other consumers.
Original failures, partial results, and threshold outcomes are preserved;
`report` itself returns 0 when rendering succeeds.

Use `merge` for test shards measured against the same sources, packages,
metrics, and Go/build settings. It unions observations and recomputes MC/DC,
including witness pairs whose evaluations came from different executions.

```sh
gomcdc test --save shard-a.gomcdc.json ./... -- -run '^TestA'
gomcdc test --save shard-b.gomcdc.json ./... -- -run '^TestB'
gomcdc merge --output merged.gomcdc.json shard-a.gomcdc.json shard-b.gomcdc.json
gomcdc reanalyze --output analyzed.gomcdc.json --mcdc-masking-max-search-states 8000000 merged.gomcdc.json
gomcdc report --format html --output coverage-html analyzed.gomcdc.json
```

Both commands process the saved files offline and write a new saved-result
file. `reanalyze` recomputes the selected Unique-Cause and Masking metrics from
recorded observations. Both accept the same three `--mcdc-masking-max-*` limits
as `test`. Unspecified limits use defaults for `merge` and the input's recorded
limits for `reanalyze`.

Repeated inputs and nested merges count each original snapshot once.
Different package sets, sources, or build settings produce a compatibility
error. Original failures, interruptions, and strict/threshold outcomes remain
available for each input. The derived report uses `not-requested` for its
strict/threshold fields. The offline command's exit code reports the save
outcome, with 0 for success.

Interpret support and analysis states separately from coverage misses:

| State | Meaning |
| --- | --- |
| `not-covered` | Valid evidence exists, but no required coverage witness was found |
| `infeasible` | The obligation cannot be satisfied under the selected MC/DC strategy and Go's evaluation rules |
| `unsupported-by-backend` | No active measurement backend can establish the obligation |
| `unknown` | Evidence authority or measurement completeness is insufficient to decide coverage |
| `analysis-incomplete` | Exact analysis did not complete for the obligation, including when Masking MC/DC reaches its bounded search budget |

`unknown`, `unsupported-by-backend`, `infeasible`, and `analysis-incomplete`
each retain their own status and count. Failed tests, panics, timeouts, and
truncated runtime evidence produce a partial report when trustworthy evidence
survives.

Masking MC/DC uses exact search. Its built-in limit for each condition obligation
is 1,000,000 candidate evaluation pairs, 4,000,000 search states, and 64 MiB of
primary solver backing arrays. The solver-byte limit excludes validated input,
result witnesses, goroutine stacks, and all other process memory; it is not a
process-wide heap, RSS, or total-memory limit. A search that would exceed a
limit yields `analysis-incomplete`.
The three `--mcdc-masking-max-*` options override these positive per-obligation
limits. Raising them can multiply total work across conditions and decisions.
Reports record the effective values as `maskingAnalysisLimits`.

## Common options

```sh
# Measure selected metrics
gomcdc test --coverage=decision,condition,mcdc-unique,mcdc-masking ./...

# Exclude module-relative paths; --exclude is repeatable and supports **
gomcdc test --exclude='internal/generated/**' ./...

# Include active _test.go files in every selected metric
gomcdc test --include-tests ./...

# Forward arguments to go test after --
gomcdc test ./... -- -run TestCritical
```

| Option | Purpose |
| --- | --- |
| `--coverage=<list>` | Select comma-separated metrics; default is `all` |
| `--exclude=<glob>` | Exclude a module-relative source glob; repeatable |
| `--include-tests` | Include active `_test.go` files, including statements and functions |
| `--format=text\|json\|html` | Select report format |
| `--output=<path>` | Write a file, or a directory for HTML |
| `--save=<path>` | Save original sources, measurement settings, and results for `gomcdc report` |
| `--strict` | Fail on requested unsupported, unknown, analysis-incomplete, or uninstrumented entities |
| `--fail-under-<metric>=<percent>` | Fail when one enabled metric is below a threshold |
| `--mcdc-masking-max-evaluation-pairs=<count>` | Override candidate evaluation pairs per Masking condition obligation; default 1,000,000 |
| `--mcdc-masking-max-search-states=<count>` | Override newly expanded search states per Masking condition obligation; default 4,000,000 |
| `--mcdc-masking-max-solver-bytes=<bytes>` | Override primary solver backing-array bytes per Masking condition obligation; default 67,108,864 |
| `--timeout=<duration>` | Set the `go test` subprocess timeout; default is 10 minutes |
| `--keep-workdir` | Retain the instrumented temporary workspace for diagnosis |
| `--workdir=<directory>` | Choose the parent directory for the temporary workspace |

Run `gomcdc test --help` for the complete option list.

## CI policy

Use `--strict` to require complete measurement and `--fail-under-*` to enforce
coverage policy. A threshold is valid only for a metric selected by
`--coverage`.

```sh
gomcdc test \
  --coverage=decision,condition,mcdc-unique,mcdc-masking \
  --strict \
  --fail-under-decision=80 \
  --fail-under-condition=75 \
  --fail-under-mcdc-unique=60 \
  --fail-under-mcdc-masking=65 \
  ./...
```

| Exit code | Meaning |
| ---: | --- |
| 0 | Success |
| 1 | The `go test` run failed |
| 2 | Measurement, instrumentation, integrity, or report failure |
| 3 | Coverage threshold failure |
| 4 | Invalid CLI usage |

## Supported scope and limitations

- Package patterns must resolve to packages in the main module. The standard
  library, external modules, `vendor`, and files marked with Go's generated-code
  comment are excluded.
- An active `go.work` is supported when it contains exactly one main module.
- `_test.go` files enter the selected metrics only with `--include-tests`.
  This includes Statement and Function Coverage for test helpers and tests,
  using the same Go cover counter placement as ordinary source files.
- Windows, assembly internals, cgo internals, compiler-IR obligations, path
  coverage, and distributed test execution are outside v2.
- The target module is built and tested with the current user's authority. The
  temporary workspace is not a sandbox for malicious target code.

## Reference

- [Normative specification](docs/specification.ja.md)
- [English reference specification](docs/specification.md)
- [Measurement JSON schema](schema/report-v2.0.schema.json)
- [Offline analysis JSON schema](schema/report-v2.1.schema.json)
- [Saved-result schema](schema/saved-result-v1.1.schema.json)

## Development

See [Releasing gomcdc](docs/releasing.md) for the release workflow.

```sh
go test -count=1 ./...
go test -count=1 -race ./...
go vet ./...
```

## License

MIT. See [LICENSE](LICENSE).
