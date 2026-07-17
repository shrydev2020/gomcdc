# Full self measurement修正計画

## 目的と境界

runtime event journal案A後も残る全11指標HTML self measurementの遅延を、対象test実行、
compiler-aware toolchain preparation、post-test report構築、HTML publishへ分離し、意味を
変えずに確定的な重複処理と不要なfilesystem mutationを除く。

runtime journalのcommit/compactionは`runtime-journal-io-optimization-plan.md`の責任であり、
この計画では変更しない。standard-coverとASTを一回へ統合せず、test集合、coverage対象、
MC/DC定義、witness、resource budget、JSON/HTML schema、exit codeを性能のために弱めない。

## Baseline

2026-07-17、Apple M2 Pro、darwin/arm64、Go 1.26.5の全11指標HTML self measurementで、
表示されたpackage時間は次のとおりだった。

| phase | critical package | journal修正前 | 案A後 | 変化 |
|---|---|---:|---:|---:|
| standard-cover | `internal/cli` | 35.086秒 | 33.510秒 | 4.5%短縮 |
| AST | `internal/cli` | 119.881秒 | 38.855秒 | 67.6%短縮 |
| AST | `internal/mcdc` | 100.374秒 | 13.853秒 | 86.2%短縮 |
| AST | `internal/runtimecov` | 21.360秒 | 26.546秒 | 24.3%増加 |

package testはphase内で並列実行されるため表示時間を合計しない。表示上のcritical pathは
最低でも約72秒残る。生成された`coverage-html/index.html`は12,340,929 bytesだったが、
package時間はcollection、validation、report build、source projection、HTML publishを
含むcommand全体時間ではない。

## 責任分離

- `measure`はworkspace、instrumentation、go test、evidence collection/verificationを所有する。
- `compileraware.Prepare`はselected Go toolchainに対応するcompiler producerを作る。installed
  GOROOTを変更してはならない。
- `report.Build`は検証済みinputからcoverage hierarchy、witness、summaryを構築する。
- `runCoverage`は構築済みsummaryからstrict/threshold、run results、errors、exit codeを決める。
- `WithSourceViews`とHTML renderはcoverageやwitnessを再解析しない。
- 対象testの時間とpost-test処理を一つの「CLIが遅い」bucketへまとめない。

## FSM-001: post-test処理をfocused計測する

full self measurementを反復する前に、同じheavy `report.Input`で次を比較する。

- coverage hierarchyを二回buildする現行policy反映
- hierarchyを一回buildし、run results/errorsだけ更新する処理
- source projectionとHTML render/publish

`ns/op`、`B/op`、`allocs/op`、出力bytesを記録する。Maskingの複雑なconditionを含め、CPU
profileで`report.Build`、MC/DC、source projection、template renderを分離する。HTML fileの
最終sizeや`Sync`だけからphysical SSD writeを推測しない。

## FSM-002: coverage hierarchyを一回だけ構築する

最初の`report.Build`で得たsummaryからstrict/thresholdを判定し、coverage treeを再構築せず
`Run.Results`と`Errors`だけを正規化・copyする。責任を隠す`Finalize`、`Manager`、`Service`
は導入せず、変更fieldを名前で限定する。

必須test:

- 旧二回build相当と更新後reportがJSON上同一になる。
- strict/thresholdの未要求、pass、failを既存integration testで確認する。
- summary、hierarchy、MC/DC witness、instrumentation coverageが変わらない。
- callerのerrors sliceを後から変更してもreportが変わらない。
- HTML source projectionは構築済みreportへ一度だけ付与する。

## FSM-003: `internal/cli` AST critical pathをprofileする

全AST指標のisolated `internal/cli` profileで、nested Go subprocess、compiler preparation、
runtime writer、test logicを分離する。testやpackageを黙って除外しない。

変更前profileではtest 25.967秒、command real 31.85秒だった。CPU sample 6.67秒中6.38秒が
filesystem syscallで、`compileraware.Prepare`のGOROOT view作成が3.46秒、workspace cleanup
が2.83秒を占めた。約15,000 entryの作成・削除が主要因だった。

## FSM-004: shadow GOROOTを必要なtoolchainへ限定する

`cmd/go`がoverlay replacementを拒否するのはselected GOROOTがlexicalに`GOMODCACHE`配下の
downloaded toolchainである場合なので、その場合だけdisposable shadow GOROOTを作る。
Homebrew等の独立installではreal GOROOTをread-only overlay targetとして使う。

維持する条件:

- installed GOROOTのfileを作成、変更、削除しない。
- downloaded toolchainでは従来のshadow view互換経路を使う。
- stable Go version、compiler source anchor、patch失敗をfail-closedにする。
- compiler patch IDでGo build cacheを分離する。
- process-global/persistent cacheはcleanup、lock、invalidationのauthorityを新設するため導入しない。

## FSM-005: 選択されていないMC/DC解析を実行しない

`buildDecisionReport`は`CoverageSet`でUnique-Cause MC/DCとMasking MC/DCが無効でも、両方の
strategyを呼び出してから、結果の表示だけを`disabled`にしていた。MC/DCを要求しない
Decision/Condition計測でも、複雑なconditionのwitness探索とevaluation準備が走る。

