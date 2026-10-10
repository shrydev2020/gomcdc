# セルフレビューの妥当性と修正計画

## 現行版との差分（2026-10-11）

以下は初回レビューと改善履歴を含む。今回の対応と検証根拠は
[カバレッジ機能の再検証](coverage-gaps-2026-10.md)にまとめた。
現行v2はsingle-run、Masking上限はCLI指定可能、HTMLはsweep-line、workspaceは
対応filesystemでcopy-on-writeを使う。`--include-tests`はStatement/Functionも含む。

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
| HTML注釈 | 改善済み、出力下限は残る | `sourceIntervals` はsweep-line。境界処理は `O(A log A)`、重複注釈出力は `O(K)`。全ての重複を表示する出力サイズ `K` 自体は最悪 `O(A²)`。 |
| runtime event記録 | 改善済み、同期I/Oは残る | active evaluationを64 shardへ分割し、writerはpackageごとに直列化する。checkpointはtail byte量に応じて適応的に行う。 |
| workspace copy | 改善済み | treeは毎回走査するが、対応filesystemでは独立inodeのcopy-on-writeを使用する。非対応時は通常copy。 |
| dual-run | v2で解消済み | 現行D22はsingle-run。計装前後のcoverage correspondenceを検証し、同一実行の証拠を統合する。 |

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

**実施状況:** `Build` は既存の`buildContext`による入力/evidence正規化を境界として、
`buildHierarchy`でpackage/file/function/decision/clauseの内容を生成し、
`finalizeReport`で決定論的な階層順序とpackage/module summaryを確定する構造へ分離した。
HTML source viewは引き続き`WriteHTMLReport`の投影処理だけが担当し、JSON/text schemaと
`report.Input`の外部形状は変更していない。reportの既存テスト、通常/race、vetを通過した。

## 性能評価と修正計画

初回レビュー時の計算量分析を基にbenchmarkを追加した。以下の過去計測値と、
2026-10-11の再検証値は別の実装・実行条件なので直接比較しない。

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
Maskingはread-once AND/OR/NOT式に対する厳密なjoint searchであり、completion pairの
Cartesian productを列挙しない。obligationごとのpair/state/solver-byte上限はCLIで
変更可能。上限到達は`analysis-incomplete`となり、通常の未達やinfeasibleへ変換しない。

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

**実施状況:** start/end eventをsortするsweep-lineへ置換済み。active集合を
イベント単位で更新し、非重複caseのbenchmarkと重複区間の回帰テストがある。
境界処理は`O(A log A)`、active集合の出力は`O(K)`。さらにintervalごとの注釈struct
複製を参照共有へ変更し、重複fixtureの割り当て量を約119 MBから約9 MBへ削減した。

重複情報を全て表示する場合の出力サイズ`K`は最悪`O(A²)`になる。これは残っている
全注釈走査ではなく、出力そのものの下限である。注釈情報を省略する変更は行わない。

### PERF-003 runtime event記録

**実施状況:** 収集側の代表benchmarkを追加した。10,000件のself-contained eventを
`CollectDetailed`で読む場合、Apple M2 Proで `23.4 ms/op`、`39.9 MB/op`、
`150,205 allocs/op` だった。生成runtimeについては、writerを非同期化せず、
`active`、writer map、diagnostic state、package writerのロック責務を分離した。
terminal recordを書き込んでからactive stateを削除することで、compactionとterminalの
間にbeginが失われる競合を防いでいる。compactionがBeginのwriter待ち中にbeginを先に
保存した場合は、一時IDで後続の重複begin書込みも抑制する。生成runtimeの並行評価、
compaction、recorder failure fixtureと通常/raceの全テストを通過している。collector benchmarkは維持し、writerの
単独マイクロベンチマークは別途追加しない限り速度改善の定量根拠にはしない。

**現状:** active evaluationを評価IDによる64 shardへ分割し、Condition更新が全体の
mutexを奪い合わないようにした。writer mapはread lockで参照し、未作成時だけwrite
lockを取得する。Begin/terminal/clause eventの同期I/Oとcompactionは対象packageの
writerだけを直列化する。非同期channelへは置き換えず、process異常終了時の回収を維持する。
生成runtimeのwriter benchmarkと並行condition benchmarkも現在は存在する。

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

**実施状況:** macOSのclonefile、LinuxのFICLONEによるcopy-on-writeを追加した。
利用不可なら既存streaming copyへ戻す。hardlinkは使わない。source bytesとmode、
symlinkのtree内制限、relative replace、cleanupの既存テストに加え、inode独立性と
copyへの書込みが元sourceを変えないこと、既存destinationを上書きしないことを検証する。

16 MiB fileのmacOS計測では0.150–0.198 ms/op、streamingでは28.94–34.70 ms/opだった。
64 B fileでは時間短縮を確認していない。詳細は再検証文書を参照する。tree走査は毎回行い、
差分cacheは追加していない。

### PERF-006 test harness build cache

**実施状況:** CLIとinstrumentのintegration testが各テストごとに一時`GOCACHE`を
作成していたため、同一fixtureのGo buildを毎回やり直していた。`-count=1`による
test result再利用禁止は維持したまま、build cacheだけを共有するよう修正した。
代表環境では通常テストが約42秒から約16秒、raceテストが約53秒から約28秒へ短縮した。

### ACCEPT-001 integration acceptance coverage

**実施状況:** integration fixtureへ`runtime.Goexit`をcondition評価中に発生させるケースを追加した。`Goexit`で完了しないdecisionが`completed`へ昇格せず、`aborted` evidenceとして回収されることをCLI integration testで検証している。既存の`assertPackageSums`により、fixture moduleの11指標についてpackage集計とmodule集計の分子・分母一致も同じ受け入れテストで確認する。

### PERF-005 dual-runの扱い

**状態:** v2で完了。現行仕様D22はsingle-runを定義する。Statement/Functionの元source
inventoryと計装後Go cover regionのcorrespondenceを実行前に構築し、同じtest実行から
C0/AST/compiler-selectionを取得する。証拠はproducer単位で検証・受理する。

integration fixtureの実行markerでpackageごとに一回の実行を確認し、v1 dual-runの
比較oracleでC0/ASTの意味が一致することを検証する。`--include-tests`追加後も一回の
実行を維持する。

## 初回計画時の実施順序（履歴）

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
