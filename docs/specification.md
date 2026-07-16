# gomcdc Normative Specification

This document defines the semantics and conformance requirements of `gomcdc` 1.0. The Japanese edition is normative; this English edition is a reference translation with identical definition numbers. The specification version is `1.0`.

The `gomcdc` v1 series treats this specification as its compatibility contract. The v1 series does not remove or change the meaning of existing metrics, CLI options and exit codes, or required JSON fields. It may add opt-in capabilities and optional fields that do not require existing data to be reinterpreted.

A public JSON field-set change uses a new `schemaVersion` and a new checked-in schema; schema `1.0` is never changed in place.

## 1. Scope

For one logical `gomcdc test` measurement request, the tool aggregates eleven metrics over one source model: Statement, Function, Decision, Switch Clause Body, Type Switch Clause Body, Select Clause Body, Switch Clause Selection, Type Switch Clause Selection, Condition, Unique-Cause MC/DC, and Masking MC/DC.

`gomcdc version` performs no measurement, writes the build identity to standard output, and exits with code 0. A module-version build uses `gomcdc vMAJOR.MINOR.PATCH` (or the complete Go module version); a local build uses `gomcdc devel`, optionally followed by the abbreviated VCS revision and `dirty`. Additional arguments are invalid CLI usage. The identity comes from Go build information and requires no linker flag.

The target environment is stable Go 1.26.x (1.26.0 or later), Go Modules, Linux, and macOS. The compiler-aware backend applies an exact-anchor patch to the selected Go compiler source and fails explicitly when those anchors are incompatible. Target sources are packages in the main module returned by `go list` for the supplied package patterns. The target set excludes the standard library, external modules, vendor, tool-generated sources, and sources with the standard Go generated-code comment. `_test.go` belongs to AST metrics only with `--include-tests`; that flag does not affect Statement or Function.

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

A v1 Decision is the whole condition of an `if`, the condition of a conditional `for`, or each case expression of a conditionless switch. Multiple expressions in one case are independent Decisions.

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

Every condition starts as `not-evaluated`. Only an Atom that actually evaluates is updated to true or false. An evaluation is completed only upon reaching `EndDecision`; it is aborted when panic, `runtime.Goexit`, process interruption, or another termination prevents that. TestID is auxiliary information and is `unknown` when unavailable.

### D10. Evaluation function

For a complete assignment `x ∈ {false,true}^n`, `eval(E,x)` is the value of Decision tree `E`. `Comp(v)` is the set of complete assignments consistent with observed vector `v`, including the observed short-circuit path. Condition indices are occurrence identities: v1 does not infer or enforce value coupling between occurrences, even when their source text is equal. Source-language coupling analysis is outside this model; a witness never receives credit from an unproved coupling assumption.

### D11. Clause event

```text
body-execution   : control reached the start of a clause body
direct-selection : dispatch directly selected that clause
no-match         : dispatch without default selected no case
```

Reaching a later body through fallthrough is body-execution, not direct-selection of the later clause.

## 5. Metrics

### D12. Statement Coverage

Each unit in a Go cover-profile block's statement count is an obligation. If the block counter is greater than zero, all its units are covered. The detail unit is a statement region carrying original source range, statement count, and counter.

### D13. Function Coverage

Each Function containing at least one statement unit is an obligation. It is covered when at least one of its statement units is covered. A Function with no statement unit is outside the target set.

### D14. Decision Coverage

Each Decision `d` has obligations `(d,true)` and `(d,false)`. An outcome is covered when it occurs as Result in a completed evaluation.

### D15. Condition Coverage

Each condition occurrence `c` has obligations `(c,true)` and `(c,false)`. Only a value actually evaluated in a completed evaluation is covered. not-evaluated creates neither an additional obligation nor evidence.

### D16. Clause Body Coverage

Each case/default Clause body is one obligation. Expression and conditionless switches belong to Switch Clause Body; type switches to Type Switch Clause Body; selects to Select Clause Body. A corresponding body-execution event covers the obligation.

### D17. Clause Selection Coverage

Each case/default Clause and `NoMatch(SwitchID)` of an expression switch is a Switch Clause Selection obligation; a type switch analogously contributes Type Switch Clause Selection obligations. A direct-selection event carries SwitchID and ClauseID; a no-match event carries SwitchID and the no-match role. A case-alternative index is retained as evidence but does not add a v1 obligation. A fallthrough event carries source and destination ClauseIDs and is not a selection event.

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

An MC/DC percentage covers independent-effect obligations for condition occurrences. Complete MC/DC achievement also requires the corresponding Decision and Condition Coverage. Equal values are not assumed for occurrences sharing source text. A witness requiring unproved occurrence coupling, or an exact search stopped by a resource limit, is analysis-incomplete.

Each MC/DC analysis is a strategy-specific pure function.

```go
type MCDCStrategy interface {
    Analyze(DecisionMetadata, []DecisionEvaluation) MCDCResult
}
```

