# gomcdc 規範仕様

本書は `gomcdc` の唯一の規範仕様である。

以前の要件定義、追加要件、緊急訂正は、本書へ統合した時点で規範性を失う。

本書の日本語版を規範版とし、英語版は等価な翻訳とする。

仕様バージョンは `1.0-draft` である。

`1.0` 公開前のJSON schemaとCLIには互換性保証を設けない。

## 1. 目的

`gomcdc` は、Go moduleのテスト実行から次の指標を計測し、一つのレポートへ統合する。

- Statement Coverage
- Function Coverage
- Decision Coverage
- Switch Clause Body Coverage
- Type Switch Clause Body Coverage
- Select Clause Body Coverage
- Switch Clause Selection Coverage
- Type Switch Clause Selection Coverage
- Condition Coverage
- Unique-Cause MC/DC
- Masking MC/DC
- 指標ごとのInstrumentation Coverage

取得不能な事実を推測してcoveredまたはnot-coveredとして報告してはならない。

## 2. 対象環境

moduleの言語バージョンはGo 1.26とする。

開発とCIはGo 1.26系列の最新セキュリティ修正版を使用する。

Go Modules、Linux、macOS、GitHub Actionsを対象とする。

compiler-aware backendは対応するGo compiler versionを明示し、未対応versionを推測で処理せずunsupported-by-backendとする。

Windows、assembly、cgo内部、compiler IR、path coverage、distributed test executionはバージョン1の対象外とする。

## 3. 用語

**Condition** は、`&&`、`||`、`!`を含まない、bool型の原子的ソース式である。

比較、関数呼出し、field selection、map lookupのbool結果は、それ以上論理分解しない。

**Condition occurrence** は、ソース上の一つのcondition出現である。

同じテキストが複数回現れても、各出現へ別indexを割り当てる。

**Decision** は、制御フローを選択するbool式全体である。

**Clause body** は、switch、type switch、selectのcaseまたはdefaultに属するstatement列である。

**Evaluation trace** は、一回のdecision評価で観測したcondition state列とdecision resultである。

condition stateは`true`、`false`、`not-evaluated`のいずれかである。

## 4. 対象ソース

Go package patternから`go list`で得た、main module内のpackageだけを対象にする。

標準library、外部module、vendor、本ツールが生成したソースは対象外とする。

Go標準形式の生成コードコメントを持つユーザーソースは、デフォルトで対象外とする。

`_test.go`は、`--include-tests`を指定した場合だけAST系指標の分母へ含める。

Go標準coverがtest sourceをstatement分母へ含めないため、`--include-tests`はStatement CoverageとFunction Coverageへ影響しない。

解析と`go test`は、同じGOOS、GOARCH、build tags、CGO設定、module設定を使用しなければならない。

## 5. Statement Coverage

Statement Coverageは、元ソースへ実行したGo標準cover profileから取得する。

計測単位はGo cover profileのblockが持つstatement countである。

blockのcounterが0より大きい場合、そのblockの全statement unitをcoveredとする。

分子はcoveredなstatement unit数である。

分母は対象blockのstatement count合計である。

詳細レポートでは、個別AST statementを捏造せず、元ソース範囲、statement count、counterを持つstatement regionとして表示する。

## 6. Function Coverage

Function Coverageの対象は、関数、method、function literalである。

対象function内のstatement unitが一つ以上coveredなら、そのfunctionをcoveredとする。

statement unitを持たないfunctionは分母から除外する。

複数の`init` functionとfunction literalは、元ソース位置で区別する。

## 7. Decision Coverage

バージョン1のdecisionは次に限定する。

- `if`のcondition
- conditionを持つ`for`のcondition
- conditionless switchの各case expression

assignment、return value、call argumentだけに現れるbool式はdecisionではない。

conditionless switchの一つのcase clauseに複数expressionがある場合、各expressionを独立decisionとする。

先行expressionがtrueとなった後のcase expressionはfalseではなく`not-evaluated`とする。

Decision CoverageをCFG Edge Coverageと呼んではならない。

