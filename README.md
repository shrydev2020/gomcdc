# gomcdc

`gocoverage` は、Go 標準の statement coverage と AST 計装による decision / clause body / condition / MC/DC coverage を、同じ元ソースモデルへ統合する CLI です。

既定の `--coverage=all` は `dual-run-standard-cover` モードです。元ソースに対する Go 標準 C0 計測と、計装済み一時 workspace に対する AST 計測を、独立した2回の `go test` として実行します。結果は同じレポートへ統合しますが、同一テスト実行で得た証跡として混同しません。

Go 1.24 以降、Go Modules、macOS / Linux を対象にしています。

## 名前と package 境界

- repository: [`shrydev2020/gomcdc`](https://github.com/shrydev2020/gomcdc)
- Go module: `github.com/shrydev2020/gomcdc`
- executable / product: `gocoverage`

repository と実行ファイルは別の役割です。repository/module は公開先に合わせて `gomcdc`、ユーザーが入力するコマンド名は既存要件と互換な `gocoverage` としています。

本プロジェクトの安定した外部契約は CLI と versioned JSON report です。`analyzer`、`instrument`、`runtimecov`、`gotest`、`workspace` などは、ユーザーコードを書き換え・実行する実装詳細であり、外部moduleから誤って依存されないよう `internal` に置きます。`internal` は技術的な必須条件ではなく、Go library APIをまだ公開しないという互換性方針です。共通語彙は曖昧な `model` ではなく `internal/coverage` に集約しています。将来library APIを公開する場合は、安定化したcoverage data modelとpureなMC/DC APIだけを明示的に昇格させます。

## 計測指標

- Statement Coverage（C0）: Go 標準 cover profile の statement 数を使用します。
- Function Coverage: 関数内の statement が 1 つ以上実行されれば covered です。statement を持たない空関数はデフォルトで分母から除外します。
- Decision Coverage（`c1` alias）: `if`、条件付き `for`、および conditionless switch（`switch { ... }`）の各 case conditionについて、式全体の true / false を数えます。完全な CFG Edge Coverage ではありません。
- Switch Clause Body Coverage: expression switch と conditionless switch の各 case/default節について、その body が実行されたかを数えます。
- Type Switch Clause Body Coverage: type switch の各 case/default節について、その body が実行されたかを数えます。
- Select Clause Body Coverage: `select` の各 case/default節について、その body が実行されたかを数えます。
- Condition Coverage（`c2` alias）: `&&` / `||` で結合された原子的条件ごとに true / false を数えます。短絡された条件は `not evaluated` であり、false ではありません。
- Unique-Cause MC/DC: 対象条件と decision 結果だけが変わり、他条件の状態が同一である評価ペアを要求します。`not-evaluated` は独立した状態です。
- Masking MC/DC: 対象条件が両評価ベクトルで decision に対して pivotal であり、他条件の差が論理的にマスクされる評価ペアを認めます。

MC/DC の各 covered condition には、条件ベクトル、decision 結果、成立ペアが JSON / text の両方に出力されます。テスト名を正確に取得できない場合、`TestID` は推測せず `unknown` になります。

同一の原子的source expressionが1 decision内に複数回現れる場合、呼出しや副作用によって値が結合しているか独立しているかをASTだけから一般には証明できません。この場合、Unique-Causeは観測済みベクトルだけで判定できますが、counterfactual completionを使うMasking MC/DCは推測値を返さず `unknown` とし、計装対応率にも反映します。

### switch / type switch / select の意味

AST backend が正式に計測するのは clause body の実行です。次の情報は、AST だけでは意味を保存したまま正確に取得できないため推測しません。

- expression switch で一致した個別の case expression
- type switch で一致した個別の型 alternative
- case 節の直接選択
- fallthrough edge

このため、fallthrough 先の body 実行と、その case 節が直接選択されたことは区別できません。レポートでは selection capability を `unsupported-by-backend` とし、body execution を正式な coverage として表示します。`case A, B` や複数型を列挙した type case は節 body 単位であり、A/Bや各型 alternative を個別の coverage item にはしません。default がない switch/type switch の no-match も、AST backend では合成 coverage item にしません。正確な matched expression / matched type alternative / direct selection / fallthrough edge は将来の compiler backend の責務であり、その backend がない状態で covered / not covered を返しません。

conditionless switch では、各 case condition が独立した boolean decision です。`case a && b, c:` では `a && b` と `c` を別々の source decision とし、case 節全体を暗黙の OR decision として合成しません。先行する case condition が true になり、後続の case condition が評価されなかった場合、後続 decision は false ではなく `not evaluated` として記録します。その case 節の body 実行は、decision / condition / MC/DC とは別に Switch Clause Body Coverage へ記録します。

## インストールと実行

```bash
go install github.com/shrydev2020/gomcdc/cmd/gocoverage@latest
gocoverage test ./...
```

リポジトリ内で直接実行する場合:

```bash
go run ./cmd/gocoverage test ./...
```

デフォルトの `--coverage=all` は全指標を有効にし、次の2計測を実行します。

1. `standard-cover`: 元ソースを `go test -coverprofile` で計測し、Statement / Function Coverage を取得
2. `ast`: 計装済み一時 workspace で decision / clause body / condition / MC/DC を計測

JSON / text レポートには `measurementMode: dual-run-standard-cover` と各 measurement run の結果を含めます。Statement / Function だけを指定した場合は `standard-cover`、AST 指標だけを指定した場合は `single-run` です。dual-run は各measurementに元ソースから作った別々の一時module copyを使うため、1回目がmodule内へ書いた状態は2回目へ持ち越しません。一方、module外のサービス・ファイル・環境などの外部状態や非決定的な結果は2回で異なり得るため、C0 と AST の個々の評価を同一実行の出来事として関連付けることはできません。

```bash
gocoverage test \
  --coverage c0,function,decision,clause,condition,mcdc-unique,mcdc-masking \
  --format json \
  --output coverage.json \
  ./...
```

alias:

- `c0` = `statement`
- `c1` = `decision`
- `c2` = `condition`
- `mcdc` = `mcdc-unique,mcdc-masking`
- `all` = `c0,function,decision,clause,condition,mcdc-unique,mcdc-masking`

`clause` は3種類の Clause Body Coverage を有効にする名前です。直接選択 coverage の別名ではありません。

`--` 以降は `go test` へ渡されます。カバレッジ実行がキャッシュヒットで省略されないよう、ユーザー指定にかかわらず最後に `-count=1` を強制します。`-cover` / `-covermode` / `-coverprofile` / `-coverpkg` と `-json` はmeasurement側が所有し、明示引数と `GOFLAGS` の両方から除去して各runに必要な値だけを設定します。AST runには `-cover=false` を明示し、標準coverを計装済みsourceへ重ねません。build tagなど無関係な `GOFLAGS` は維持します。

```bash
gocoverage test ./... -- -race -run TestPolicy
```

## CI 閾値

```bash
gocoverage test \
  --fail-under-statement 85 \
  --fail-under-function 80 \
  --fail-under-decision 90 \
  --fail-under-clause 80 \
  --fail-under-condition 85 \
  --fail-under-mcdc-unique 70 \
  --fail-under-mcdc-masking 80 \
  ./...
```

`--fail-under-c1`、`--fail-under-c2`、`--fail-under-mcdc` は提供しません。Decision、Condition、Unique-Cause MC/DC、Masking MC/DC の閾値を明示的に指定してください。

`unsupported`、`unknown`、`aborted`、`possibly infeasible` は別件数として保持します。`possibly infeasible` は、式木から対象条件を pivotal にできないこと、または短絡状態を同一に保つ Unique-Cause ペアを構成できないことを証明できた場合に使用し、単なるテスト不足には使用しません。デフォルトの `--special-denominator=exclude` では分母から除外します。未達として分母へ含める場合は `--special-denominator=include` を指定します。

レポートは `standard-cover` と `ast` を別 producer として backend capability を表示し、さらにツール全体の集約 capability を `supported` / `unsupported-by-backend` / `unknown` に分けます。計装 coverage は、要求された metric entity 単位で discovered / supported / instrumented / unsupported / unknown の件数を表示します。`--strict` を指定すると、対象となる計装項目に unsupported / unknown がある場合、または supported entity に必要な計装証跡が作られなかった場合に非ゼロ終了します。分母からの除外は、計装の欠落を隠すものではありません。

終了コード:

| Code | 意味 |
|---:|---|
| 0 | テスト成功、閾値達成 |
| 1 | build / test failure または timeout（取得済みデータで partial report を生成） |
| 2 | coverage threshold failure |
| 3 | package load / instrumentation / strict capability failure |
| 4 | CLI usage error |
| 5 | runtime data corruption / report / internal failure |

テスト失敗時は閾値判定を行いません。ランタイムデータの整合性エラーは、誤った coverage を成功扱いしないため test failure より優先されます。

## レポートとソース位置

`--format=text` と `--format=json` を提供します。JSON schema version は `1` です。すべての割合に分子・分母があり、module / package / file / function / decision / condition / clause の階層を保持します。Clause の集計名は `clause`、`switchClauseBody`、`typeSwitchClauseBody`、`selectClauseBody` です。JSON に `clauseBody` という別名は出力しません。

C0 は元ソースを対象にした標準 cover profile から取得します。AST metadata と runtime evidence は、計装後の一時行ではなく元の module-relative file、行、列へ正規化されます。本ツールが生成した bridge/runtime/probe だけを分母から除外し、ユーザーmoduleに元から存在する `// Code generated ... DO NOT EDIT.` ソースは通常どおり計測します。ユーザーの作業ツリーは変更せず、module の複製だけを計装します。

複数 package pattern は各 measurement の `go test` コマンドへまとめて渡されます。AST measurement では、各 test process が package import path、PID、run ID、nonce を含む個別ファイルへ書き、CLI が終了後に回収します。dual-run では、別に取得した元ソースの標準 cover profile と AST evidence を、測定由来を保持したまま module report へ統合します。

## 主なオプション

```text
--coverage <list>                 計測指標（default: all）
--format text|json                出力形式
--output <path>                   出力先（default: stdout、- も stdout）
--exclude <glob>                  module-relative除外glob、複数指定可、**対応
--include-tests                   activeな_test.goもAST計測対象にする
--keep-workdir                    計装済み一時workspaceを保持する
--workdir <parent>                一時workspaceの親directory
--timeout <duration>              go test全体の上限（default: 10m、0で無効）
--special-denominator exclude|include
--strict                          unsupported / unknown / 未計装の対象があれば失敗
```

元ソース位置の権威を維持できない `go test -overlay` は、明示引数と `GOFLAGS` のどちらから指定されても拒否します。activeな `go.work` を使った複数 main module の統合も対象外で、1 moduleずつ実行します。

## 開発

```bash
go test ./...
go test -race ./...
go vet ./...
staticcheck ./...
```

`internal/cli/testdata` の fixture module を使い、短絡、nested condition、両 MC/DC、panic abort、再帰、goroutine、conditionless switch の not-evaluated、3種類の Clause Body Coverage、複数 package、部分的 build failure、dual-run C0 source mapping、閾値を end-to-end で検証します。