## 6. Aggregation and evidence

### D20. Aggregation

The module, package, file, function, decision, condition, and clause levels sum integer obligations. A parent percentage is computed from its covered and total sums, never from an average of child percentages.

### D21. Evidence authority

Go standard cover over original sources supplies Statement and Function evidence. The AST backend supplies Decision, Condition, both MC/DC metrics, and the three Clause Body metrics. A compiler-aware backend supplies the two Clause Selection metrics.

For each entity, a backend emits SupportStatus. Instrumentation Coverage carries `discovered`, `supported`, `instrumented`, `unsupported`, and `unknown` for every metric.

`--strict` fails when any requested metric has an unsupported-by-backend, unknown, analysis-incomplete, or supported-but-uninstrumented entity.

### D22. Measurement

MeasurementMode is `standard-cover`, `single-run`, or `dual-run-standard-cover`. Statement/Function alone uses standard-cover; non-standard-cover metrics alone use single-run; selecting both uses dual-run-standard-cover. A dual run executes original-source and instrumented-source runs once each. Each run retains independent status, failure kind, and package status; evaluations are not correlated across runs. The test suite is not repeated separately for each metric using the same evidence.

## 7. Execution model

### D23. Runtime

```go
BeginDecision(decisionID, conditionCount) EvaluationID
EvalCondition(evaluationID, conditionIndex, value) bool
EndDecision(evaluationID, result) bool
AbortDecision(evaluationID)
```

The runtime prevents EvaluationID collisions and has no package-global current evaluation. Identity separates recursion, nesting, loops, goroutines, and processes. Shared runtime state is race-free.

### D24. Semantic preservation

Instrumentation preserves Go evaluation order, short-circuiting, evaluation and call counts, panic conditions, defer order, side-effect order, return values, errors, and user-observable state. A probe records its received value once and returns the same value. Execution time, allocations, stack traces, inlining, scheduling, CPU, and memory are outside the equivalence relation.

### D25. Processes and collection

Each test process writes to a distinct file whose name contains a collision-free encoding of package import path, PID, run ID, and nonce. Every record in one file has the same `(runID, packagePath, PID)` provenance. The CLI retains provenance on raw Decision and Clause events until it verifies the requested run, source-inventory ownership, process identity, condition count, states, result, status, and short-circuit consistency. Only verified events are projected to provenance-free, idempotent coverage observations. It constructs a partial report after test failure, build failure, timeout, panic, a truncated tail, or abnormal termination.

SIGINT and SIGTERM cancel the active request. Cancellation terminates every measurement-owned subprocess group, starts no later measurement phase, and then permits only bounded evidence recovery, report construction, and workspace cleanup. Caller cancellation is `failureKind=interrupted`, not timeout or command failure.

### D26. go test

At least one package pattern is required. The CLI invokes `go test -count=1`. User or GOFLAGS settings for `-count`, `-cover`, `-coverprofile`, `-covermode`, `-coverpkg`, `-json`, `-overlay`, or `-toolexec` are CLI errors. Non-conflicting arguments after `--` apply with the same meaning to analysis and every run. Analysis and tests use identical GOOS, GOARCH, build tags, CGO, and module settings. A go.work with multiple active main modules is outside the target set.

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

### D28. Exit result

```text
0 success
1 one or more go test runs failed
2 measurement, instrumentation, integrity, or report failure
3 coverage threshold failure
4 invalid CLI usage
130 interrupted by SIGINT
143 interrupted by SIGTERM
```

Precedence for ordinary completion is `4 > 2 > 1 > 3 > 0`. A handled termination signal returns its signal-derived exit code after recovery and cleanup. The overall result retains separate test, measurement, integrity, strict, and threshold fields.

`run.results` contains `test`, `measurement`, `integrity`, `strict`, and `threshold`. Each value is one of `passed`, `failed`, `timeout`, `not-run`, or `not-requested`; only test uses `timeout`. Strict and threshold are `not-requested` when their corresponding policy was not specified, and exit-code precedence never overwrites another result field.

### D29. JSON

The root contains `schemaVersion`, `toolVersion`, `module`, `run`, `measurementMode`, `measurements`, `capabilities`, `backendCapabilities`, `instrumentationCoverage`, `summary`, `packages`, and `errors`. `schemaVersion` is the report compatibility contract and is `1.1`; tool interruption adds `interrupted` to `failureKind` without conflating it with timeout or a command failure. `toolVersion` is the build identity from `gomcdc version`. `capabilities` is the tool-wide aggregate, while `backendCapabilities` exposes the per-producer authority required by D21.

[`schema/report-v1.1.schema.json`](../schema/report-v1.1.schema.json) is the machine-readable JSON Schema for every current public field, required and optional key, type, enum, and nullability rule. Schema 1.0 remains checked in as the immutable preceding contract.