`goto`、labeled break、labeled continue、return、panic、recover、fallthroughのedgeはDecision Coverageへ含めない。

一つのdecisionにつき、true outcomeとfalse outcomeの二つをcoverage obligationとする。

分子は観測済みoutcome数である。

分母は二である。

completed evaluationだけを証拠として使用する。

## 8. Condition式木

decisionを次の式木へ正規化する。

```go
type DecisionExpr interface { isDecisionExpr() }
type AtomExpr struct { ConditionIndex uint16 }
type NotExpr struct { Operand DecisionExpr }
type AndExpr struct { Left, Right DecisionExpr }
type OrExpr struct { Left, Right DecisionExpr }
```

parenthesesは木構造だけに影響し、conditionを追加しない。

`!a`のconditionは`a`であり、式木は`NotExpr(AtomExpr(a))`である。

`a && (b || c)`のcondition indexは、左から右のソース出現順に`a=0`、`b=1`、`c=2`とする。

condition indexは、ファイル解析順、map iteration、並列処理へ依存してはならない。

## 9. Condition Coverage

一つのcondition occurrenceにつき、trueとfalseの二つをcoverage obligationとする。

短絡により評価されなかったconditionは`not-evaluated`として記録する。

`not-evaluated`をfalseとして扱ってはならない。

`not-evaluated`はCondition Coverageの分母を増やさない。

completed evaluationで実際に観測したtrueとfalseだけを証拠として使用する。

## 10. Evaluation lifecycle

一回の動的decision評価ごとにEvaluationIDを発行する。

EvaluationIDは一つのtest process内で一意でなければならない。

module全体で使用するidentityは次とする。

```go
type EvaluationIdentity struct {
    RunID        string
    PackagePath  string
    ProcessID    int
    EvaluationID uint64
}
```

runtime APIは、Begin、condition記録、End、Abortを明示的に表現する。

Endへ到達した評価だけをcompletedとする。

panic、`runtime.Goexit`、process interruptionなどでEndへ到達しなかった評価はabortedとする。

aborted evaluationはDecision、Condition、MC/DCの証拠に使用しない。

再帰、loop反復、nested decision、複数goroutine、複数processのevaluationを混同してはならない。

## 11. MC/DC共通規則

本ツールのMC/DC percentageは、condition occurrenceごとの独立影響obligation達成率である。

完全なMC/DC主張には、対応するDecision CoverageとCondition Coverageの達成も必要である。

本ツールはentryとexitの完全な呼出しをMC/DC percentageへ含めず、DO-178C適合、tool qualification、安全認証を主張しない。

### 11.1 Unique-Cause MC/DC

対象condition `i`について、二つのcompleted evaluation `p`と`q`が次を全て満たす場合にcoveredとする。

1. `p[i]`と`q[i]`は実際に評価されている。
2. `p[i] != q[i]`である。
3. `p.result != q.result`である。
4. 全ての`j != i`について`p[j] == q[j]`である。
5. `not-evaluated`はtrueまたはfalseと等しくない。

条件4はcondition stateの一致であり、推測した入力値の一致ではない。

短絡により条件4を満たすpairが構成不能な場合は、式木だけでUnique-Cause witness pairの不存在を証明できるとき`infeasible`とする。

`infeasible`はprogram path全般の到達不能を意味しない。

証明できない場合は`not-covered`とする。

`a && b`に対して次の三traceだけがある場合、Unique-Causeは`a=infeasible`、`b=covered`となる。

```text
[false, not-evaluated] -> false
[true,  true]          -> true
[true,  false]         -> false
```

## 12. Masking MC/DC

Masking MC/DCはcondition occurrenceごとに解析する。

解析はDecisionExprとcompleted evaluationだけを入力にするpure functionでなければならない。

全conditionへtrueまたはfalseを割り当てたvectorをcomplete assignmentと呼ぶ。

観測trace `p`のcompletionは、評価済みconditionの値と一致し、式木をGoの左から右の短絡規則で評価した結果が`p.result`となり、同じconditionがnot-evaluatedとなるcomplete assignmentである。

