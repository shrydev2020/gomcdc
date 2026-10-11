# gomcdc Normative Specification

This document defines the semantics and conformance requirements of `gomcdc` 2.0. The Japanese edition is normative; this English edition is a reference translation with identical definition numbers. The specification version is `2.0`.

## 1. Scope

For one logical `gomcdc test` measurement request, the tool aggregates eleven metrics over one source model: Statement, Function, Decision, Switch Clause Body, Type Switch Clause Body, Select Clause Body, Switch Clause Selection, Type Switch Clause Selection, Condition, Unique-Cause MC/DC, and Masking MC/DC.

`gomcdc version` writes the build identity to standard output and exits with code 0. A module-version build uses `gomcdc vMAJOR.MINOR.PATCH` (or the complete Go module version); a local build uses `gomcdc devel`, optionally followed by the abbreviated VCS revision and `dirty`. Additional arguments are invalid CLI usage. The identity is read directly from Go build information.

The target environment is Go 1.26.0 or later, Go Modules, Linux, and macOS. The compiler-aware backend applies an exact-anchor patch to the selected Go compiler source and fails explicitly when those anchors are incompatible. Later releases use the same source-anchor compatibility check. Target sources are packages in the main module returned by `go list` for the supplied package patterns. The target set excludes the standard library, external modules, vendor, tool-generated sources, and sources with the standard Go generated-code comment. `_test.go` belongs to all selected metrics, including Statement and Function, only with `--include-tests`. Active internal and external test files share the normal build-tag, generated-source, and exclusion rules.

## 2. Basic domains

### D1. Source location

`Location = (file, startLine, startColumn, endLine, endColumn)`. `file` is a physical path relative to the module root. Lines and columns are one-based. Every public location is normalized to the pre-instrumentation source Location.

### D2. Identifiers

`DecisionID`, `ConditionID`, and `ClauseID` are deterministic for one source revision. Their generation function may depend only on module path, package import path, relative file path, node kind, start offset, end offset, and condition index.

`EvaluationID` is unique for each decision evaluation begun within one process. The cross-process evaluation identity is `(runID, packagePath, PID, evaluationID)`.

### D3. States

```text
ConditionState   = true | false | not-evaluated
EvaluationStatus = completed | aborted
CoverageStatus   = covered | not-covered | infeasible | analysis-incomplete
SupportStatus    = supported | unsupported-by-backend | unknown
```

`not-evaluated` observes that a condition did not execute; it is not a Boolean value. `aborted` belongs only to EvaluationStatus.

### D4. Coverage count

Let `O_m` be the obligation set of metric `m`, and let `C_m ⊆ O_m` be its covered obligations.

```text
covered(m) = |C_m|
total(m)   = |O_m|
ratio(m)   = |C_m| / |O_m|   if |O_m| > 0
ratio(m)   = undefined       if |O_m| = 0
```

Entities classified as `unsupported-by-backend`, `unknown`, `infeasible`, or `analysis-incomplete` are excluded from `O_m` and counted separately. Undefined is rendered as `n/a` in text and `null` in JSON.

## 3. Source model

### D5. Function

A Function is a function declaration, method declaration, or function literal. Multiple `init` functions and function literals are distinguished by Location.

### D6. Decision

A Decision is the whole condition of an `if`, the condition of a conditional `for`, or each case expression of a conditionless switch. Multiple expressions in one case are independent Decisions.

### D7. Boolean expression tree

A Decision expression is transformed into this tree:

```text
Expr ::= Atom(conditionIndex) | Not(Expr) | And(Expr, Expr) | Or(Expr, Expr)
```

An Atom is a Boolean source-expression occurrence containing no `&&`, `||`, or `!`. A Boolean comparison, call, selection, or map lookup is one Atom. Indices `0..n-1` follow left-to-right syntactic source order. The Atom of `!a` is `a`; the `Not` node represents negation.

### D8. Clause

