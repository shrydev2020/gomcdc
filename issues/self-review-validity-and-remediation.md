# セルフレビューの妥当性と修正計画

## 結論

提示されたセルフレビューは、問題の所在をほぼ正しく捉えている。ただし、
「Phase単位へ分割すればよい」と自動的に結論づけてはいけない。分割は、
責務・データ所有権・失敗の意味を明確にする境界だけに対して行う。

| 項目 | 判定 | 補正 |
| --- | --- | --- |
| データモデル | 概ね妥当 | `Decision`、`Condition`、`Clause`、`DecisionEvaluation`、`SourceLocation` が共有語彙になっている。一方、`report.Input` は静的メタデータ、runtime証拠、実行状態を同時に受け取るため、完全に単純とは言えない。 |
| CLI orchestration | 妥当 | `internal/cli/cli.go` の `runCoverage` がロード、AST解析、workspace管理、計測順序、C0収集、runtime検証、report入力構築、閾値判定、出力を一つの関数で担当している。 |
| report builder | 妥当 | `internal/report/report.go` の `Build` と補助関数群が、証拠の索引化、階層構築、メトリクス集計、状態分類、source view接続を担当している。 |
| MC/DC探索 | 部分的に妥当 | Unique-Causeのpair探索は `O(C × E²)`。Maskingは候補pairごとの式評価・completionもあるため、一般には `O(C × E² × S)`（`S` は式木サイズ）であり、`S≈C` なら `O(E² × C²)` まで増える。 |
| HTML注釈 | 妥当だが下限に注意 | `sourceIntervals` は境界ごとに全注釈を走査するため、区間生成は最悪 `O(A²)`。sweep-lineで走査は改善できるが、重なり結果を全てHTMLへ出すなら出力サイズ `K` 自体が最悪 `O(A²)` になる。 |
| runtime event記録 | 妥当 | `recordMu` を保持したままJSON marshal、ファイルwrite、一定回数ごとのcompactionを行うため、goroutine数とイベント量に応じて競合・I/O待ちが増える。 |
| workspace copy | 妥当 | 現在はmodule treeを毎回全コピーする。単純なhardlinkは計装時の書き込みが元sourceへ波及するため不適切。 |
| dual-run | 「ボトルネック」ではあるが現状は仕様どおり | C0は元sourceのGo標準cover、AST系は計装workspaceで取得するため、両方を選ぶと2 runになる。仕様D22がこの動作を明示しており、削除は最適化ではなく証拠モデルの変更になる。 |

## ライセンスの現状

プロジェクトライセンスはMIT Licenseに決定した。`LICENSE` に本文を置き、
`THIRD_PARTY_NOTICES.md` のGo `cmd/cover`由来コードの通知とは分離する。

### L-001 プロジェクトライセンスを決定する

- **状態:** 完了
- **対応:** `LICENSE` にMIT License本文を追加し、READMEの日英から参照する。
- **完了条件:** `LICENSE` が存在し、README、配布物、third-party noticeの関係が
  一貫している。完了済み。

## アーキテクチャ評価

### ARC-001 CLI orchestrationの責務分離

**妥当性:** 高い。`runCoverage` は制御フローの順序を決めるだけでなく、各段階の
詳細なデータ変換とエラー分類まで抱えている。これは変更理由が異なる処理が同じ
関数へ集まり、テスト対象の境界も曖昧になる状態である。

**修正方針:** パッケージを機械的に増やさず、まず純粋な結果型を導入して境界を固定
する。

1. `load/analyze` の出力を、対象sourceと静的metadataを含む値として固定する。
2. `measurement` を、standard-cover runとAST runの実行だけを担当する境界にする。
3. `evidence` を、event journal/C0 profileの収集・検証・部分回収に限定する。
4. `report` へ渡す入力を、検証済みevidenceとrun stateの合成結果として構築する。
5. CLI関数は上記の順序、失敗分類、終了コードだけを調停する。

**禁止:** `Phase1`、`Manager`、`Service` など責務を説明しない名前のパッケージを
追加しない。既存の挙動を保ったまま、一段ずつ抽出する。

**実施状況:** `internal/cli/measurement.go` の `measure` がworkspace作成、standard-cover
実行、AST計装と実行、event/C0収集、観測検証を担当する。`runCoverage` は入力準備、
実行順序、report assembly、終了コードとthreshold判定を担当し、workspace cleanupの
タイミングと既存のエラー文言を維持する。`measurementRequest` と
`measurementOutcome` はこのpackage内だけの値境界であり、汎用ManagerやServiceは追加
していない。既存integration、通常/race、vetを通過している。

**完了条件:** 既存fixtureのJSON/text/HTMLが同一意味を持ち、package failure、
timeout、partial evidence、threshold failureの終了コードが変わらない。抽出後に
`go test -race ./...` が通る。

### ARC-002 report builderの入力境界と集計境界