complete assignment `x`でcondition `j`がmaskedであるとは、`j`だけを反転したassignment `flip(x,j)`について次が成立することをいう。

```text
eval(tree, x) == eval(tree, flip(x, j))
```

対象condition `i`について、completed evaluation `p`と`q`およびそのcompletion `x`と`y`が次を全て満たす場合にcoveredとする。

1. `p[i]`と`q[i]`は実際に評価されている。
2. `x[i] != y[i]`である。
3. `eval(tree, x) != eval(tree, y)`である。
4. 全ての`j != i`について、`x[j] == y[j]`であるか、`j`が`x`と`y`の両方でmaskedである。
5. `x`と`y`はそれぞれ`p`と`q`の有効なcompletionである。

成立したwitnessには、観測trace、completion、target condition、masked conditionを保存する。

同じsource expressionが複数condition occurrenceとして現れる場合、値の同一性を仮定してはならない。

未観測値の補完が、複数occurrence間の意味的couplingを仮定しなければ成立しない場合は`analysis-incomplete`とする。

実装がresource limitにより正確な探索を完了できない場合も`analysis-incomplete`とし、推測scoreを返してはならない。

上記`a && b`の三traceでは、Masking MC/DCは`a=covered`、`b=covered`となる。

## 13. Clause Body Coverage

AST backendはcase selectionではなくbody entryを計測する。

次の三指標を独立に集計する。

- Switch Clause Body Coverage
- Type Switch Clause Body Coverage
- Select Clause Body Coverage

一つのcaseまたはdefault bodyにつき、一つのcoverage obligationを持つ。

実行がbodyへ入った場合にcoveredとする。

元のbodyが空でも、明示されたcaseまたはdefaultは分母へ含める。

fallthroughで到達したbodyもbody coverageではcoveredとする。

body executionをdirect case selectionと呼んではならない。

conditionless switchのcase expressionはDecision Coverageの対象でもあり、そのcase bodyはSwitch Clause Body Coverageの対象でもある。

### 13.1 Clause Selection Coverage

compiler-aware backendは、Switch Clause Selection CoverageとType Switch Clause Selection Coverageを計測する。

各明示case、default、defaultがない場合のno-matchを一つのselection obligationとする。

直接選択されたclauseだけをcoveredとする。

fallthroughでbodyへ到達したclauseを直接選択coveredとしてはならない。

`case A, B`はバージョン1では一つのclause selection obligationとする。

compiler eventは、matched case expressionまたはmatched type alternativeを区別できる情報を保持する。

selection eventは`SwitchID`と直接選択した`ClauseID`を持ち、case alternativeが存在する場合だけmatched alternative indexを持つ。

no-match eventは`SwitchID`とno-match roleを持ち、存在しないClauseIDを捏造しない。

fallthrough eventはsourceとdestinationのClauseIDを持つ。

matched alternativeはバージョン1のcoverage分母へ含めないが、取得した証拠をreportから捨ててはならない。

fallthrough edgeはselectionとbody entryのどちらにも混ぜず、独立eventとして保持する。

Switch Clause Selection CoverageとType Switch Clause Selection Coverageはバージョン1の完成条件に含める。

汎用的な`Clause Coverage`集約値は定義しない。

## 14. Instrumentation Coverage

coverage scoreと計装対応率を分離する。

各指標について次を出力する。

```text
discovered
supported
instrumented
unsupported
unknown
```

`discovered`は、対象sourceから静的に識別したentity数である。

`supported`は、選択backendがそのentityへ正確な証拠を生成できる数である。

`instrumented`は、必要なprobeまたはcompiler eventを実際に生成した数である。

`unsupported`は、backendの能力外であると確定した数である。

`unknown`は、解析または整合性検証を完了できず能力判定も確定できない数である。

単位は各指標のcoverage obligationを持つsource entityとする。

Statementはstatement unit数、Functionはfunction数、Decisionはdecision数、Conditionと各MC/DCはcondition occurrence数、Clause Bodyはclause body数、Clause Selectionはselection obligation数を単位とする。

Instrumentation Coverageの分母はdiscovered、分子はinstrumentedとする。