A Clause is a case or default clause of an expression switch, type switch, conditionless switch, or select. `case A, B` is one Clause.

`ClauseRole = case | default`. A switch dispatch has a `SwitchID`. An expression switch or type switch without default has one `NoMatch(SwitchID)` selection obligation distinct from every source Clause.

## 4. Observation semantics

### D9. Decision evaluation

One evaluation of a Decision with `n` conditions is:

```go
type DecisionEvaluation struct {
    DecisionID DecisionID
    EvaluationID EvaluationID
    TestID string
    Conditions [n]ConditionState
    Result bool
    Status EvaluationStatus
}
```

Every condition starts as `not-evaluated`. Only an Atom that actually evaluates is updated to true or false. An evaluation is completed only upon reaching `EndDecision`; it is aborted when panic, `runtime.Goexit`, process interruption, or another termination prevents that. TestID carries the Go test or subtest name for attributed evaluations, including goroutines spawned from that test and cleanup callbacks. Test entry-point instrumentation records attribution independently of `--include-tests`; that option controls test-code membership in the metric denominator. Identical vectors from different tests or processes remain separate report evidence; MC/DC analysis may still deduplicate identical vectors when finding witnesses. Initializers and other execution outside a test context retain `unknown`.

### D10. Evaluation function

For a complete assignment `x ∈ {false,true}^n`, `eval(E,x)` is the value of Decision tree `E`. `Comp(v)` is the set of complete assignments consistent with observed vector `v`, including the observed short-circuit path. Condition indices identify individual occurrences, including occurrences with identical source text. In v1, their values are independent variables in the Boolean model. Source-language coupling analysis is outside this model, and a witness must satisfy the rules using those independent variables.

### D11. Clause event

```text
body-execution   : control reached the start of a clause body
direct-selection : dispatch directly selected that clause
no-match         : dispatch without default selected no case
```

Reaching a later body through fallthrough records body-execution for that clause. Direct-selection records the clause chosen by dispatch.

## 5. Metrics

### D12. Statement Coverage

Each statement unit in the unchanged original-source inventory is an obligation. A unit is covered when its corresponding accepted Go cover counter is greater than zero. When a producer basic block is split into multiple regions, each original obligation is counted once across repeated NumStmt values. Units with different counters are displayed as separate statement regions. The detail unit is a statement region carrying original source range, statement count, and counter.

### D13. Function Coverage

Each Function containing at least one statement unit is an obligation. It is covered when at least one of its statement units is covered.

### D14. Decision Coverage

Each Decision `d` has obligations `(d,true)` and `(d,false)`. An outcome is covered when it occurs as Result in a completed evaluation.

### D15. Condition Coverage

Each condition occurrence `c` has obligations `(c,true)` and `(c,false)`. Only a value actually evaluated in a completed evaluation is covered. `not-evaluated` remains an observation state; the coverage obligations and evidence are the Boolean outcomes defined above.

### D16. Clause Body Coverage

Each case/default Clause body is one obligation. Expression and conditionless switches belong to Switch Clause Body; type switches to Type Switch Clause Body; selects to Select Clause Body. A corresponding body-execution event covers the obligation.

### D17. Clause Selection Coverage

Each case/default Clause and `NoMatch(SwitchID)` of an expression switch is a Switch Clause Selection obligation; a type switch analogously contributes Type Switch Clause Selection obligations. A direct-selection event carries SwitchID and ClauseID; a no-match event carries SwitchID and the no-match role. A case-alternative index is retained as evidence for its clause-level v1 obligation. A fallthrough event carries source and destination ClauseIDs as body-execution provenance.

### D18. Unique-Cause MC/DC

Condition `i` is covered when a pair `(p,q)` of completed evaluations satisfies all of:

1. `p[i]` and `q[i]` are Boolean values.
2. `p[i] ≠ q[i]`.
3. `p.result ≠ q.result`.
4. For every `j ≠ i`, `p[j] = q[j]`.

