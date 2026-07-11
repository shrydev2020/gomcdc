# gomcdc 規範仕様

本書は `gomcdc` 1.0 の意味論と適合条件を定める。日本語版を規範版とし、英語版は同じ定義番号を持つ参考訳とする。仕様バージョンは `1.0-draft` である。

## 1. 適用範囲

`gomcdc test` は Go module の一回の論理的計測要求から、Statement、Function、Decision、Switch Clause Body、Type Switch Clause Body、Select Clause Body、Switch Clause Selection、Type Switch Clause Selection、Condition、Unique-Cause MC/DC、Masking MC/DC の11指標を一つの source model 上へ集約する。

対象は Go 1.26、Go Modules、Linux、macOS とする。対象 source は package pattern を `go list` して得た main module 内の package である。標準 library、外部 module、vendor、本ツール生成source、Go標準形式のgenerated-code commentを持つsourceは対象集合に含めない。`_test.go` は `--include-tests` 指定時だけ AST 系指標へ含め、このflagはStatement/Functionへ影響しない。

## 2. 基本領域

### D1. Source location

`Location = (file, startLine, startColumn, endLine, endColumn)` とする。`file` は module root からの物理相対 path、行と列は1始まりである。公開位置は計装前 source の `Location` へ正規化する。

### D2. 識別子

`DecisionID`、`ConditionID`、`ClauseID` は同一 source revision に対して決定論的である。生成関数は module path、package import path、相対 file path、node kind、開始 offset、終了 offset、condition index だけを入力にできる。

`EvaluationID` は process 内で decision 評価開始ごとに一意である。複数 process を含む評価 identity は `(runID, packagePath, PID, evaluationID)` である。

### D3. 状態

```text
ConditionState  = true | false | not-evaluated
EvaluationStatus = completed | aborted
CoverageStatus  = covered | not-covered | infeasible | analysis-incomplete
SupportStatus   = supported | unsupported-by-backend | unknown
```

`not-evaluated` は condition が実行されなかった観測であり boolean 値ではない。`aborted` は evaluation status にだけ属する。

### D4. Coverage count

指標 `m` の obligation 集合を `O_m`、covered obligation 集合を `C_m ⊆ O_m` とする。

```text
covered(m) = |C_m|
total(m)   = |O_m|
ratio(m)   = |C_m| / |O_m|   if |O_m| > 0
ratio(m)   = undefined       if |O_m| = 0
```

`unsupported-by-backend`、`unknown`、`infeasible`、`analysis-incomplete` の entity は `O_m` から除き、件数を別集計する。undefined は text で `n/a`、JSON で `null` とする。

## 3. Source model

### D5. Function

Function は function declaration、method declaration、function literal のいずれかである。複数の `init` と function literal は Location で区別する。

### D6. Decision

v1 の Decision は `if` の condition 全体、condition を持つ `for` の condition 全体、conditionless switch の各 case expression である。一つの case に複数 expression がある場合、各 expression を独立 Decision とする。

### D7. Boolean expression tree

Decision expression を次の木へ変換する。

```text
Expr ::= Atom(conditionIndex) | Not(Expr) | And(Expr, Expr) | Or(Expr, Expr)
```

Atom は `&&`、`||`、`!` を含まない bool 型 source expression occurrence である。比較、call、selection、map lookup の bool 結果は一つの Atom とする。index は source の左から右への構文順で `0..n-1` を割り当てる。`!a` の Atom は `a` であり、否定は `Not` node が表す。

### D8. Clause

Clause は expression switch、type switch、conditionless switch、select の case または default 節である。同一 switch の `case A, B` は一つの Clause とする。

`ClauseRole = case | default` とする。Switch dispatchは `SwitchID` を持つ。defaultを持たないexpression switchとtype switchにはsource Clauseとは別の `NoMatch(SwitchID)` selection obligationを一つ置く。

## 4. 観測意味論

### D9. Decision evaluation

condition 数 `n` の一回の評価は次で表す。

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