support statusは`supported`、`unsupported-by-backend`、`unknown`のいずれかとする。

coverage statusは`covered`、`not-covered`、`infeasible`、`analysis-incomplete`のいずれかとする。

`aborted`はevaluation statusであり、coverage entity statusへ変換しない。

unsupported-by-backendとunknownはcoverage percentageの分母へ含めない。

coverage percentageへunsupportedやunknownを混ぜる設定は提供しない。

`--strict`は、要求指標にunsupported-by-backend、unknown、analysis-incomplete、未計装entityが一つでもあれば失敗する。

## 15. 計装による意味保存

計装後も次を保存する。

- Go仕様上の評価順序
- `&&`と`||`の短絡
- 式と関数呼出しの評価回数
- panicの発生条件
- deferの順序
- 副作用の順序
- return valueとerror
- ユーザーコードから観測可能な状態変更

実行時間、allocation数、stack trace、inlining、goroutine scheduling、CPU使用量、memory使用量の一致は保証しない。

計測runtimeはユーザー式の値を一度だけ受け取り、同じ値を返す。

計測runtime自身の失敗をユーザーコードへpanicとして伝播させてはならない。

計測失敗はdiagnosticとintegrity statusへ反映する。

## 16. Source locationとID

ユーザー向けの正式位置は、main module rootからの物理相対pathと物理的な一始まりの行・列とする。

ユーザー記述の`//line`による論理位置は、追加情報として保持してよいが正式位置を置換しない。

一時workspace、generated bridge、runtime packageの位置をユーザー向け位置として表示してはならない。

DecisionIDとClauseIDは、同じsource revisionに対して決定論的でなければならない。

IDはmodule path、package import path、相対file path、node kind、物理start offset、物理end offset、condition indexから生成してよい。

IDはload順、解析順、goroutine順、map iteration順へ依存してはならない。

異なるsource revision間のID安定性は保証しない。

## 17. Evidence authorityとMeasurement mode

standard-cover backendだけがStatement CoverageとFunction Coverageの実行証拠を所有する。

AST backendだけがDecision、Condition、両MC/DC、三種類のClause Body Coverageの実行証拠を所有する。

compiler-aware backendだけがSwitch Clause SelectionとType Switch Clause Selectionの実行証拠を所有する。

backendは、別backendの証拠を推測または派生させて正式証拠へ昇格してはならない。

### 17.1 Measurement mode

StatementまたはFunctionだけを選択した場合は`standard-cover`とする。

standard-cover以外の指標だけを選択した場合は`single-run`とする。

両方を選択した場合は`dual-run-standard-cover`とする。

dual-runでは、元ソースのstandard cover runと、計装済みsourceのinstrumented runを一回ずつ実行する。

instrumented runはAST backendとcompiler-aware backendを同じbuildとtest executionへ適用する。

Clause Selection指標を選択しても第三のtest runを追加してはならない。

指標ごとにtestを再実行してはならない。

選択指標が内部的に別指標のmetadataまたはruntime evidenceを必要とする場合、その依存情報は収集してよい。

依存情報の収集だけを理由に、未選択指標をreportへ追加してはならない。

二つのrunは別のtest executionであり、evaluationを相互に対応付けてはならない。

各runのstatus、failure kind、package statusを独立してレポートする。

一方が失敗しても、もう一方と失敗runから取得済みのデータを可能な範囲で回収する。

## 18. go test引数

package patternは一つ以上必須とする。

`--`以降のGo build/test引数は、解析と全measurement runへ同じ意味で適用する。

本ツールは`-count=1`を常に設定する。

ユーザーまたはGOFLAGSが別の`-count`を指定した場合はCLI errorとする。

`-cover`、`-covermode`、`-coverpkg`、`-coverprofile`、`-json`、`-overlay`は本ツールが所有するため、ユーザー指定をCLI errorとする。

`-run`、`-skip`、`-tags`、`-race`、`-shuffle`、`-parallel`、`-timeout`など、計測方式と衝突しない引数は保持する。

activeな複数main moduleを持つgo.workは対象外とし、推測で一つを選ばない。