not-evaluated is unequal to every Boolean value. A satisfying pair is retained as a witness. A structurally impossible obligation is infeasible.

### D19. Masking MC/DC

For complete assignment `z`, let `flip(z,j)` reverse condition `j`, and define `masked(E,z,j) := eval(E,z) = eval(E,flip(z,j))`.

Condition `i` is covered when completed evaluations `(p,q)` and completions `(x,y) ∈ Comp(p) × Comp(q)` exist such that:

1. `p[i]` and `q[i]` are Boolean values.
2. `x[i] ≠ y[i]`.
3. `eval(E,x) ≠ eval(E,y)`.
4. For every `j ≠ i`, either `x[j] = y[j]`, or `masked(E,x,j) ∧ masked(E,y,j)`.

The observed pair, completions, and masked condition indices are retained as a witness.

For the validated read-once AND/OR/NOT expression model, Masking MC/DC performs
an exact joint search for both completions using shared search states. Each
condition obligation has default limits of 1,000,000 candidate
evaluation pairs, 4,000,000 newly expanded joint-search states, and 64 MiB for
the primary solver backing arrays (the flattened expression, memo table,
completion buffers, and candidate evaluation indexes). A search that would exceed a limit is
`analysis-incomplete`; reaching a witness
at the exact limit is `covered`. `infeasible` is used only when a separate
structure-only check proves that the target can never be pivotal.

An MC/DC percentage covers independent-effect obligations for condition occurrences. Complete MC/DC achievement also requires the corresponding Decision and Condition Coverage. Occurrences sharing source text remain independent variables in the analysis. A witness requiring unproved occurrence coupling, or an exact search stopped by a resource limit, is analysis-incomplete.

Each MC/DC analysis is a strategy-specific pure function.

```go
type MCDCStrategy interface {
    Analyze(DecisionMetadata, []DecisionEvaluation) MCDCResult
}
```

When Masking MC/DC is selected, report construction may analyze independent
Decisions with a bounded worker pool. The effective worker count is at most the
Decision count, `GOMAXPROCS`, and four. Each running Decision retains the D19
per-obligation limits in full. Each worker owns its solver state and places
results at the pre-sorted Decision index. Status, witness selection, aggregation,
and output order remain identical across worker counts. Cancellation stops
scheduling new Masking analyses, preserves already accepted evidence and other
metrics, and marks unscheduled Masking obligations `analysis-incomplete`.

## 6. Aggregation and evidence

### D20. Aggregation

The module, package, file, function, decision, condition, and clause levels sum integer obligations. A parent percentage is computed from its covered and total sums.

### D21. Evidence authority

Go standard cover over original sources supplies Statement and Function evidence. The selected Go cover's coordinates and code-range splitting are identified from its emitted instrumentation. Producer NumStmt is separate from original-source statement obligations; physical producer regions retain each statement's logical source identity. Each split region retains its own statement count and counter; that region's accepted counter determines its coverage. The AST backend supplies Decision, Condition, both MC/DC metrics, and the three Clause Body metrics. A compiler-aware backend supplies the two Clause Selection metrics.

The unchanged original-source Inventory is the sole denominator authority. Raw
producer output becomes coverage evidence only after integrity, provenance,
producer compatibility, correspondence mapping, and execution completeness are
validated. Accepted evidence is then projected to original Inventory
obligations. The denominator remains the original Inventory obligation set.

Instrumentation separately records byte-coordinate transformation provenance
between immutable source revisions. A revision exposes its stage identity,
content digest, size, and zero-based byte unit. Its total half-open range
relation distinguishes preserved, synthetic, deleted, ambiguous, and lossy
bytes, including one-to-many and many-to-one history. AST-printer output is
lossy unless the complete file is byte-identical. Composition requires the
same intermediate identity, digest, and size and exactly aligned relation
boundaries; a partial match produces an error. This model records transformation
history. Go-cover correspondence and the unchanged Inventory remain the
independent acceptance and obligation authorities.

