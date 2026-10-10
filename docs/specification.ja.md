# gomcdc 規範仕様

本書は `gomcdc` 2.0 の意味論と適合条件を定める。日本語版を規範版とし、英語版は同じ定義番号を持つ参考訳とする。仕様バージョンは `2.0` である。

`gomcdc` v2系列は本仕様を互換性契約とする。v2系列内では、既存指標、CLI optionと終了code、schema 2.0のJSON必須fieldを、それぞれの意味とともに維持する。既存データの解釈を保ちながら、opt-in capabilityとoptional fieldを追加できる。

公開JSONのfield集合を変更するときは新しい `schemaVersion` とrepository内schemaを追加し、schema `1.0` と `1.1` は不変のまま保持する。

## 1. 適用範囲

`gomcdc test` は Go module の一回の論理的計測要求から、Statement、Function、Decision、Switch Clause Body、Type Switch Clause Body、Select Clause Body、Switch Clause Selection、Type Switch Clause Selection、Condition、Unique-Cause MC/DC、Masking MC/DC の11指標を一つの source model 上へ集約する。

`gomcdc version` はbuild identityを標準出力へ書き、終了code 0を返す。module versionからbuildした場合は `gomcdc vMAJOR.MINOR.PATCH`（または完全なGo module version）、local buildでは `gomcdc devel` に省略VCS revisionと `dirty` を必要に応じて付加する。追加argumentはinvalid CLI usageである。identityはGo build informationから直接取得する。

対象はGo 1.26.0以降、Go Modules、Linux、macOS とする。compiler-aware backendは選択されたGo compiler sourceへexact-anchor patchを適用し、anchorの互換性検査に失敗した場合は明示的にエラーを返す。後続releaseにも同じsource anchorの互換性検査を適用する。対象 source は package pattern を `go list` して得た main module 内の package である。標準 library、外部 module、vendor、本ツール生成source、Go標準形式のgenerated-code commentを持つsourceは対象集合から除外する。`_test.go` は `--include-tests` 指定時だけ、Statement/Functionを含む選択した全指標へ含める。activeなinternal/external test fileにも、通常のbuild tag、generated source、除外規則を適用する。

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

Decision は `if` の condition 全体、condition を持つ `for` の condition 全体、conditionless switch の各 case expression である。一つの case に複数 expression がある場合、各 expression を独立 Decision とする。

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

開始時に全 condition state を `not-evaluated` とする。実際に評価した Atom だけを true または false へ更新する。`EndDecision` に到達した評価だけを completed とし、panic、`runtime.Goexit`、process interruption 等で到達前に終了した評価を aborted とする。TestID は評価を実行した Go test／subtest の名前を保持し、その test が起動した goroutine と cleanup callback にも引き継ぐ。test の入口の計装は `--include-tests` と独立して評価の出所を記録する。test code の分母への追加は、このoptionで選択する。異なる test／process の同一 vector は report の証拠として別々に保持する。MC/DC 判定では証拠 pair の探索時に同一 vector を集約できる。初期化など test context 外の実行は `unknown` とする。

### D10. 評価関数

完全 assignment `x ∈ {false,true}^n` に対する Decision tree の値を `eval(E,x)` とする。`Comp(v)` は観測された短絡評価経路を含め、観測 vector `v` と整合する完全 assignment の集合とする。condition indexは各occurrenceの識別子であり、source textが同じ場合も個別に識別する。v1のBoolean modelでは各occurrenceの値を独立変数として扱う。source-language coupling解析はこのモデルの対象外であり、witnessはこれらの独立変数を使って成立条件を満たす必要がある。

### D11. Clause event

```text
body-execution   : clause body の先頭へ制御が到達した
direct-selection : dispatch がその clause を直接選択した
no-match         : default のない dispatch がどの case も選択しなかった
```

fallthrough による後続 body 到達は、その clause の body-execution として記録する。direct-selection はdispatchが直接選んだclauseを記録する。

## 5. 指標

### D12. Statement Coverage

変更前の元source inventoryのstatement unitをobligationとする。各unitに対応する受理済みGo cover counterが0より大きいときcoveredとする。producerがbasic blockを複数regionへ分割した場合も、NumStmtの重複をまとめて元sourceの各obligationを一度だけ数える。異なるcounterを持つunitは別々のstatement regionとして表示する。詳細単位は元 source range、statement count、counter を持つ statement region である。