## 19. Runtime data

test processごとに独立したevent fileへ書き込む。

file identityにはpackage import pathの衝突不能なencoding、PID、RunID、nonceを含める。

package pathをそのままfile path要素として連結してはならない。

truncated tail、panic、timeout、異常終了があっても、完全に検証できたrecordを回収する。

DecisionID、EvaluationIdentity、condition count、condition state、result、statusの整合性を検証する。

式木の短絡規則と矛盾するcompleted vectorはcoverage evidenceから除外し、integrity errorとする。

同じdecision、vector、result、statusの重複は集約してよい。

witnessに必要な代表vectorとoccurrence countは失ってはならない。

TestIDはbest-effort情報であり、取得できない場合は`unknown`とする。

TestIDをMC/DC成立条件へ使用しない。

## 20. CLI

基本実行は次とする。

```sh
gocoverage test ./...
```

`--coverage`は次の正式名だけを受け付ける。

```text
statement
function
decision
switch-clause-body
type-switch-clause-body
select-clause-body
switch-clause-selection
type-switch-clause-selection
condition
mcdc-unique
mcdc-masking
all
```

`c0`、`c1`、`c2`、`mcdc`などの別名は提供しない。

主要optionは次とする。

```text
--format text|json
--output <path>
--exclude <module-relative-glob>
--include-tests
--keep-workdir
--workdir <parent>
--timeout <duration>
--strict
```

`--exclude`は複数指定でき、空または解析不能なglobをerrorとする。

`--output`未指定時はstdoutへ出力する。

`--keep-workdir`未指定時は一時workspaceを終了時に削除する。

デフォルトは`all`とする。

`all`は、`all`以外の十一の正式指標を全て選択する。

閾値optionは次とする。

```text
--fail-under-statement
--fail-under-function
--fail-under-decision
--fail-under-switch-clause-body
--fail-under-type-switch-clause-body
--fail-under-select-clause-body
--fail-under-switch-clause-selection
--fail-under-type-switch-clause-selection
--fail-under-condition
--fail-under-mcdc-unique
--fail-under-mcdc-masking
```

閾値を指定した指標が`--coverage`に含まれない場合はCLI errorとする。

汎用的な`--fail-under-clause`は提供しない。

## 21. Coverage ratioと閾値

coverage ratioは、covered obligation数を、coveredとnot-coveredのobligation合計で割った値とする。

unsupported-by-backend、unknown、infeasible、analysis-incompleteはcoverage ratioの分母へ含めない。

分母が0の場合、percentageは数学的に未定義であるため、textでは`n/a`、JSONでは`null`とする。

分母0の指標へ閾値を指定した場合、その閾値は未達とする。

閾値比較は丸め前の整数比で行う。

表示percentageは小数点以下二桁へ丸める。

## 22. 終了状態

各measurement runは、passed、test-failed、build-failed、timeout、command-failedを区別する。

overall resultは全run、integrity、strict、thresholdの結果を保持する。

process exit codeは次とする。

```text
0 success
1 one or more go test runs failed
2 measurement, instrumentation, integrity, or report failure
3 coverage threshold failure
4 invalid CLI usage
```

複数条件が重なる場合の優先順位は`4 > 2 > 1 > 3 > 0`とする。

test failureまたはmeasurement failureがあっても、生成可能なreportは出力する。

## 23. JSON report

reportはmodule、package、file、function、decision、condition、clause body、clause selectionの階層で集計する。

上位集計の分子と分母は、直下entityのpercentage平均ではなくcoverage obligation数の合計から計算する。

JSON rootの`version`はdraft中`1.0-draft`とし、1.0公開時に`1`へ固定する。

JSON rootは最低限、次を持つ。

```text
version
module
run
measurementMode
measurements
instrumentationCoverage
summary
packages
errors
```

summary keyはCLIの正式指標名をlower camel caseへ変換したものとする。

```text
statement
function
decision
switchClauseBody
typeSwitchClauseBody
selectClauseBody
switchClauseSelection
typeSwitchClauseSelection
condition
mcdcUnique
mcdcMasking
```