Each requested producer reports four independent axes: `integrity` is `valid`,
`valid-prefix`, `invalid`, or `unavailable`; `completeness` is `complete`,
`partial`, or `unavailable`; `mapping` is `complete`, `invalid`, or
`unavailable`; and `usability` is `accepted`, `accepted-partial`, or `rejected`.
The execution layer decides usability. Report construction applies that
decision to each producer and preserves accepted evidence from other producers.

For each entity, a backend emits SupportStatus. Instrumentation Coverage carries `discovered`, `supported`, `instrumented`, `unsupported`, and `unknown` for every metric.

`--strict` fails when any requested metric has an unsupported-by-backend, unknown, analysis-incomplete, or supported-but-uninstrumented entity.

### D22. Measurement

MeasurementMode is `standard-cover` or `single-run`. Statement/Function alone
uses `standard-cover`. Any request containing a non-standard-cover metric uses
`single-run`, including a request for all eleven metrics. One measurement
session creates one disposable workspace and executes each selected package
test binary exactly once. Go cover, AST runtime, and compiler-aware producers
observe that same physical execution. The report retains one run status,
failure kind, and package-status map. The former v1 two-run behavior is retained
exclusively as a deterministic test oracle.

With `--include-tests`, the selected `go tool cover` also instruments test files
which `go test -cover` omits. Its emitted counter metadata must exactly match the
planned producer inventory, including AST rewrites, before instrumentation is
accepted. Test counter transport records set membership: whether each counter
was hit.
A separate file per source and process binds the run ID to a digest of package,
source path, and instrumented contents. Initialized zero counters are published
atomically; each first hit is committed synchronously before the source block
executes. The collector validates identity, shape, and counter values and
projects them through the same original-source correspondence as ordinary Go
cover. Missing evidence after interruption remains unknown. Collection uses
the evidence published during that single test execution, including executions
interrupted before their exit callbacks.

## 7. Execution model

### D23. Runtime

```go
BeginDecision(decisionID, conditionCount) EvaluationID
EvalCondition(evaluationID, conditionIndex, value) bool
EndDecision(evaluationID, result) bool
AbortDecision(evaluationID)
```

The runtime assigns each evaluation a unique EvaluationID and keeps its state under that identity. Identity separates recursion, nesting, loops, goroutines, and processes. Shared runtime state is race-free.

### D24. Semantic preservation

Instrumentation preserves Go evaluation order, short-circuiting, evaluation and call counts, panic conditions, defer order, side-effect order, return values, errors, and user-observable state. A probe records its received value once and returns the same value. Execution time, allocations, stack traces, inlining, scheduling, CPU, and memory may vary under instrumentation.

### D25. Processes and collection

Each test process writes to a distinct file whose name contains a collision-free encoding of package import path, PID, run ID, and nonce. Every record in one file has the same `(runID, packagePath, PID)` provenance. The CLI retains provenance on raw Decision and Clause events until it verifies the requested run, source-inventory ownership, process identity, condition count, states, result, status, and short-circuit consistency. Only verified events are projected to provenance-free, idempotent coverage observations. It constructs a partial report after test failure, build failure, timeout, panic, a truncated tail, or abnormal termination.


SIGINT and SIGTERM cancel the active request. Cancellation terminates every measurement-owned subprocess group and ends measurement scheduling. It then permits only bounded evidence recovery, report construction, and workspace cleanup. Caller cancellation uses the distinct result `failureKind=interrupted`.

### D26. go test

At least one package pattern is required. The CLI invokes `go test -count=1`. User or GOFLAGS settings for `-count`, `-cover`, `-coverprofile`, `-covermode`, `-coverpkg`, `-json`, `-overlay`, or `-toolexec` are CLI errors. Non-conflicting arguments after `--` apply with the same meaning to analysis and every run. Analysis and tests use identical GOOS, GOARCH, build tags, CGO, and module settings. An active go.work must contain exactly one main module.