開始時に全 condition state を `not-evaluated` とする。実際に評価した Atom だけを true または false へ更新する。`EndDecision` に到達した評価だけを completed とし、panic、`runtime.Goexit`、process interruption 等で到達しなかった評価を aborted とする。TestID は補助情報であり、取得不能時は `unknown` とする。

### D10. 評価関数

完全 assignment `x ∈ {false,true}^n` に対する Decision tree の値を `eval(E,x)` とする。観測 vector `v` と整合する完全 assignment の集合を `Comp(v)` とする。

### D11. Clause event

```text
body-execution   : clause body の先頭へ制御が到達した
direct-selection : dispatch がその clause を直接選択した
no-match         : default のない dispatch がどの case も選択しなかった
```

fallthrough による後続 body 到達は body-execution であり、後続 clause の direct-selection ではない。

## 5. 指標

### D12. Statement Coverage

Go cover profile block の statement count の各 unit を obligation とする。block counter が0より大きいとき、その block の全 unit を covered とする。詳細単位は元 source range、statement count、counter を持つ statement region である。

### D13. Function Coverage

一つ以上の statement unit を持つ Function を obligation とする。その Function に属する statement unit が一つ以上 covered なら covered とする。statement unit を持たない Function は対象集合外である。

### D14. Decision Coverage

各 Decision `d` に `(d,true)` と `(d,false)` を obligation として置く。completed evaluation の Result に現れた outcome を covered とする。

### D15. Condition Coverage

各 condition occurrence `c` に `(c,true)` と `(c,false)` を obligation として置く。completed evaluation で実際に評価された値だけを covered とする。not-evaluated は追加 obligation も証拠も生成しない。

### D16. Clause Body Coverage

各 case/default Clause body を一つの obligation とする。expression switch と conditionless switch は Switch Clause Body、type switch は Type Switch Clause Body、select は Select Clause Body に属する。対応する body-execution event があれば covered とする。

### D17. Clause Selection Coverage

expression switch の各 case/default Clauseと `NoMatch(SwitchID)` を Switch Clause Selection の obligation とする。type switchも同様にType Switch Clause Selectionのobligationを持つ。direct-selection eventはSwitchIDとClauseID、no-match eventはSwitchIDとno-match roleを持つ。case alternative indexは証拠として保持するがv1分母へ追加しない。fallthrough eventはsource/destination ClauseIDを持ち、selection eventではない。

### D18. Unique-Cause MC/DC

condition `i` は、completed evaluation pair `(p,q)` が次をすべて満たすとき covered である。

1. `p[i]` と `q[i]` は boolean 値である。
2. `p[i] ≠ q[i]`。
3. `p.result ≠ q.result`。
4. すべての `j ≠ i` について `p[j] = q[j]`。

not-evaluated は boolean 値と等しくない。成立 pair を witness として保存する。構造上成立しない obligation は infeasible とする。

### D19. Masking MC/DC

完全 assignment `z` の condition `j` を反転した assignment を `flip(z,j)` とし、`masked(E,z,j) := eval(E,z) = eval(E,flip(z,j))` と定義する。

condition `i` は、completed evaluation pair `(p,q)` と completion `(x,y) ∈ Comp(p) × Comp(q)` が存在し、次をすべて満たすとき covered である。

1. `p[i]` と `q[i]` は boolean 値である。
2. `x[i] ≠ y[i]`。
3. `eval(E,x) ≠ eval(E,y)`。
4. 各 `j ≠ i` について、`x[j] = y[j]` または `masked(E,x,j) ∧ masked(E,y,j)`。

観測 pair、completion、masked condition index を witness として保存する。

MC/DC percentageはcondition occurrenceの独立影響obligation達成率である。完全なMC/DC達成には対応するDecision CoverageとCondition Coverageも必要である。同じsource textのcondition occurrence間に値の同一性を仮定しない。witnessが未証明のoccurrence couplingを必要とする場合、または正確な探索がresource limitで完了しない場合はanalysis-incompleteとする。

MC/DC 解析は strategy ごとの pure function とする。

```go
type MCDCStrategy interface {
    Analyze(DecisionMetadata, []DecisionEvaluation) MCDCResult
}
```