The summary keys are `statement`, `function`, `decision`, `switchClauseBody`, `typeSwitchClauseBody`, `selectClauseBody`, `switchClauseSelection`, `typeSwitchClauseSelection`, `condition`, `mcdcUnique`, and `mcdcMasking`.

Each summary carries covered, total, percentage, and support/status counts. IDs are hexadecimal strings. Arrays are ordered by package path, Location, and ID. Decision detail carries expression, Location, outcomes, conditions, both MC/DC results, evaluation vectors, and witnesses. An error carries phase, code, message, and only when applicable package and module-relative path.

### D30. Text

Every enabled metric displays `covered / total = percentage` or `n/a`, with Instrumentation Coverage and excluded-status counts. MC/DC displays each condition result, witness vectors, Decision results, Masking completions, and the unsatisfied rule.

### D31. HTML

`--format html --output <directory>` creates `<directory>/index.html`. HTML consumes only the same Report model and triggers no additional build or test.

The module summary leads to package-centered navigation, followed by files and functions within each package. The function table presents all eleven metrics as independent columns. Function detail presents original-source Locations, Decisions, Conditions, Clause Body, Clause Selection, and both MC/DC results. Partial runs, unsupported, unknown, infeasible, and analysis-incomplete remain distinct from ordinary non-coverage.

The output is one self-contained static HTML file with no external asset, CDN, network request, or JavaScript. Source expressions and paths are escaped for their HTML context. Information does not depend on color alone; numerator, denominator, percentage, and status text are present.

### D32. Resources and trust boundary

One instrumented run collects all non-standard-cover evidence with one source instrumentation, compiler instrumentation, build, and test. The event journal aggregates and compacts without losing witnesses and does not retain every record in memory in proportion to loop iterations. Performance claims require a checked-in benchmark and are not part of v1 conformance.

The tool builds and tests a trusted target module with the current user's authority. The temporary workspace is not a security sandbox, and evidence authenticity is not guaranteed against malicious target code. Temporary directories and event files are readable and writable only by the current user. Source copying prevents symlinks and hardlinks from writing outside the workspace. Normal reports contain no user absolute local path.

The tool defines coverage semantics only and claims no DO-178C compliance, tool qualification, or safety certification. Windows, assembly, cgo internals, coverage whose obligations are compiler IR, path coverage, and distributed test execution are outside v1.

## 9. Conformance conditions

An implementation conforms only when all conditions hold.

1. It implements the types, sets, functions, and external forms of D1–D32.
2. It integrates all eleven metrics over one source model and report.
3. It does not record not-evaluated as false.
4. It does not use an aborted evaluation as coverage evidence or an entity status.
5. It does not merge Clause Body with Clause Selection, or direct selection with fallthrough body execution.
6. It does not name or aggregate Decision Coverage as CFG Edge Coverage.
7. It does not add Boolean expressions used only in assignment, return, or call arguments to the v1 Decision set.
8. It does not infer covered or not-covered from unsupported or unknown evidence.
9. It exposes no generic `clause` metric, `clause` / `clauseBody` summary, metric alias, or ambiguous threshold flag.
10. It does not expose instrumented sources, temporary paths, or generated lines as formal Locations.
11. Package load order, map iteration, goroutine order, and analysis parallelism do not affect IDs or report order.
12. Test caching does not omit a measurement run.
13. Test processes do not write to the same evidence file.
14. A compiler-aware backend does not mark an unsupported Go version as supported.
15. Completion does not omit integrated C0, either MC/DC strategy, multiple packages, source mapping, concurrent evaluation, or Clause Selection.

## 10. Acceptance verification

The acceptance suite includes AND, OR, NOT, nested expressions, side effects, evaluation order, conditionless switch, expression switch, type switch, select, empty bodies, fallthrough, direct selection, no-match, recursion, nested decisions, loops, multiple goroutines, panic, recover, defer, `runtime.Goexit`, multiple packages, external test packages, build tags, test/build failure, timeout, truncated events, partial recovery, provenance mutation, source mapping, user `//line`, generated-code exclusion, JSON Schema contract mutation, and all eleven thresholds. For `a && b`, Unique-Cause yields `a=infeasible` and `b=covered`, while Masking covers both conditions.

Completion requires successful `go test -count=1 ./...`, `go test -race -count=1 ./...`, and `go vet ./...`, and integer equality between fixture-module aggregation and the sum of its package aggregations.

Repository CI measures gomcdc itself on the latest stable Go 1.26.x/Linux toolchain and checks the module and critical-package floors in `.github/self-mcdc-baseline.json`. This baseline is a maintenance contract that prevents regression from the verified test suite, not a coverage-conformance threshold defined by this specification. A change that lowers a floor must identify and justify the obligations being lost during review.

## 11. References

The semantic references are FAA CAST-10, the NASA MC/DC tutorial, Hayhurst et al., Chilenski, the Go language specification, and Go `cmd/cover`. References do not override D1–D32.