## 8. External forms

### D27. CLI

The canonical `--coverage` values are the following eleven names and `all`; the default is `all`.

```text
statement, function, decision,
switch-clause-body, type-switch-clause-body, select-clause-body,
switch-clause-selection, type-switch-clause-selection,
condition, mcdc-unique, mcdc-masking, all
```

Each metric has exactly one `--fail-under-<metric>` threshold flag. A threshold for an unselected metric is a CLI error. A threshold over a zero total fails. Comparison uses the unrounded integer ratio.

The per-condition-obligation Masking MC/DC limits can be changed with
`--mcdc-masking-max-evaluation-pairs`, `--mcdc-masking-max-search-states`, and
`--mcdc-masking-max-solver-bytes`. Each supplied value is a positive decimal
integer and requires `mcdc-masking` to be selected; a mismatch is a CLI error. Every
unspecified field independently uses its D19 default. Raising a limit can
multiply total work across conditions and decisions.

### D28. Exit result

```text
0 success
1 the go test run failed
2 measurement, instrumentation, integrity, or report failure
3 coverage threshold failure
4 invalid CLI usage
130 interrupted by SIGINT
143 interrupted by SIGTERM
```

Precedence for ordinary completion is `4 > 2 > 1 > 3 > 0`. A handled termination signal returns its signal-derived exit code after recovery and cleanup. The overall result retains separate test, measurement, integrity, strict, and threshold fields.

`run.results` contains `test`, `measurement`, `integrity`, `strict`, and `threshold`. Each value is one of `passed`, `failed`, `timeout`, `not-run`, or `not-requested`; only test uses `timeout`. Strict and threshold use `not-requested` for omitted policies. Each result field preserves its own outcome independently of exit-code precedence.

### D29. JSON

The root contains `schemaVersion`, `toolVersion`, `module`, `run`, `measurementMode`, `measurements`, `producerOutcomes`, `capabilities`, `backendCapabilities`, `instrumentationCoverage`, `summary`, `packages`, and `errors`. When Masking MC/DC is requested, the optional `maskingAnalysisLimits` field records the three effective D19 limits. For ordinary measurements, `schemaVersion` is the report compatibility contract and is `2.0`; tool interruption has its own `failureKind` value, `interrupted`. `toolVersion` is the build identity from `gomcdc version`. `producerOutcomes` carries the four D21 evidence axes for every requested producer. `capabilities` is the tool-wide aggregate, while `backendCapabilities` exposes per-producer instrumentation authority.

[`schema/report-v2.0.schema.json`](../schema/report-v2.0.schema.json) is the machine-readable JSON Schema for ordinary measurement fields, required and optional keys, types, enums, and nullability. D34 defines the separate offline Report 2.1 contract.

The summary keys are `statement`, `function`, `decision`, `switchClauseBody`, `typeSwitchClauseBody`, `selectClauseBody`, `switchClauseSelection`, `typeSwitchClauseSelection`, `condition`, `mcdcUnique`, and `mcdcMasking`.

Each summary carries covered, total, percentage, and support/status counts. IDs are hexadecimal strings. Arrays are ordered by package path, Location, and ID. Decision detail carries expression, Location, outcomes, conditions, both MC/DC results, evaluation vectors, and witnesses. An error carries phase, code, message, and only when applicable package and module-relative path.

### D30. Text

Every enabled metric displays `covered / total = percentage` or `n/a`, with Instrumentation Coverage and excluded-status counts. MC/DC displays each condition result, witness vectors, Decision results, Masking completions, and the unsatisfied rule.

### D31. HTML

`--format html --output <directory>` creates `<directory>/index.html`. HTML is rendered entirely from the same Report model.