## 6. 集約と証拠

### D20. 集約

module、package、file、function、decision、condition、clause の各階層で obligation の整数和を取る。上位 percentage は covered 合計と total 合計から計算する。

### D21. Evidence authority

Statement と Function の証拠は元 source を対象とした Go standard cover が生成する。Decision、Condition、MC/DC、3種の Clause Body は AST backend、2種の Clause Selection は compiler-aware backend が生成する。

backend は entity ごとに SupportStatus を出力する。Instrumentation Coverage は指標ごとに `discovered`、`supported`、`instrumented`、`unsupported`、`unknown` を持つ。

`--strict` は、要求指標に unsupported-by-backend、unknown、analysis-incomplete、または未計装 entity が一つでもあれば失敗する。

### D22. Measurement

MeasurementMode は `standard-cover`、`single-run`、`dual-run-standard-cover` である。Statement/Function だけなら standard-cover、非standard-cover指標だけなら single-run、両方なら dual-run-standard-cover とする。dual-run は元source runと計装runを一回ずつ実行する。各runのstatus、failure kind、package statusを独立して保持し、run間でevaluationを対応付けない。同一指標を得るためにtest suiteを指標別に反復しない。

## 7. 実行モデル

### D23. Runtime

```go
BeginDecision(decisionID, conditionCount) EvaluationID
EvalCondition(evaluationID, conditionIndex, value) bool
EndDecision(evaluationID, result) bool
AbortDecision(evaluationID)
```

runtime は EvaluationID を衝突させず、package-global な current evaluation を持たない。再帰、nest、loop、複数 goroutine、複数 process を identity で分離する。共有状態は race-free である。

### D24. 意味保存

計装変換は Go の評価順序、短絡、評価回数、call 回数、panic 条件、defer 順序、副作用順序、return value、error、ユーザーから観測可能な状態を保存する。probe は受け取った値を一度だけ記録し、同じ値を返す。実行時間、allocation、stack trace、inlining、scheduling、CPU、memory は同値条件に含めない。

### D25. Process と回収

各 test process は package import path の衝突不能なencoding、PID、run ID、nonceを含む固有 file へ evidence を書く。CLI は完全に検証できたrecordを収集しmodule reportへmergeする。DecisionID、EvaluationIdentity、condition count、state、result、statusと短絡規則の整合性を検証する。test failure、build failure、timeout、panic、truncated tail、異常終了時も取得済み evidence と静的 inventory から partial report を構成する。

### D26. go test

package pattern は一つ以上必須とする。CLI は `go test -count=1` を使用する。ユーザーまたはGOFLAGSによる `-count`、`-cover`、`-coverprofile`、`-covermode`、`-coverpkg`、`-json`、`-overlay` はCLI errorとする。`--`以降の非競合引数は解析と全runへ同じ意味で適用する。解析とtestは同じGOOS、GOARCH、build tags、CGO、module設定を使用する。activeな複数main moduleを持つgo.workは対象外とする。

## 8. 外部形式

### D27. CLI

`--coverage` の正式値は次の11個と `all` である。default は `all` とする。

```text
statement, function, decision,
switch-clause-body, type-switch-clause-body, select-clause-body,
switch-clause-selection, type-switch-clause-selection,
condition, mcdc-unique, mcdc-masking, all
```

閾値 flag は各指標に一対一で対応する `--fail-under-<metric>` とする。選択されていない指標への閾値は CLI error、total が0の指標への閾値は未達とする。比較は丸め前の整数比で行う。

### D28. 終了結果

```text
0 success
1 one or more go test runs failed
2 measurement, instrumentation, integrity, or report failure
3 coverage threshold failure
4 invalid CLI usage
```

優先順位は `4 > 2 > 1 > 3 > 0` とする。overall result は test、measurement、integrity、strict、threshold の結果を別 field で保持する。

### D29. JSON

root は `version`、`module`、`run`、`measurementMode`、`measurements`、`instrumentationCoverage`、`summary`、`packages`、`errors` を持つ。draft 中の version は `1.0-draft` とする。

