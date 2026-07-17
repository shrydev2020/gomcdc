# gomcdc

[English](README.md)

`gomcdc` はGoのtestを実行し、Statement、Function、Decision、Condition、
switch clause、Unique-Cause MC/DC、Masking MC/DCを一つのcoverage reportへ
まとめます。

Go標準coverageがstatementとfunctionの実行有無を測るのに対し、`gomcdc`は
boolean評価vectorも記録し、各conditionがdecision結果へ独立に影響したかを
示します。clause bodyの実行とswitch/type-switchの直接selectionを区別し、
test失敗や中断時も検証可能なpartial resultを保持します。

## 動作要件

- Go 1.26.x（1.26.0以降）
- Go Modulesを使用する計測対象
- LinuxまたはmacOS

compiler-aware計装は選択されたGo 1.26.xのcompiler sourceにexact anchorが存在する
ことを検証し、互換性がなければ明示的に失敗します。

## インストール

```sh
go install github.com/shrydev2020/gomcdc@v1.0.1
```

最新releaseへ追随する場合は`@latest`を使用します。`gomcdc version`で
インストールされたbuildを確認できます。

## Quick start

計測対象moduleで`gomcdc`を実行します。

```sh
cd /path/to/your/module
gomcdc test ./...
```

defaultでは11指標をすべて有効にし、text reportを標準出力へ書きます。有効な
summaryはcovered数、分母、percentageに加え、通常のcovered/not-coveredへ
含められないevidenceを状態別に表示します。

```text
Summary:
  Decision Coverage: enabled=true 22 / 30 = 73.33% unsupported=0 unknown=0 infeasible=0 analysis-incomplete=0
  Condition Coverage: enabled=true 34 / 46 = 73.91% unsupported=0 unknown=0 infeasible=0 analysis-incomplete=0
  Unique-Cause MC/DC: enabled=true 8 / 15 = 53.33% unsupported=0 unknown=0 infeasible=8 analysis-incomplete=0
  Masking MC/DC: enabled=true 14 / 23 = 60.87% unsupported=0 unknown=0 infeasible=0 analysis-incomplete=0
```

module summaryの後にpackage、file、function、decision、condition、MC/DC witness、
観測した評価vectorを展開します。

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

```sh
# Textを標準出力へ表示
gomcdc test ./...

# JSON file
gomcdc test --format json --output coverage.json ./...

# coverage-html/index.htmlへHTMLを出力
gomcdc test --format html --output coverage-html ./...
```

通常の未達と区別して次の状態を解釈してください。

| 状態 | 意味 |
| --- | --- |
| `not-covered` | 有効なevidenceはあるが必要なcoverage witnessが見つからない |
| `infeasible` | 選択したMC/DC strategyとGoの評価規則ではobligationを成立させられない |
| `unsupported-by-backend` | 有効なmeasurement backendがobligationを証明できない |
| `unknown` | evidence authorityまたはmeasurement completenessが不足して判定できない |
| `analysis-incomplete` | Masking MC/DCの有限な探索予算到達を含め、obligationの正確なanalysisが完了しなかった |

`unknown`、`unsupported-by-backend`、`infeasible`、`analysis-incomplete`を通常の
未達へ暗黙変換しません。test失敗、panic、timeout、runtime evidenceのtruncated
tailが発生しても、信頼できるevidenceが残ればpartial reportを生成します。

Masking MC/DCはexact searchします。組み込みのcondition obligationごとの上限は、
candidate evaluation pair 1,000,000件、search state 4,000,000件、search workspace
64 MiBです。いずれかを超える探索が必要な場合は`analysis-incomplete`とし、
`not-covered`や`infeasible`へ変換しません。現時点では、これらの上限を変更する
CLI optionはありません。

## 主なoption

```sh
# 指標を選択
gomcdc test --coverage=decision,condition,mcdc-unique,mcdc-masking ./...

# module-relative pathを除外。--excludeは繰り返し可能で**を使用可能
gomcdc test --exclude='internal/generated/**' ./...

# activeな_test.go decisionをAST metricの分母へ追加
gomcdc test --include-tests ./...

# --以降をgo testへ渡す
gomcdc test ./... -- -count=1 -run TestCritical
```

| Option | 用途 |
| --- | --- |
| `--coverage=<list>` | comma区切りで指標を選択。defaultは`all` |
| `--exclude=<glob>` | module-relative source globを除外。繰り返し可能 |
| `--include-tests` | activeな`_test.go` decisionをAST metricへ追加 |
| `--format=text\|json\|html` | report形式を選択 |
| `--output=<path>` | fileへ出力。HTMLの場合はdirectory |
| `--strict` | requested entityにunsupported、unknown、analysis-incomplete、未計装があれば失敗 |
| `--fail-under-<metric>=<percent>` | 有効な一指標がthreshold未満なら失敗 |
| `--timeout=<duration>` | `go test` subprocessのtimeout。defaultは10分 |
| `--keep-workdir` | 診断用に計装済みtemporary workspaceを保持 |
| `--workdir=<directory>` | temporary workspaceの親directoryを指定 |

完全なoption一覧は`gomcdc test --help`で確認できます。

## CI policy

`--strict`で不完全なmeasurementを拒否し、`--fail-under-*`でcoverage policyを
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
| 1 | 一つ以上の`go test` runが失敗 |
| 2 | measurement、instrumentation、integrity、reportの失敗 |
| 3 | coverage threshold未達 |
| 4 | 不正なCLI usage |

## 対応範囲と制約

- package patternはmain module内のpackageへ解決される必要があります。標準
  library、外部module、`vendor`、Go標準generated-code commentを持つfileは
  除外します。
- `_test.go` decisionは`--include-tests`指定時だけAST metricへ含めます。
  Statement/Function CoverageはGo標準coverageに基づきます。
- Windows、assembly内部、cgo内部、compiler IR obligation、path coverage、
  distributed test executionはv1対象外です。
- 対象moduleを現在ユーザーの権限でbuild/testします。temporary workspaceは
  悪意ある対象codeに対するsandboxではありません。
- `gomcdc`はcoverage意味論を定義しますが、安全認証、DO-178C適合、tool
  qualificationを主張しません。

## Reference

- [規範仕様](docs/specification.ja.md)
- [英語参考仕様](docs/specification.md)
- [JSON report schema](schema/report-v1.1.schema.json)

## 開発時の確認

```sh
go test -count=1 ./...
go test -count=1 -race ./...
go vet ./...
```

## ライセンス

MIT Licenseです。詳細は[LICENSE](LICENSE)を参照してください。