The module summary leads to package-centered navigation, followed by files and functions within each package. The function table presents all eleven metrics as independent columns. Function detail presents original-source Locations, Decisions, Conditions, Clause Body, Clause Selection, and both MC/DC results. Partial runs, unsupported, unknown, infeasible, and analysis-incomplete remain distinct from ordinary non-coverage.

The report presents every producer's integrity, completeness, mapping, and usability axes separately from the overall run status.

The output is one self-contained static HTML file containing all display assets and using only declarative HTML and CSS. Viewing it works entirely offline and triggers no network requests. Source expressions and paths are escaped for their HTML context. Numerator, denominator, percentage, and status text accompany the colors.

### D32. Execution and storage

One measurement run instruments sources and the compiler, builds, and executes tests to collect Go-cover, AST, and compiler-aware evidence. The event journal aggregates records while preserving evaluation provenance and witnesses.

Temporary directories and event files use owner-only permissions. Source copies remain within the workspace. Report source paths are module-relative.

### D33. Saved results and reports

`gomcdc test --save FILE` writes a [saved result 2.0](../schema/saved-result-v2.0.schema.json). Its payload contains the `measurement` archive and derived `report`. The archive holds original sources and measurement settings. Failed and interrupted tests retain recovered evidence and outcomes.

Saved files contain `format`, `version`, `sha256`, and a payload. The checksum is SHA-256 over compact JSON with sorted object keys and exact integer precision. Readers validate required fields, versions, checksums, source hashes, and source ownership. Publication renames a temporary file with permission 0600.

`gomcdc report [--format=text|json|html] [--output=PATH] FILE` renders a saved result. The default is text on stdout; HTML writes index.html in the specified directory. Original test and policy outcomes appear in the report; successful rendering returns 0. Input and output must be distinct files, including through symbolic or hard links.

### D34. MC/DC reanalysis

`gomcdc reanalyze --output FILE [analysis limits] INPUT` recomputes coverage from the saved measurement archive. The three D19 limits may be overridden; omitted limits retain their recorded values.

Multi-run analysis and reanalysis use [Report 2.1](../schema/report-v2.1.schema.json). `contributingRuns` retains each execution's outcomes, policy decisions, producer outcomes, and diagnostics. Multiple executions use `measurementMode=merged` and list each measurement in `measurements`. Aggregate test, measurement, and integrity outcomes retain original failures; `complete` is true when every execution is complete. The analysis operation's strict and threshold outcomes are `not-requested`.

### D35. Measurement archives and CLI operations

A [measurement archive 1.0](../schema/measurement-v1.0.schema.json) contains a common Plan and individual Runs.

- Plan: original sources, build conditions, module/tool/measurement definition, Decision/Condition/Clause metadata, Boolean structure, and byte offsets.
- Run: invocation/runtime IDs, original settings, test outcomes, producer acceptance, generated probe IDs, runtime observations, counters mapped to original sources, and diagnostics.

| Operation | Input and output |
| --- | --- |
| `record --output FILE PACKAGE... [-- test arguments]` | Execute tests and save a measurement archive |
| `merge --output FILE [--expect-runs N] MEASUREMENT...` | Combine executions with a common Plan |
| `analyze --output FILE [--expect-runs N] [analysis limits] MEASUREMENT...` | Combine evidence, determine coverage, and save the result |
| `require [--complete] [--METRIC PERCENT] ANALYSIS` | Evaluate explicit requirements against a saved result |

Each CI job records the same sources, package set, metrics, and Go/build conditions into a separate file. Test selection and execution settings may differ between jobs. Merge deduplicates by invocation ID and sorts executions by ID. Conflicting contents for an ID, different Plans, and overlapping physical runtime IDs are errors. A positive `--expect-runs N` requires exactly N independent executions.

Analyze uses the Plan's Boolean structure and accepted evaluations to find MC/DC witnesses across executions. Each C0 Run holds inventories for all Plan sources; merging uses original-source statement units. Counter overflow is an error. Original failures and evidence provenance remain attached to each execution.