### D13. Function Coverage

一つ以上の statement unit を持つ Function を obligation とする。その Function に属する statement unit が一つ以上 covered なら covered とする。

### D14. Decision Coverage

各 Decision `d` に `(d,true)` と `(d,false)` を obligation として置く。completed evaluation の Result に現れた outcome を covered とする。

### D15. Condition Coverage

各 condition occurrence `c` に `(c,true)` と `(c,false)` を obligation として置く。completed evaluation で実際に評価された値だけを covered とする。`not-evaluated`は観測状態として保持し、coverageのobligationと証拠には上記のboolean outcomeを使う。

### D16. Clause Body Coverage

各 case/default Clause body を一つの obligation とする。expression switch と conditionless switch は Switch Clause Body、type switch は Type Switch Clause Body、select は Select Clause Body に属する。対応する body-execution event があれば covered とする。

### D17. Clause Selection Coverage

expression switch の各 case/default Clauseと `NoMatch(SwitchID)` を Switch Clause Selection の obligation とする。type switchも同様にType Switch Clause Selectionのobligationを持つ。direct-selection eventはSwitchIDとClauseID、no-match eventはSwitchIDとno-match roleを持つ。case alternative indexはclause単位のv1 obligationに対する証拠として保持する。fallthrough eventはbody-executionの来歴としてsource/destination ClauseIDを持つ。

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

検証済みread-once AND/OR/NOT式modelに対し、Masking MC/DCは探索状態を共有して、
両completionをexact joint searchする。condition obligationごとのdefault上限は、candidate
evaluation pair 1,000,000件、新しく展開するjoint search state 4,000,000件、主要solver
backing array 64 MiBである。solver byte単位にはflatten済み式、memo table、completion
buffer、candidate evaluation indexを含め、検証済みinput data、result witness、goroutine
stack、その他のprocess memoryは含めない。この値はprocess全体のheap、RSS、総memory上限ではない。
上限を超える探索が必要な場合は`analysis-incomplete`とし、上限ちょうどで
witnessへ到達した場合は`covered`とする。`infeasible`は、targetがpivotalになり得ないことを
別の構造検査で証明した場合だけ使用する。

MC/DC percentageはcondition occurrenceの独立影響obligation達成率である。完全なMC/DC達成には対応するDecision CoverageとCondition Coverageも必要である。同じsource textを持つcondition occurrenceも独立変数として解析する。witnessが未証明のoccurrence couplingを必要とする場合、または正確な探索がresource limitで中断した場合はanalysis-incompleteとする。

MC/DC 解析は strategy ごとの pure function とする。

```go
type MCDCStrategy interface {
    Analyze(DecisionMetadata, []DecisionEvaluation) MCDCResult
}
```

Masking MC/DC選択時、report構築は独立したDecisionをbounded worker poolで解析してよい。
実効worker数はDecision数、`GOMAXPROCS`、4の最小値以下とする。実行中の各DecisionはD19の
condition obligation単位上限を全量そのまま持つ。各workerが専用のsolver stateを保持し、
結果を事前にsortしたDecision indexへ格納する。status、witness選択、集約、出力順は
解析並列度にかかわらず同一となる。cancellationは新しいMasking解析の割当てを終了し、
受理済みevidenceと他metricを保持する。未開始のMasking obligationは
`analysis-incomplete`とする。

## 6. 集約と証拠

### D20. 集約

module、package、file、function、decision、condition、clause の各階層で obligation の整数和を取る。上位 percentage は covered 合計と total 合計から計算する。

### D21. Evidence authority

Statement と Function の証拠は元 source を対象とした Go standard cover が生成する。選択されたGo coverの座標系とcode range分割は、実際の計装出力から識別する。producerのNumStmtと元sourceのstatement obligation数を分離し、物理座標で出力されるregionでも各statementの論理source identityを保持する。分割されたregionはstatement数とcounterを個別に保持し、各region自身の受理済みcounterからcoverageを判定する。Decision、Condition、MC/DC、3種の Clause Body は AST backend、2種の Clause Selection は compiler-aware backend が生成する。

変更前の元source Inventoryを唯一のdenominator authorityとする。raw producer出力はintegrity、provenance、producer compatibility、correspondence mapping、実行completenessを検証した後にだけcoverage evidenceとなる。受理済みevidenceを元Inventory obligationへ射影する。分母は元Inventoryのobligation集合を維持する。

