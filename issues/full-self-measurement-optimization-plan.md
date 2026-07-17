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

## 完了条件

- productionの`runCoverage`がcoverage hierarchyを一回だけbuildする。
- focused heavy report benchmarkで時間とallocationが減り、JSON結果が一致する。
- 独立GOROOTでshadow viewを作らず、downloaded toolchain判定を境界testで固定する。
- JSON/HTML summary、witness、diagnostic、run results、errors、exit codeが変わらない。
- focused `internal/cli` AST commandを同一coverage選択で変更前後比較する。
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
HTML self measurementを完了した。測定値と制限は
`issues/runtime-journal-io-optimization-results.md`の「第2段階」へ保存した。残るcritical path
は測定対象のintegration testが待つnested Go subprocessであり、test削減やdual-run統合を
この計画の追加修正として行わない。