Require's `--complete` demands complete execution, evidence, instrumentation, and analysis. A metric minimum is satisfied when at least one measured, feasible obligation exists, unknown/unsupported/analysis-incomplete counts are zero, and the unrounded percentage meets the requested minimum. Minimums are finite values from 0 to 100.

Record returns 1 for test failure and 2 for measurement failure. Merge, analyze, reanalyze, and report return 0 on successful processing; require returns 0 for satisfied requirements and 3 otherwise. Invalid arguments return 4; read, analysis, or output errors return 2; interruption returns 130. Atomic publication preserves existing output on failure before publication. Record and test need a checkout and Go; subsequent operations use saved files.

## 9. Conformance conditions

An implementation conforms only when all conditions hold.

1. It implements the types, sets, functions, and external forms of D1–D32.
2. It integrates all eleven metrics over one source model and report.
3. It records not-evaluated as its own condition state, distinct from false.
4. It keeps aborted as an evaluation status and uses completed evaluations as coverage evidence.
5. It measures Clause Body and Clause Selection separately, preserving direct selection and fallthrough body execution as distinct events.
6. It names and aggregates Decision Coverage using the Decision outcomes defined in D14.
7. Its Decision set consists of the if, conditional for, and conditionless-switch case expressions defined in D6.
8. It preserves unsupported and unknown as distinct evidence states.
9. It exposes exactly the canonical metrics and corresponding threshold flags in D27 and the summary keys in D29.
10. It expresses every formal Location in the pre-instrumentation source coordinates defined in D1.
11. IDs and report order remain identical across package load order, map iteration, goroutine order, and analysis parallelism.
12. Each selected package test binary executes exactly once per session, including when cached test results exist.
13. Each test process writes to its own evidence file.
14. A compiler-aware backend reports support according to the selected Go compiler's compatibility check.
15. Completion requires integrated C0, both MC/DC strategies, multiple packages, source mapping, concurrent evaluation, and Clause Selection.
16. Production uses the single-execution model in D22; the v1 dual-run implementation is retained exclusively as test-oracle code.
17. Covered status requires validated correspondence; unknown, partial, and ambiguous correspondence retain their evidence states.
18. Accepted evidence from each producer is preserved independently of other producers' outcomes.

## 10. Acceptance verification

The acceptance suite includes AND, OR, NOT, nested expressions, a fixed-seed semantic oracle with four or more conditions, side effects, evaluation order, conditionless switch, expression switch, type switch, select, empty bodies, fallthrough, direct selection, no-match, recursion, nested decisions, loops, multiple goroutines, panic, recover, defer, `runtime.Goexit`, multiple packages, external test packages, build tags, test/build failure, timeout, caller interruption, truncated events, partial recovery, provenance mutation, source mapping, user `//line`, generated-code exclusion, JSON Schema contract mutation, and all eleven thresholds. A deterministic all-metric fixture must match the semantic projection of the v1 C0-plus-AST dual-run oracle while executing each package test binary once. For `a && b`, Unique-Cause yields `a=infeasible` and `b=covered`, while Masking covers both conditions.

Completion requires successful `go test -count=1 ./...`, `go test -race -count=1 ./...`, and `go vet ./...`, and integer equality between fixture-module aggregation and the sum of its package aggregations.

Repository CI runs the acceptance suite on Go 1.26.x and 1.27.x. It measures gomcdc itself on the latest stable Go/Linux toolchain and checks the module and critical-package floors in `.github/self-mcdc-baseline.json`. This baseline is a maintenance contract that preserves the verified test suite's achieved coverage. Coverage conformance follows the semantic requirements above. A change that lowers a floor must identify and justify the obligations being lost during review.

## 11. References

The semantic references are FAA CAST-10, the NASA MC/DC tutorial, Hayhurst et al., Chilenski, the Go language specification, and Go `cmd/cover`. D1–D32 take precedence over the references.