計装処理はこれと分離して、immutableなsource revision間のbyte座標による変換来歴を記録する。
revisionはstage identity、content digest、size、zero-based byte unitを明示する。全領域を覆うhalf-open
range relationはpreserved、synthetic、deleted、ambiguous、lossyを区別し、one-to-manyと
many-to-oneの来歴も表現する。AST printer出力はfile全体がbyte-identicalな場合を除いてlossyとする。
composeには中間revisionのidentity、digest、sizeの一致とrelation境界の完全一致を要求し、partial
matchはエラーとする。このmodelは変換来歴の記録を担う。Go-cover correspondenceと
変更前Inventoryをそれぞれ独立したacceptance authority、obligation authorityとして維持する。

要求されたproducerは4軸を独立に報告する。`integrity` は `valid`、`valid-prefix`、`invalid`、`unavailable`、`completeness` は `complete`、`partial`、`unavailable`、`mapping` は `complete`、`invalid`、`unavailable`、`usability` は `accepted`、`accepted-partial`、`rejected` のいずれかとする。usabilityは実行層が決定する。report構築はその決定をproducerごとに適用し、他のproducerの受理済みevidenceを保持する。

backend は entity ごとに SupportStatus を出力する。Instrumentation Coverage は指標ごとに `discovered`、`supported`、`instrumented`、`unsupported`、`unknown` を持つ。

`--strict` は、要求指標に unsupported-by-backend、unknown、analysis-incomplete、または未計装 entity が一つでもあれば失敗する。

### D22. Measurement

MeasurementMode は `standard-cover` または `single-run` である。Statement/Functionだけなら `standard-cover`、非standard-cover指標を一つでも含む要求は、全11指標要求を含めて `single-run` とする。一つのmeasurement sessionは一つのdisposable workspaceを作り、選択された各packageのtest binaryを正確に一度だけ実行する。Go cover、AST runtime、compiler-aware producerは同じ物理実行を観測する。reportは一つのrun status、failure kind、package-status mapを保持する。旧v1二実行動作は決定的test oracle専用として保持する。

`--include-tests` 指定時は、`go test -cover` が省くtest fileにも選択された
`go tool cover`を適用する。出力されたcounter metadataは、AST書換えを含む
producer inventoryとの完全一致を計装時に検証する。test counter transportは
各counterのhit有無をset membershipとして記録する。source/processごとの独立fileに、
run IDとpackage、source path、計装済み内容のdigestを保持する。初期値0のcounterを
atomicに公開し、各counterの初回hitをsource block実行前に同期書込みする。
collectorはidentity、長さ、counter値を検証し、通常Go coverと同じ元sourceへの
correspondenceで投影する。中断時に欠けた証拠はunknownとする。回収には、その一回の
test実行中に公開済みの証拠を使い、終了callback前に中断した実行も扱う。

## 7. 実行モデル

### D23. Runtime

```go
BeginDecision(decisionID, conditionCount) EvaluationID
EvalCondition(evaluationID, conditionIndex, value) bool
EndDecision(evaluationID, result) bool
AbortDecision(evaluationID)
```

runtime は各評価に一意なEvaluationIDを割り当て、そのidentityごとに状態を保持する。再帰、nest、loop、複数 goroutine、複数 process を identity で分離する。共有状態は race-free である。

### D24. 意味保存

計装変換は Go の評価順序、短絡、評価回数、call 回数、panic 条件、defer 順序、副作用順序、return value、error、ユーザーから観測可能な状態を保存する。probe は受け取った値を一度だけ記録し、同じ値を返す。実行時間、allocation、stack trace、inlining、scheduling、CPU、memory は計装による変化を許容する。

### D25. Process と回収

各 test process は package import path の衝突不能なencoding、PID、run ID、nonceを含む固有 file へ evidence を書く。同一file内の全recordは同じ `(runID, packagePath, PID)` provenanceを持つ。CLIはraw Decision eventとClause eventのprovenanceを、要求run、source inventory上の所有package、process identity、condition count、state、result、status、短絡規則の整合性を検証するまで保持する。検証済みeventだけをprovenance-freeで冪等なcoverage observationへ射影する。test failure、build failure、timeout、panic、truncated tail、異常終了時も取得済み evidence と静的 inventory から partial report を構成する。