**妥当性:** 高い。`Build` は入力の索引化、evidenceのpackage帰属、decision/clauseの
階層化、状態分類、summaryの再計算、source view接続を一連で行う。

**修正方針:** `Build` を外部から見える単一入口として維持し、内部を次の純粋な変換へ
分ける。

- `indexEvidence`: IDからdecision/clause/package evidenceを引く。
- `buildHierarchy`: package/file/function/decision/condition/clauseを生成する。
- `aggregateSummaries`: 子要素から分子・分母・unknown等を集計する。
- `attachSourceViews`: 元sourceとbyte-range annotationを接続する。

各変換の入力と出力を専用型で表し、`report.Input` に新しい責務を追加しない。
JSON schemaの変更を伴う分割は別issueにし、まず内部関数の純粋性とテストを確立する。

## 性能評価と修正計画

現状には専用benchmarkがないため、下記の計算量はコードからの分析であり、実測値
ではない。最適化前に代表的なfixtureと人工的な最悪ケースをbenchmarkへ追加する。

### PERF-001 MC/DC評価pair探索

**実施状況:** Unique-Causeの探索を完了。小規模入力または早期witnessはallocation-free
の探索を維持し、大規模・witness無し入力では非対象condition vectorの索引を使う。
代表benchmark（Apple M2 Pro、8条件・256評価）では、witness無しの比較で
`982052 ns/op`（二重ループ）から `176577 ns/op`（索引化を含むハイブリッド）へ改善した。
早期witnessのケースは `930 ns/op` から `1698 ns/op` となるため、索引を常時使わず
小規模・早期成立の経路を残している。Maskingは各evaluation・targetについて、観測された
短絡経路に整合する全completionを列挙し、completion pairごとにD19のnon-target masking
条件を検証するよう変更した。独立oracle付きfixtureでwitnessの正当性を検証している。
completionの完全列挙は式の条件数に対して指数的になり得るため、64条件のread-once式でも
path constraintで不要なassignmentを展開せず完了することを回帰テストで固定した。
この項目の実装とbenchmarkを完了とする。

**現状:** `mcdc.UniqueCauseStrategy` はtarget以外の状態vector索引を使用する。
`MaskingStrategy` はcompletion列挙を一度行い、pairごとの式木再走査を避ける。
completion列挙自体は一般に指数的だが、短絡pathとpivotal pathの制約で探索空間を削減する。

**修正計画:**

1. `E`（評価ベクトル数）、`C`（condition数）、`S`（式木サイズ）を変化させる
   benchmarkを追加する。
2. Unique-Causeは、target以外の状態ベクトルとdecision resultをキーにした索引を
   構築し、最初の決定論的witnessを保持する。`not evaluated`をfalseへ変換しない。
3. Maskingはevaluationごとの全completionをtarget単位で先に計算し、pairループでD19の
   masked conditionを検証する。masked conditionの証跡は保持する。
4. 最適化前後でwitness、状態、並び順、possibly-infeasible判定を比較する。

**完了条件:** benchmarkで改善を確認し、既存のMC/DC fixture、race、決定論性テストが
通る。入力が小さい場合に索引構築のオーバーヘッドが増えないことも確認する。

### PERF-002 HTML source annotation

**現状:** `sourceIntervals` は全境界をsortした後、各intervalについて全annotationを
調べる。注釈数を `A` とすると区間生成は最悪 `O(A²)`。

**修正計画:** start/end eventをsortするsweep-lineへ置き換え、active annotationの
更新をイベント単位で行う。active集合の出力順は現在の決定論的順序を維持する。

ただし、重なりを個別tooltipとして出力する限り、出力されるactive組合せ数 `K` は
最悪 `O(A²)` であり、アルゴリズムだけでHTML全体を常に `O(A log A)` にはできない。
重複表示を減らす場合は表示仕様の変更として別途承認する。

**完了条件:** source annotationのbyte範囲、metric class、tooltip、HTML snapshotが
変わらず、非重複ケースの生成時間と重複ケースの出力サイズを別々に計測できる。

### PERF-003 runtime event記録

**実施状況:** 収集側の代表benchmarkを追加した。10,000件のself-contained eventを
`CollectDetailed`で読む場合、Apple M2 Proで `23.4 ms/op`、`39.9 MB/op`、
`150,205 allocs/op` だった。生成runtimeについては、writerを非同期化せず、
`active`、writer map、diagnostic state、package writerのロック責務を分離した。
terminal recordを書き込んでからactive stateを削除することで、compactionとterminalの
間にbeginが失われる競合を防いでいる。生成runtimeの並行評価、compaction、recorder failure
fixtureと通常/raceの全テストを通過している。collector benchmarkは維持し、writerの
単独マイクロベンチマークは別途追加しない限り速度改善の定量根拠にはしない。