指標選択のauthorityは`CoverageSet`に置いたまま、選択されたstrategyだけを呼び出す。
公開reportでは無効なMC/DCのfieldとcondition slotを従来どおり残し、`enabled=false`、
`status=disabled`、解析counter 0、witnessなしとする。MC/DCが一つでも選択されている場合は、
そのstrategyのcoverage意味、resource budget、witnessを変更しない。

focused benchmarkは8 conditions、24 decisions、全truth vectorを使い、全指標、Uniqueだけ、
Maskingだけ、MC/DCなしを別々に測る。既存の24〜64 conditionsのMasking重厚benchmarkも
回帰gateにする。これはMC/DCを含まない選択の最適化であり、全11指標self measurementの
61.99秒baselineが同じ変更で短縮されるとは主張しない。

## FSM-006: integration test待ちを証拠単位で確認する

`go test -json -count=1 ./internal/cli`でtest別wall timeを取得し、上位testが起動するnested
commandを調べる。HTML、全指標JSON、C0独立照合、failure/partial evidenceのように異なる
契約を検証するcommandは、同じfixtureを使うだけで重複とはみなさない。test共有cache、
test集合削減、standard-coverとASTの並列実行は、証拠の独立性、test副作用、I/O競合を変える
ため、この修正へ含めない。

## 完了条件

- productionの`runCoverage`がcoverage hierarchyを一回だけbuildする。
- focused heavy report benchmarkで時間とallocationが減り、JSON結果が一致する。
- 独立GOROOTでshadow viewを作らず、downloaded toolchain判定を境界testで固定する。
- JSON/HTML summary、witness、diagnostic、run results、errors、exit codeが変わらない。
- focused `internal/cli` AST commandを同一coverage選択で変更前後比較する。
- MC/DCを選択しないreport buildがstrategyを呼ばず、無効な公開fieldを保持する。
- Uniqueだけ、Maskingだけ、両方、どちらもなしの複雑条件benchmarkを保存する。
- 通常test、race、vet、self measurementが通る。
- full commandの改善率をpackage表示時間だけから算出しない。
- 残るnested subprocess待ちはreport/compiler修正の未達とせず、別の根因がある場合だけ分離する。

## Rollback条件

- coverage summary、witness、error ordering、run results、exit codeが変わる。
- downloaded toolchainでcompiler overlayがbuild不能になる。
- installed GOROOTを変更する。
- test集合やcoverage対象を性能のために減らす。
- workspace cleanup後にmeasurement-owned artifactを残す。

## 実施status

2026-07-17にFSM-001〜004を実装し、focused benchmark/profile、通常test、race、vet、全11指標
HTML self measurementを完了した。続いてFSM-005/006を追加し、integration test別wall timeと
複雑条件の指標選択benchmarkを取得した。FSM-005/006も実装、通常test、race、vetまで完了
した。FSM-001〜004の測定値と制限は
`issues/runtime-journal-io-optimization-results.md`の「第2段階」へ保存した。残るcritical path
は測定対象のintegration testが待つnested Go subprocessであり、test削減やdual-run統合を
この計画の追加修正として行わない。

## FSM-005/006 実施結果

変更前は8 conditions、24 decisions、各decisionの全256 truth vectorを持つinputで、
Decision/Conditionだけを選択しても両MC/DC strategyが走っていた。5回benchmarkの中央値は
次のようになった。

| report build | median ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| MC/DCなし・変更前 | 5,681,414 | 6,865,597 | 33,923 |
| 全指標・変更後 | 5,542,925 | 6,868,540前後 | 34,287 |
| Uniqueだけ・変更後 | 3,861,942 | 5,606,174前後 | 27,964 |
| Maskingだけ・変更後 | 4,031,824 | 5,922,900前後 | 32,318 |
| MC/DCなし・変更後 | 2,324,711 | 4,660,529 | 25,995 |

MC/DCなしはwall time 59.1%、allocation bytes 32.1%、allocation回数23.4%減となった。
全指標では従来どおり両strategyを実行し、片方だけなら選択されたstrategyだけを実行する。
disabled field、condition slot、JSON schemaは残し、解析していないcounterを0、witnessをnilに
固定するtestを追加した。

Maskingの既存重厚benchmarkも`-benchtime=3x -count=3`で通過した。32-condition全covered、
24-condition no-witness、48-conditionの既定search-state limitとevaluation-pair limit、
64-condition high-unobservedを含み、resource limit時の`analysis-incomplete`を維持した。

`internal/cli`のtest別wall time上位は、全指標JSONとC0独立照合を行うtestが5.10秒、全指標
HTMLが4.25秒、failure/interrupt evidenceが2.81秒、partial multi-package reportが2.45秒
だった。先頭2件だけでもnested `go test`は、AST/standardの独立run、HTML、全指標JSON、
C0照合という別の証拠を生成する。共有cacheやtest統合でこの時間を消すと、製品処理ではなく
test契約を弱めるため採用しなかった。

最終gateは`go test -count=1 ./...`（command 28.45秒）、`go test -race -count=1 ./...`、
`go vet ./...`がすべて成功した。MC/DCを含む全11指標経路の計算分岐は変わらないため、既に
61.99秒のbaselineを持つHTML self measurementをSSDへ再出力して改善率を装わなかった。
したがって今回の59.1%は「MC/DCを選択しない複雑条件report build」の改善率であり、全11
指標commandの追加高速化率ではない。