このpartial recoveryはtest processまたはtool processの異常終了を対象とする。hostの電源断、kernel crash、filesystem corruption、storage device故障後のdurabilityは保証しない。

SIGINTとSIGTERMはactive requestをcancelする。cancellationはmeasurement所有の全subprocess groupと後続measurementの割当てを終了する。その後は時間制限付きevidence recovery、report構築、workspace cleanupだけを許可する。caller cancellationには専用の結果 `failureKind=interrupted` を使う。

### D26. go test

package pattern は一つ以上必須とする。CLI は `go test -count=1` を使用する。ユーザーまたはGOFLAGSによる `-count`、`-cover`、`-coverprofile`、`-covermode`、`-coverpkg`、`-json`、`-overlay`、`-toolexec` はCLI errorとする。`--`以降の非競合引数は解析と全runへ同じ意味で適用する。解析とtestは同じGOOS、GOARCH、build tags、CGO、module設定を使用する。activeなgo.workはmain moduleが一つであることを要求する。

## 8. 外部形式

### D27. CLI

`--coverage` の正式値は次の11個と `all` である。default は `all` とする。

```text
statement, function, decision,
switch-clause-body, type-switch-clause-body, select-clause-body,
switch-clause-selection, type-switch-clause-selection,
condition, mcdc-unique, mcdc-masking, all
```

閾値 flag は各指標に一対一で対応する `--fail-under-<metric>` とし、指定には対応指標の選択を要求する。この条件との不一致はCLI error、total が0の指標への閾値は未達とする。比較は丸め前の整数比で行う。

Masking MC/DCのcondition obligation単位上限は、`--mcdc-masking-max-evaluation-pairs`、
`--mcdc-masking-max-search-states`、`--mcdc-masking-max-solver-bytes`で変更できる。指定値は
正の10進整数とし、指定には`mcdc-masking`の選択を要求する。この条件との不一致はCLI errorとする。未指定fieldはD19の
defaultを個別に使用する。上限を増やすとconditionとdecisionの数に応じて総処理量が増え得る。

### D28. 終了結果

```text
0 success
1 the go test run failed
2 measurement, instrumentation, integrity, or report failure
3 coverage threshold failure
4 invalid CLI usage
130 interrupted by SIGINT
143 interrupted by SIGTERM
```

通常完了の優先順位は `4 > 2 > 1 > 3 > 0` とする。処理したtermination signalはrecoveryとcleanup後にsignal由来の終了codeを返す。overall result は test、measurement、integrity、strict、threshold の結果を別 field で保持する。

`run.results` は `test`、`measurement`、`integrity`、`strict`、`threshold` を持つ。各値は `passed`、`failed`、`timeout`、`not-run`、`not-requested` のいずれかとする。`timeout` は test にだけ使用する。strict と threshold は対応する policy の指定を省略した場合に `not-requested` とする。各結果fieldは終了codeの優先順位と独立して自身の結果を保持する。

### D29. JSON

root は `schemaVersion`、`toolVersion`、`module`、`run`、`measurementMode`、`measurements`、`producerOutcomes`、`capabilities`、`backendCapabilities`、`instrumentationCoverage`、`summary`、`packages`、`errors` を持つ。Masking MC/DC要求時は任意field `maskingAnalysisLimits`にD19で使用した3つの実効上限を記録する。通常の計測の`schemaVersion` はreport互換性契約であり `2.0` とする。tool interruptionには専用の`failureKind`値 `interrupted` を使う。`toolVersion` は `gomcdc version` が返すbuild identityである。`producerOutcomes` は要求された全producerについてD21の4 evidence軸を持つ。`capabilities` はtool全体のaggregate、`backendCapabilities` はproducer別instrumentation authorityを表す。

[`schema/report-v2.0.schema.json`](../schema/report-v2.0.schema.json) は通常計測の公開field、必須・任意key、型、enum、nullabilityを定める機械可読JSON Schemaである。オフラインReport 2.1はD34で定める。Report schema 1.0と1.1は不変の旧契約としてrepositoryに保持する。

summary key は `statement`、`function`、`decision`、`switchClauseBody`、`typeSwitchClauseBody`、`selectClauseBody`、`switchClauseSelection`、`typeSwitchClauseSelection`、`condition`、`mcdcUnique`、`mcdcMasking` である。