summary key は `statement`、`function`、`decision`、`switchClauseBody`、`typeSwitchClauseBody`、`selectClauseBody`、`switchClauseSelection`、`typeSwitchClauseSelection`、`condition`、`mcdcUnique`、`mcdcMasking` である。

各 summary は covered、total、percentage、support/status 別件数を持つ。ID は16進文字列、array は package path、Location、ID の順に整列する。Decision detail は expression、Location、outcome、condition、両 MC/DC、evaluation vector、witness を持つ。error は phase、code、message と該当時だけ package、module-relative path を持つ。

### D30. Text

有効な全指標について `covered / total = percentage` または `n/a` を表示する。Instrumentation Coverage と除外 status 件数を併記する。MC/DC は condition ごとの結果、witness vector、Decision result、Masking completion、成立しない規則を表示する。

### D31. Resource と trust boundary

一つの計装runは一回のsource計装、compiler計装、build、testで全非standard-cover証拠を収集する。event journalはwitnessを失わない形で集約・compactionし、loop回数に比例する全recordをmemoryへ保持しない。通常の `go test` に対する目標はAST-onlyで2倍以内、両MC/DC込みで5倍以内だが、この目標は適合判定に使用しない。

対象moduleは信頼済みコードとして現在ユーザー権限でbuild/testする。一時workspaceはsecurity sandboxではなく、悪意ある対象コードに対するevidence真正性を保証しない。一時directoryとevent fileは現在ユーザーだけが読み書きできるpermissionとし、source複製時にsymlink/hardlinkからworkspace外へ書き込まない。通常reportはユーザーの絶対local pathを含めない。

本ツールはcoverage意味論だけを定義し、DO-178C適合、tool qualification、安全認証を主張しない。Windows、assembly、cgo内部、compiler IR、path coverage、distributed test executionはv1対象外である。

## 9. 適合条件

実装は次の全条件を満たすとき本仕様へ適合する。

1. D1–D31 の型、集合、関数、外部形式を実装する。
2. 11指標を同一 source model と同一 report に統合する。
3. not-evaluated を false として記録しない。
4. aborted evaluation を coverage evidence または coverage entity status に使用しない。
5. Clause Body と Clause Selection、direct selection と fallthrough body execution を統合しない。
6. Decision Coverage を CFG Edge Coverage と呼称または集計しない。
7. assignment、return、call argument の bool expression を v1 Decision 集合へ追加しない。
8. unsupported または unknown evidence を covered / not-covered と推測しない。
9. 汎用 `clause` 指標、`clause` / `clauseBody` summary、指標 alias、曖昧な threshold flag を公開しない。
10. 計装 source、temporary path、生成行を正式 Location として公開しない。
11. package load 順、map iteration 順、goroutine 順、解析並列度を ID または report 順序へ反映しない。
12. test cache によって計測実行を省略しない。
13. 個別 process が同じ evidence file へ書き込まない。
14. compiler-aware backend が未対応の Go version を supported として処理しない。
15. 完成条件から C0統合、両 MC/DC、複数 package、source mapping、並行評価、Clause Selection を除外しない。

## 10. 受け入れ検証

受け入れ suite は AND、OR、NOT、nested expression、side effect、evaluation order、conditionless switch、expression switch、type switch、select、empty body、fallthrough、direct selection、no-match、再帰、nested decision、loop、複数 goroutine、panic、recover、defer、`runtime.Goexit`、複数 package、external test package、build tag、test/build failure、timeout、truncated event、partial recovery、source mapping、ユーザー `//line`、生成code除外、全11閾値を含む。`a && b` は Unique-Causeで `a=infeasible`、`b=covered`、Maskingで両condition coveredとなる。

`go test -count=1 ./...`、`go test -race -count=1 ./...`、`go vet ./...` が成功し、fixture module の module 集計と package 集計の整数和が一致することを完成条件とする。

## 11. 参考資料

FAA CAST-10、NASA MC/DC tutorial、Hayhurst et al.、Chilenski、Go language specification、Go `cmd/cover` を参照する。参考資料は D1–D31 を上書きしない。
