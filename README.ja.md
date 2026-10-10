# gomcdc

[![Ask DeepWiki](https://deepwiki.com/badge.svg)](https://deepwiki.com/shrydev2020/gomcdc)

[English](README.md)

`gomcdc` はGoのtestを実行し、Statement、Function、Decision、Condition、
switch clause、Unique-Cause MC/DC、Masking MC/DCを一つのcoverage reportへ
まとめます。

Go標準coverageがstatementとfunctionの実行有無を測るのに対し、`gomcdc`は
boolean評価vectorも記録し、各conditionがdecision結果へ独立に影響したかを
示します。clause bodyの実行とswitch/type-switchの直接selectionを区別し、
test失敗や中断時も検証可能なpartial resultを保持します。

一つのmeasurement sessionでは、選択された各packageのtest binaryを一度だけ
実行します。Go cover、AST runtime計装、compiler-aware selection hookは同じ実行を
観測します。

## 動作要件

- Go 1.26.0以降
- Go Modulesを使用する計測対象
- LinuxまたはmacOS

## インストール

Go 1.26以降でv2 releaseをインストールします。

```sh
go install github.com/shrydev2020/gomcdc/v2@latest
```

`gomcdc version`でインストールしたbuildを確認できます。

## Quick start

計測対象moduleで`gomcdc`を実行します。

```sh
cd /path/to/your/module
gomcdc test ./...
```

defaultでは11指標をすべて有効にし、text reportを標準出力へ書きます。有効な
summaryはcovered数、分母、percentageに加え、対応状況と解析状態を件数付きで
表示します。

```text
Summary:
  Decision Coverage: enabled=true 22 / 30 = 73.33% unsupported=0 unknown=0 infeasible=0 analysis-incomplete=0
  Condition Coverage: enabled=true 34 / 46 = 73.91% unsupported=0 unknown=0 infeasible=0 analysis-incomplete=0
  Unique-Cause MC/DC: enabled=true 8 / 15 = 53.33% unsupported=0 unknown=0 infeasible=8 analysis-incomplete=0
  Masking MC/DC: enabled=true 14 / 23 = 60.87% unsupported=0 unknown=0 infeasible=0 analysis-incomplete=0
```

module summaryの後にpackage、file、function、decision、condition、MC/DC witness、
観測した評価vectorを展開します。
評価記録とMC/DCの証拠には、実行したtest／subtestの名前を表示します。
初期化などtest context外の実行は`unknown`と表示します。

## 指標

| CLI名 | 計測内容 |
| --- | --- |
| `statement` | 実行されたGo statement |
| `function` | bodyが実行されたfunction |
| `decision` | `if`、boolean `for`、conditionless-switch case decisionのtrue/false結果 |
| `switch-clause-body` | 実行されたexpression-switch clause body |
| `type-switch-clause-body` | 実行されたtype-switch clause body |
| `select-clause-body` | 実行された`select` clause body |
| `switch-clause-selection` | 直接選択されたexpression-switch clauseと一致alternative |
| `type-switch-clause-selection` | 直接選択されたtype-switch clauseと一致type alternative |
| `condition` | atomic boolean conditionのtrue/false結果 |
| `mcdc-unique` | target conditionだけが変化してdecision結果を変えるpairを持つcondition |
| `mcdc-masking` | maskされた値を考慮した上でtargetが独立にdecisionを決めるpairを持つcondition |

Clause BodyとClause Selectionは意図的に別指標です。先行clauseからの
fallthroughでbodyが実行される場合がある一方、selectionはdispatchが直接選んだ
clauseを示します。

## Report

defaultはtextです。JSONはrepository内のreport schemaに従います。HTMLは指定
directoryへself-contained reportを書きます。

schema 2.0は要求されたevidence producerごとのoutcomeを公開します。integrity、
実行completeness、source mapping、最終usability判断を分離します。compiler
selection streamが拒否された場合も、有効なASTやGo cover evidenceを保持します。

```sh
# Textを標準出力へ表示
gomcdc test ./...

# JSON file
gomcdc test --format json --output coverage.json ./...

# coverage-html/index.htmlへHTMLを出力
gomcdc test --format html --output coverage-html ./...
```

一度の計測から複数のreportを作るには、`--save`で結果を保存します。
`report`は保存ファイル内の結果と元ソースを使い、オフラインで表示します。

```sh
gomcdc test --save coverage.gomcdc.json --format json --output coverage.json ./...
gomcdc report --format html --output coverage-html coverage.gomcdc.json
gomcdc report coverage.gomcdc.json
```

保存ファイルには元ソース、source hash、Goのversion・build環境、計測指標、
除外設定、test引数を含みます。共有時にはソースと引数も渡ります。
`report`の入力には`--save`で作ったファイルを指定してください。
`--format json`は、他のツールで利用するための表示用reportを出力します。
失敗・partial・閾値判定の結果は保存時のまま表示し、`report`自身は出力成功時に0を返します。

同じソース・パッケージ・計測指標・Go/build条件でテストを分割実行した結果は、
`merge`で統合できます。各実行の観測を合わせてMC/DCを再判定するため、
別々の実行で得られた評価からも証拠ペアが成立します。

```sh
gomcdc test --save shard-a.gomcdc.json ./... -- -run '^TestA'
gomcdc test --save shard-b.gomcdc.json ./... -- -run '^TestB'
gomcdc merge --output merged.gomcdc.json shard-a.gomcdc.json shard-b.gomcdc.json
gomcdc reanalyze --output analyzed.gomcdc.json --mcdc-masking-max-search-states 8000000 merged.gomcdc.json
gomcdc report --format html --output coverage-html analyzed.gomcdc.json
```

`merge`と`reanalyze`も保存ファイルを使ってオフラインで処理し、新しい保存ファイルを出力します。
`reanalyze`は記録済みの観測を使い、選択済みのUnique-Cause／Masking MC/DCを再判定します。
探索上限には`test`と同じ3つの`--mcdc-masking-max-*`を指定できます。
`merge`は未指定の上限に既定値を使い、`reanalyze`は入力ファイルの上限を引き継ぎます。

同じ保存結果の重複指定や統合済みファイルの再統合でも、元の各実行を一度だけ集計します。
異なるパッケージ集合・ソース・ビルド設定の統合はエラーになります。
元の失敗・中断・strict／閾値判定を実行別に保持し、統合結果のstrict／閾値欄は
`not-requested`とします。オフライン処理の終了codeは保存処理の成否を表し、成功時は0です。

通常の未達と区別して次の状態を解釈してください。

| 状態 | 意味 |
| --- | --- |
| `not-covered` | 有効なevidenceはあるが必要なcoverage witnessが見つからない |
| `infeasible` | 選択したMC/DC strategyとGoの評価規則ではobligationを成立させられない |
| `unsupported-by-backend` | 有効なmeasurement backendがobligationを証明できない |
| `unknown` | evidence authorityまたはmeasurement completenessが不足して判定できない |
| `analysis-incomplete` | Masking MC/DCの有限な探索予算到達を含め、obligationの正確なanalysisが完了しなかった |

`unknown`、`unsupported-by-backend`、`infeasible`、`analysis-incomplete`は、
それぞれの状態と件数を保持します。test失敗、panic、timeout、runtime evidenceのtruncated
tailが発生しても、信頼できるevidenceが残ればpartial reportを生成します。

Masking MC/DCはexact searchします。組み込みのcondition obligationごとの上限は、
candidate evaluation pair 1,000,000件、search state 4,000,000件、主要solver backing
array 64 MiBです。solver byte上限には検証済みinput、result witness、goroutine stack、
その他のprocess memoryを含まず、process全体のheap、RSS、総memory上限ではありません。
いずれかを超える探索が必要な場合は`analysis-incomplete`とします。
3つの`--mcdc-masking-max-*` optionで、obligation単位の上限を正の値に変更できます。値を増やすとconditionとdecisionの数に応じて総処理量が増えます。
reportは実効値を`maskingAnalysisLimits`として記録します。

## 主なoption

```sh
# 指標を選択
gomcdc test --coverage=decision,condition,mcdc-unique,mcdc-masking ./...

# module-relative pathを除外。--excludeは繰り返し可能で**を使用可能
gomcdc test --exclude='internal/generated/**' ./...

# activeな_test.goを選択した全指標の対象へ追加
gomcdc test --include-tests ./...

# --以降をgo testへ渡す
gomcdc test ./... -- -run TestCritical
```

| Option | 用途 |
| --- | --- |
| `--coverage=<list>` | comma区切りで指標を選択。defaultは`all` |
| `--exclude=<glob>` | module-relative source globを除外。繰り返し可能 |
| `--include-tests` | activeな`_test.go`をStatement／Functionを含めた対象指標へ追加 |
| `--format=text\|json\|html` | report形式を選択 |
| `--output=<path>` | fileへ出力。HTMLの場合はdirectory |
| `--save=<path>` | 元ソースと計測条件を含む結果を保存し、後から`gomcdc report`で表示 |
| `--strict` | requested entityにunsupported、unknown、analysis-incomplete、未計装があれば失敗 |
| `--fail-under-<metric>=<percent>` | 有効な一指標がthreshold未満なら失敗 |
| `--mcdc-masking-max-evaluation-pairs=<count>` | Masking condition obligationごとのcandidate evaluation pair上限。default 1,000,000 |
| `--mcdc-masking-max-search-states=<count>` | Masking condition obligationごとの新規search state上限。default 4,000,000 |
| `--mcdc-masking-max-solver-bytes=<bytes>` | Masking condition obligationごとの主要solver backing array上限。default 67,108,864 bytes |
| `--timeout=<duration>` | `go test` subprocessのtimeout。defaultは10分 |
| `--keep-workdir` | 診断用に計装済みtemporary workspaceを保持 |
| `--workdir=<directory>` | temporary workspaceの親directoryを指定 |

完全なoption一覧は`gomcdc test --help`で確認できます。

## CI policy

`--strict`でmeasurementの完全性を要求し、`--fail-under-*`でcoverage policyを
適用します。thresholdは`--coverage`で選択した指標にだけ指定できます。

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

| 終了code | 意味 |
| ---: | --- |
| 0 | 成功 |
| 1 | `go test` runが失敗 |
| 2 | measurement、instrumentation、integrity、reportの失敗 |
| 3 | coverage threshold未達 |
| 4 | 不正なCLI usage |

## 対応範囲と制約

- package patternはmain module内のpackageへ解決される必要があります。標準
  library、外部module、`vendor`、Go標準generated-code commentを持つfileは
  除外します。
- activeな`go.work`はmain moduleが一つの場合に対応します。
- `_test.go`は`--include-tests`指定時だけ選択した指標へ含めます。テストと
  テスト用helperのStatement／Functionも、通常sourceと同じGo coverの
  カウンター配置に基づいて計測します。
- Windows、assembly内部、cgo内部、compiler IR obligation、path coverage、
  distributed test executionはv2対象外です。
- 対象moduleを現在ユーザーの権限でbuild/testします。temporary workspaceは
  悪意ある対象codeに対するsandboxではありません。

## Reference

- [規範仕様](docs/specification.ja.md)
- [英語参考仕様](docs/specification.md)
- [Measurement JSON schema](schema/report-v2.0.schema.json)
- [Offline analysis JSON schema](schema/report-v2.1.schema.json)
- [Saved-result schema](schema/saved-result-v1.1.schema.json)

## 開発時の確認

リリース手順は[Releasing gomcdc](docs/releasing.md)を参照してください。

```sh
go test -count=1 ./...
go test -count=1 -race ./...
go vet ./...
```

## ライセンス

MIT Licenseです。詳細は[LICENSE](LICENSE)を参照してください。