汎用的な`clause`または`clauseBody` keyは出力しない。

IDはJSON numberではなく16進文字列として出力する。

enum値はschemaで列挙し、未知値を既知値へ変換しない。

arrayの順序はpackage path、物理source location、IDの順で決定論的にする。

decision reportには式、位置、outcome、condition、MC/DC結果、評価vector、witnessを含める。

各errorは少なくとも`phase`、`code`、`message`を持ち、該当する場合だけ`package`とmodule-relative `path`を持つ。

## 24. Text report

全ての有効指標について分子、分母、percentageまたは`n/a`を表示する。

Instrumentation Coverageとunsupported、unknown、analysis-incomplete件数をcoverage scoreの近くへ表示する。

MC/DC witnessには観測vector、decision result、Maskingで使用したcompletionを表示する。

Unique-CauseとMaskingの結果が異なる場合は、満たさなかった規則を表示する。

## 25. Performanceとresource境界

一つのmeasurement runで、decision数または指標数に比例してtestまたはbuildを繰り返してはならない。

instrumented runは一回のsource計装、compiler計装、build、testで全非standard-cover指標の証拠を収集する。

同一evaluation evidenceはwitnessを失わない範囲で集約する。

event journalは定期的にcompactionし、loop回数に比例してmemoryへ全recordを保持してはならない。

性能目標は通常の`go test`に対してAST-onlyで2倍以内、両MC/DCを含む場合で5倍以内とするが、test内容に依存するため適合保証には使用しない。

## 26. Securityと信頼境界

本ツールは対象moduleのbuildとtestを現在のユーザー権限で実行する。

対象moduleは信頼済みでなければならない。

一時workspaceはsecurity sandboxではない。

対象testはevent fileや環境変数へアクセスできるため、悪意ある対象コードに対するcoverage証拠の真正性を保証しない。

一時directoryとevent fileは現在ユーザーだけが読み書きできるpermissionで作成する。

sourceの複製時はsymlinkとhardlinkによるworkspace外書込みを防止する。

reportにはmodule-relative path、source expression、test outputが含まれ得るため、project dataとして扱う。

ユーザーの絶対local pathを通常reportへ出力してはならない。

## 27. 受け入れ条件

最低限、次のintegration fixtureを持つ。

- AND、OR、NOT、nested expression
- `a && b`のUnique-Causeで`a=infeasible`、`b=covered`、Maskingで両方covered
- side effectとevaluation order
- panic、recover、defer、`runtime.Goexit`
- recursion、loop、nested decision
- goroutineとparallel package execution
- conditionless switchの複数case expression
- expression switch、type switch、select、empty body、direct selection、no-match、fallthrough
- 複数package、external test package、build tag
- test failure、build failure、timeout、truncated event file
- source mapping、ユーザー`//line`、生成コード除外
- dual-runと部分回収
- 全閾値と終了コード優先順位

次が成功しなければならない。

```sh
go test -count=1 ./...
go test -count=1 -race ./...
go vet ./...
```

## 28. 完成条件

一回の`gocoverage test ./...`で、選択された全指標、Instrumentation Coverage、source location、partial run statusを一つのreportへ出力できることを完成条件とする。

AST backendが取得不能なselection情報をcoveredまたはnot-coveredとして報告しないこと、およびcompiler-aware backendがClause Selection Coverageを正確に報告することを完成条件に含める。

全受け入れfixture、race test、`go vet`が成功しなければ完成としない。

## 29. 技術参照

本書の論理式とMC/DC用語はNASA/TM-2001-210876を参照する。

短絡言語におけるMasking MC/DCはNASAのshort-circuit MC/DC研究を参照する。

Go構文と評価規則はGo 1.26 Language Specificationを参照する。

Statement Coverageの形式はGo公式coverage documentationを参照する。

- https://ntrs.nasa.gov/archive/nasa/casi.ntrs.nasa.gov/20010057789.pdf
- https://ntrs.nasa.gov/api/citations/20150011052/downloads/20150011052.pdf
- https://go.dev/ref/spec
- https://go.dev/doc/build-cover
