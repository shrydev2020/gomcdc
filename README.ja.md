# gomcdc

[English](README.md) · [DeepWiki](https://deepwiki.com/shrydev2020/gomcdc)

GoのテストからStatement、Function、Decision、Condition、switch clause、
Unique-Cause MC/DC、Masking MC/DCを計測します。MC/DCの証拠ペアには、
評価値と実行元のtest／subtest名を表示します。

## インストール

Go 1.26以降、Go Modules、LinuxまたはmacOSで利用できます。

```sh
go install github.com/shrydev2020/gomcdc/v2@latest
gomcdc version
```

## 計測と表示

対象moduleで実行します。defaultは全11指標、text出力です。
複数のmain packageも`./...`でまとめて対象にできます。

```sh
gomcdc test ./...
gomcdc test --format json --output coverage.json ./...
gomcdc test --format html --output coverage-html ./...
```

HTMLは`coverage-html/index.html`に出力します。一度の計測から複数の形式を
生成するには、元ソースと計測証拠を含む結果を保存します。

```sh
gomcdc test --save coverage.gomcdc.json ./...
gomcdc report --format html --output coverage-html coverage.gomcdc.json
gomcdc report coverage.gomcdc.json
```

`report`は保存された結果をオフラインで表示します。JSON表示は他ツール向けの
出力、`--save`は再表示・再解析用の保存ファイルです。

## CIでの並列実行

同じソース・package集合・指標・Go/build条件で、各ジョブのテストを選択します。
各ジョブは別のcheckoutと出力先を使います。

```sh
# 別々のCIジョブで実行
gomcdc record --output a.measurement.json ./... -- -run '^TestA'
gomcdc record --output b.measurement.json ./... -- -run '^TestB'

# artifactを集めて統合・解析
gomcdc merge --expect-runs 2 --output combined.json a.measurement.json b.measurement.json
gomcdc analyze --output coverage.gomcdc.json combined.json
gomcdc report --format html --output coverage-html coverage.gomcdc.json
gomcdc require --complete --mcdc-masking 100 coverage.gomcdc.json
```

`record`は実行証拠を保存し、`merge`は実行IDで重複を除いて統合します。
`--expect-runs`は独立した実行数との一致を要求します。`analyze`は統合した
評価から、実行を跨ぐMC/DC証拠ペアも求めます。複数の計測ファイルを直接
`analyze`に渡すこともできます。失敗したジョブの証拠を含める場合は、そのartifactも回収します。

探索上限を変える再解析には、保存結果を指定します。未指定の上限は保存時の値を引き継ぎます。

```sh
gomcdc reanalyze --output expanded.json --mcdc-masking-max-search-states 8000000 coverage.gomcdc.json
```

`require --complete`は実行・証拠・計装・解析の完全性を要求します。
`--METRIC PERCENT`は指標の最低達成率を要求します。判定には測定済みの対象が必要で、
unknown・unsupported・analysis-incompleteを含む指標は最低率の要求を満たしません。

## 指標

| CLI名 | 計測内容 |
| --- | --- |
| `statement` | 実行されたGo statement |
| `function` | bodyが実行されたfunction |
| `decision` | `if`、boolean `for`、conditionless-switch caseのtrue/false結果 |
| `condition` | atomic boolean conditionのtrue/false結果 |
| `switch-clause-body` | 実行されたexpression-switch clause body |
| `type-switch-clause-body` | 実行されたtype-switch clause body |
| `select-clause-body` | 実行された`select` clause body |
| `switch-clause-selection` | 直接選択されたexpression-switch clauseとalternative |
| `type-switch-clause-selection` | 直接選択されたtype-switch clauseとtype alternative |
| `mcdc-unique` | target conditionだけの変化でdecision結果を変えるpair |
| `mcdc-masking` | 他conditionの影響がmaskされ、targetが独立に結果を変えるpair |

Clause Bodyはfallthroughによる実行も数え、Clause Selectionはdispatchによる直接選択を数えます。

## 結果の読み方

各指標はcovered数、分母、達成率、判定状態を表示します。package、file、function、
decisionの順に詳細を辿れます。test context外の実行はtest名を`unknown`と表示します。

| 状態 | 意味 |
| --- | --- |
| `not-covered` | 有効な証拠に必要なwitnessが見つからなかった |
| `infeasible` | MC/DC strategyとGoの評価規則上、達成不能 |
| `unsupported-by-backend` | 計測backendの対応外 |
| `unknown` | 証拠不足で判定不能 |
| `analysis-incomplete` | 探索上限への到達などにより解析が未完了 |

test失敗・panic・timeout・中断時も、回収した証拠と元の失敗を保存します。
Masking MC/DCの既定上限はconditionごとに評価pair 1,000,000件、探索state 4,000,000件、
solver配列64 MiBです。実効値はreportの`maskingAnalysisLimits`に記録します。

## 主なoption

| Option | 用途 |
| --- | --- |
| `--coverage=<list>` | comma区切りで指標を選択。defaultは`all` |
| `--exclude=<glob>` | module-relative pathを除外。繰り返し可能、`**`対応 |
| `--include-tests` | activeな`_test.go`を全選択指標の対象へ追加 |
| `--strict` | unknown、unsupported、解析未完了、未計装で失敗 |
| `--fail-under-<metric>=<percent>` | 選択指標の最低達成率 |
| `--mcdc-masking-max-evaluation-pairs=<count>` | conditionごとの評価pair上限 |
| `--mcdc-masking-max-search-states=<count>` | conditionごとの探索state上限 |
| `--mcdc-masking-max-solver-bytes=<bytes>` | conditionごとのsolver配列上限 |
| `--timeout=<duration>` | テストのtimeout。defaultは10分 |
| `--keep-workdir` | 診断用の計装workspaceを保持 |
| `--workdir=<directory>` | 一時workspaceの親directory |

`--`以降は`go test`へ渡します。全optionは`gomcdc test --help`で確認できます。
対象はmain module内のpackageです。generated sourceとvendorを除外し、
`go.work`はmain moduleが一つの場合に対応します。

終了codeは成功0、test失敗1、計測・読出し・出力失敗2、要求未達3、引数誤り4、中断130です。
オフライン操作は処理自体の成否を返し、元のtest結果は保存結果に表示します。

## 開発と仕様

```sh
go test ./...
go test -race ./...
go vet ./...
```

- [規範仕様](docs/specification.ja.md) · [English reference](docs/specification.md)
- [Report schema](schema/report-v2.0.schema.json) · [複数実行・再解析のschema](schema/report-v2.1.schema.json)
- [計測archive schema](schema/measurement-v1.0.schema.json) · [保存結果schema](schema/saved-result-v2.0.schema.json)
- [リリース手順](docs/releasing.md) · [MIT License](LICENSE)