各 summary は covered、total、percentage、support/status 別件数を持つ。ID は16進文字列、array は package path、Location、ID の順に整列する。Decision detail は expression、Location、outcome、condition、両 MC/DC、evaluation vector、witness を持つ。error は phase、code、message と該当時だけ package、module-relative path を持つ。

### D30. Text

有効な全指標について `covered / total = percentage` または `n/a` を表示する。Instrumentation Coverage と除外 status 件数を併記する。MC/DC は condition ごとの結果、witness vector、Decision result、Masking completion、達成に必要な規則を表示する。

### D31. HTML

`--format html --output <directory>` は `<directory>/index.html` を生成する。HTMLは同じReport modelだけを使って描画する。

HTMLはmodule summaryからpackageを主navigationとし、package内をfile、functionの順に表示する。function一覧は11指標を独立列として表示し、function detailは元source Location、Decision、Condition、Clause Body、Clause Selection、両MC/DC結果を表示する。partial run、unsupported、unknown、infeasible、analysis-incompleteを通常の未達と区別する。

HTMLは各producerのintegrity、completeness、mapping、usabilityをoverall run statusと独立して表示する。

出力は表示用assetをすべて内包する単一のself-contained static HTMLとし、宣言的なHTMLとCSSだけで構成する。閲覧はオフラインで完結し、network requestを発生させない。source expressionとpathはHTML contextに従ってescapeする。色に加え、分子、分母、percentage、status textを併記する。

### D32. Resource と trust boundary

一つの計装runは一回のsource計装、compiler計装、build、testで要求されたGo cover、AST、compiler-aware evidenceをすべて収集する。event journalはwitnessを保存しながらrecordを集約・compactionし、memoryには集約済みevidenceを保持する。performance claimにはrepository内benchmarkと再現可能なprofileを必要とする。機能適合は意味論の要件で判定し、実行時間はbenchmark対象hardwareで評価する。

対象moduleは信頼済みコードとして現在ユーザー権限でbuild/testする。一時workspaceはsecurity sandboxではなく、悪意ある対象コードに対するevidence真正性を保証しない。一時directoryとevent fileは現在ユーザーだけが読み書きできるpermissionとし、source複製の書込先はsymlink/hardlinkを経由する場合もworkspace内に限定する。通常reportはmodule-relativeなsource pathを使う。

本ツールはcoverage意味論だけを定義し、DO-178C適合、tool qualification、安全認証を主張しない。Windows、assembly、cgo内部、compiler IRをobligationとするcoverage、path coverage、distributed test executionはv2対象外である。

### D33. 保存結果とオフラインreport

`gomcdc test --save <file>` は、通常のreport出力に加え、判定済みReport、HTML用の元source、計測条件を一つのJSON fileへ保存する。Reportを生成できるtest失敗、partial run、strict失敗、threshold未達でも保存する。既存のtest終了codeの意味を維持し、保存失敗は終了code 2とする。

保存形式は `format=gomcdc-saved-result`、`version`、`sha256`、`result` を持つ独立の契約である。新規計測は[saved-result schema 1.1](../schema/saved-result-v1.1.schema.json)を使い、計測前に128-bit乱数の`settings.invocationId`を付けて、同じ観測結果になった独立のC0実行も区別する。[schema 1.0](../schema/saved-result-v1.0.schema.json)も引き続き読める。通常の計測は引き続きReport schema 2.0を使う。`result`は`settings`、`report`、`sources`を持ち、D34の派生結果は`inputs`も持つ。

settingsにはmodule-relativeな実行directory、要求package patternと解決後package、指標、path再配置前のtest引数とGOFLAGS、include-tests、除外pattern、timeout、strict、各閾値、Goのversion・GOOS・GOARCH・CGO・experiment・architecture設定を記録する。sourceDigestsは選択されたactive Go sourceとtest sourceの計装前SHA-256を保持し、除外・generated sourceも含む。moduleConfigDigestsは元のgo.mod、使用時のgo.work／go.work.sum、alternate.mod／alternate.sum snapshotのSHA-256をrole別に持つ。保存対象はreportの再表示・再解析に使う上記のsourceと設定である。実行の再現には外部依存source、test data、その他の環境変数を別途用意する必要がある。