**現状:** `internal/runtimecov` は `activeMu`、`writersMu`、`diagnosticMu` と
packageごとの `writerState.mu` で共有状態を保護する。Conditionはactive stateの
短い更新だけをロックし、Begin/terminal/clause eventの同期I/Oとcompactionは対象
packageのwriterだけを直列化する。非同期channelへ置き換えてはいない。

**修正計画:**

1. まず `active` 状態、writer map、diagnostic stateのロック責務を分離できるかを
   確認する。
2. package単位のlockまたはlock shardを導入し、別packageのeventを不必要に直列化
   しない。
3. buffered writerを採用する場合は、terminal eventのflush、panic、`Goexit`、timeout、
   異常終了、部分回収時の耐久性を仕様化する。単なる非同期channel置換は禁止する。
4. event順序、EvaluationID、重複compaction、diagnosticの欠落がないことをraceと
   異常終了fixtureで検証する。

**完了条件:** goroutine/package数を増やしたfixture、compaction、recorder failureで
runtime eventが失われず、`go test -race ./...` が通ること。writerの単独速度比較を
追加しない限り、I/O削減そのものを主張しない。

### PERF-004 workspace copy

**実施状況:** 200 regular files（各ファイル約40 bytes）のmoduleを作るbenchmarkを追加し、
Apple M2 Proで `35.0 ms/op`、`6.9 MB/op`、`3,888 allocs/op` を観測した。hardlinkは
計装時の書き込みを元sourceへ伝播させるため採用しない。reflinkはOS依存のため、利用可能性
検出とsymlink・mode・cleanupを含むfixtureを先に用意するまで変更しない。

**現状:** `workspace.copyTree` はmodule全体のregular fileを毎回コピーする。AST計装
がcopyへ書き込むため、単純なhardlinkは元sourceの変更を引き起こし、意味保存条件に
反する。

**修正計画:**

- まずcopy対象のファイル数、byte数、所要時間を記録する。
- 利用可能な場合だけOSのreflink/copy-on-writeを使い、失敗時は通常copyへ戻す。
- 差分コピーを導入する場合は、削除、rename、symlink、mode、relative replace、
  module外参照を含むfixtureを用意する。
- hardlinkを使う場合は、書き込み対象を完全に別inodeへ分離する仕組みが必要であり、
  `os.Link`だけの実装は許可しない。

**完了条件:** source bytes、mode、symlink境界、計装結果、cleanupが現状と同じで、
  コピー時間またはI/O量の改善を実測できる。

### PERF-006 test harness build cache

**実施状況:** CLIとinstrumentのintegration testが各テストごとに一時`GOCACHE`を
作成していたため、同一fixtureのGo buildを毎回やり直していた。`-count=1`による
test result再利用禁止は維持したまま、build cacheだけを共有するよう修正した。
代表環境では通常テストが約42秒から約16秒、raceテストが約53秒から約28秒へ短縮した。

### PERF-005 dual-runの扱い

**判定:** 直ちに削除・統合しない。仕様D22は、C0とAST系を同時に要求したとき、
元source runと計装runを一回ずつ実行し、run statusを分離することを定義している。
これは単なる重複実行ではなく、異なるevidence producerを同じsource modelへ統合する
ための境界である。

**計画:**

1. dual-runのworkspace作成、build、test、C0解析、AST収集それぞれを計測する。
2. 仕様D32のAST-only 2倍以内、両MC/DC込み5倍以内という目標と比較する。
3. 同一processで両証拠を得る方法を検討するのは、Go cover profileとAST計装の干渉を
   fixtureで証明できる場合だけにする。
4. 実行回数を減らす変更は、side effect、panic、defer、test cache、package failure、
   C0/ASTの証拠帰属を再検証してから別issueで提案する。

## 実施順序

1. **L-001:** プロジェクトライセンスの決定（完了: MIT License）。
2. **計測基盤:** MC/DC、HTML、runtime、workspace、dual-runのbenchmarkを追加する。
3. **ARC-001 / ARC-002:** 挙動を変えない純粋な境界抽出。JSON/text/HTMLのsnapshotと
   failure fixtureを固定する。
4. **PERF-001:** MC/DCの索引化。数学的な証跡と決定論性を最優先する。
5. **PERF-002 / PERF-003:** HTMLとruntimeを、出力・耐久性を壊さない範囲で改善する。
6. **PERF-004:** reflinkまたは安全なcopy-on-writeが利用できる環境だけを対象に検討する。
7. **PERF-005:** 実測が仕様目標を超える場合に限り、dual-run削減案を別途設計する。

## 完了判定

- `go test -count=1 ./...`
- `go test -race -count=1 ./...`
- `go vet ./...`
- JSON/text/HTMLの既存意味が不変
- MC/DC witness、`not evaluated`、aborted、partial evidenceが不変
- 複数package、panic、timeout、異常終了の回収が不変
- benchmarkの改善値と適用した最適化の前提を記録
- READMEとライセンス表示が実際の配布条件と一致