sourcesはReport用の元sourceをpackage pathとmodule-relative pathで識別し、byte列をbase64で保存する。source hashはsourceDigestsと一致する。Report内の全fileに、記録済みhashと一致する元sourceを要求する。保存はpermission 0600の一時fileとrenameで公開し、公開前の失敗では既存保存fileを保持する。sha256はresultをJSON decode（整数の精度を保持）し、Go encoding/jsonでobject keyをsortしてcompact marshalしたbyte列に対して計算する。これは破損検出用であり、署名ではない。

`gomcdc report [--format=text|json|html] [--output=<path>] <saved-result>` は同じrendererで保存結果を出力する。defaultはtext／stdout、HTMLにはdirectory指定が必要である。描画に使う結果と元sourceはすべて保存fileから読み、test／strict／threshold／partial等の結果と測定時toolVersionを保持する。通常のReport JSON、未対応version、checksum不一致、必須field欠落、source欠落・hash不一致は拒否する。入力保存fileと出力file（HTMLではindex.html）は、symlink／hardlink経由も含め、別々のfileを指定する必要がある。testのsaveとoutputも別々のfileを要求する。

reportの終了codeは出力成功0、不正usage4、読出し・検証・出力失敗2とする。保存されたtestやpolicyの成否は元の判定を保持する。signalによる終了codeはD28に従う。reportは再表示のみを行う。統合と再解析はD34の明示的な操作とする。

### D34. オフライン統合とMC/DC再解析

`gomcdc merge --output <saved-file> [limits] INPUT INPUT...`は2入力以上、`gomcdc reanalyze --output <saved-file> [limits] INPUT`は1入力を受ける。Maskingを選択済みならD19の3つの正の`--mcdc-masking-max-*`を指定できる。mergeの未指定上限は既定値、reanalyzeは入力の実効上限を使う。保存済みの指標選択を引き継ぎ、記録済みevidenceを使ってオフラインで処理する。入力fileは元の内容を保持する。出力は全入力と異なるfileとし、symlink／hardlinkによる同一指定も拒否する。

統合にはmodule・計測時toolVersion、source／module設定hash、解決済みpackage集合、working directory、指標、include-tests、exclude、保存済みGo環境、measurement mode、非runtimeのGo flagsの一致を要求する。package patternの表記、timeout、strict／閾値、テスト選択・実行設定のflags（`run`、`skip`、`count`、`shuffle`、`parallel`、`cpu`、`timeout`、`failfast`、`short`、`bench`、`benchtime`、`benchmem`、`v`、`json`、`list`、`fullpath`と登録済みtest alias）は異なってよい。未知flagsとbinary引数は一致が必要である。互換性検査の対象は保存済み設定であり、保存対象外の依存source・test data・任意環境変数はこの検査の対象外とする。同じinventoryに対するテスト分割を扱い、異なるinventoryは互換性エラーとする。静的entityやcapabilityの欠落・相違は元の分母を保持してエラーにする。

派生saved-result 1.1の`inputs`は元のsettingsとReport 2.0を保持し、source bytesは共通に1組保存する。input IDは`settings`と`report`からなるobjectのcanonical JSONのSHA-256とする。派生結果の再統合では元inputに展開し、同じIDは1回だけ、ID順に処理する。invocation IDまたは観測されたruntime run IDを共有する異なるsnapshotは競合として拒否する。旧1.0はcanonical内容でsnapshotを識別する。同じ内容のsnapshotは重複として扱い、独立したC0-only実行も内容が同じなら一つにまとめる。新規保存ではinvocation IDで独立実行を区別する。

集計は採用済みのentity別evidenceから行う。statement／function／clauseのobligationは1回だけ数え、BodyとSelectionは独立に統合する。counter加算のoverflowは拒否する。診断用のraw hitはproducerが決定した採否を保持する。DecisionのBoolean構造はinstrumenterと共通のoccurrence分解から復元し、保存したcondition順序・式の一致と評価vectorの妥当性を検査する。選択済みの両MC/DCは統合vectorを再解析し、実行を跨ぐwitnessにもrun／package／process／test／evaluationの出所を保持する。棄却vectorは診断用に元inputへ保持し、被覆には採用済みvectorを使う。ある実行のunknownは別の実行の採用済み観測で解消できるが、元の失敗・integrity結果は残す。

オフラインReport [schema 2.1](../schema/report-v2.1.schema.json)は`contributingRuns`に元input ID、実行・policy結果、measurement、producer結果、errorを持つ。複数の異なるinputでは`measurementMode=merged`、`measurements`には各実計測を列挙する。1 inputの再解析では元modeを保持する。採否は各実行に属するためtop-levelの`producerOutcomes`は空配列とする。test／measurement／integrityの失敗は全体にも保持し、`complete`は全inputがcompleteの場合のみtrue。top-levelのstrict／閾値は`not-requested`とし、各元実行に保存時のpolicy判定と失敗を表示する。top-levelのtoolVersionはオフライン処理を行ったtoolのversion、計測時versionは元inputに保持する。summaryと新witnessは指定上限で再計算する。text／HTMLも各元実行の結果を表示する。

両コマンドはatomicに保存する。保存成功は0（元test／policy失敗やMC/DCの未完了が残っても0）、usage不正4、入力・互換性・解析・出力失敗2。cancelは130で、公開済みの結果を保持する。公開前の失敗では元入力と既存出力を保持する。

## 9. 適合条件

実装は次の全条件を満たすとき本仕様へ適合する。

1. D1–D32 の型、集合、関数、外部形式を実装する。
2. 11指標を同一 source model と同一 report に統合する。
3. not-evaluated をfalseと区別した固有のcondition stateとして記録する。
4. abortedはevaluation statusとして保持し、coverage evidenceにはcompleted evaluationを使う。
5. Clause BodyとClause Selectionを別々に計測し、direct selectionとfallthrough body executionを独立したeventとして保持する。
6. Decision Coverageの名称と集計にはD14のDecision outcome定義を使う。
7. Decision集合はD6のif、条件付きfor、conditionless-switchのcase expressionで構成する。
8. unsupportedとunknownを、それぞれ独立したevidenceの状態として保持する。
9. 公開する指標と閾値flagはD27の正式名、summary keyはD29の定義に一致させる。
10. 正式LocationはすべてD1の計装前source座標で表す。
11. IDとreport順序は、package load順、map iteration順、goroutine順、解析並列度にかかわらず同一となる。
12. test cacheが存在する場合も、選択された各packageのtest binaryをsessionごとに正確に一度実行する。
13. 各test processは自身に固有のevidence fileへ書き込む。
14. compiler-aware backendは、選択されたGo compilerの互換性検査に基づいて対応状況を報告する。
15. 完成条件にはC0統合、両MC/DC、複数package、source mapping、並行評価、Clause Selectionをすべて含める。
16. productionはD22の一実行modelを使い、v1 dual-run実装はtest oracle専用codeとして保持する。
17. covered判定には検証済みcorrespondenceを要求し、unknown、partial、ambiguousなcorrespondenceはそれぞれのevidence状態を保持する。
18. 各producerの受理済みevidenceを、他のproducerの結果と独立して保持する。

## 10. 受け入れ検証

受け入れ suite は AND、OR、NOT、nested expression、4条件以上の固定seed semantic oracle、side effect、evaluation order、conditionless switch、expression switch、type switch、select、empty body、fallthrough、direct selection、no-match、再帰、nested decision、loop、複数 goroutine、panic、recover、defer、`runtime.Goexit`、複数 package、external test package、build tag、test/build failure、timeout、caller interruption、truncated event、partial recovery、provenance mutation、source mapping、ユーザー `//line`、生成code除外、JSON Schema contract mutation、全11閾値を含む。決定的all-metric fixtureは各package test binaryを一度だけ実行しつつ、v1のC0＋AST dual-run oracleのsemantic projectionとの一致を要求する。`a && b` は Unique-Causeで `a=infeasible`、`b=covered`、Maskingで両condition coveredとなる。

`go test -count=1 ./...`、`go test -race -count=1 ./...`、`go vet ./...` が成功し、fixture module の module 集計と package 集計の整数和が一致することを完成条件とする。

repository CI はGo 1.26.xと1.27.xで受け入れsuiteを実行する。最新の安定版Go/Linuxでgomcdc自身を計測し、`.github/self-mcdc-baseline.json` のmoduleおよびcritical package別floorを検証する。このbaselineは検証済みtest suiteの達成coverageを維持する保守契約であり、coverage適合性は上記の意味論の要件で判定する。floorを下げる変更は、失われるobligationと理由を明示したreviewを必須とする。

## 11. 参考資料

FAA CAST-10、NASA MC/DC tutorial、Hayhurst et al.、Chilenski、Go language specification、Go `cmd/cover` を参照する。D1–D32の定義を参考資料より優先する。
