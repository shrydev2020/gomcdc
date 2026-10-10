# カバレッジ機能の再検証（2026-10-11）

DeepWikiの指摘と現行v2を照合した結果。Windows対応は対象外。

| 項目 | 対応 |
| --- | --- |
| テストsourceのStatement/Function | `--include-tests`で選択した全指標へ含める。Go cover自身の配置したcounterを回収し、既存の元source inventoryへ投影する。internal/external test、test-only package、除外とbuild tagを対象に検証する。 |
| Masking MC/DCの上限 | 現行版ですでに`--mcdc-masking-max-evaluation-pairs`、`--mcdc-masking-max-search-states`、`--mcdc-masking-max-solver-bytes`で変更可能。上限は近似解を返す仕組みではない。厳密探索が完了しない場合は引き続き`analysis-incomplete`とする。無制限化による完了保証はできない。 |
| dual-run | 現行D22と実装はsingle-run。C0/AST/compiler-selectionを一つの実行から取得し、元sourceとのcorrespondenceを検証する。v1 dual-runは比較用テストoracleだけに残る。 |
| workspace copy | macOSのclonefile、LinuxのFICLONEを利用可能時に使用する。独立inodeなので書込みは元sourceに波及しない。非対応filesystem・別deviceでは通常copyへ戻す。tree走査と毎回の独立snapshotは維持する。差分cacheは追加しない。 |
| HTML注釈 | 現行版ですでにsweep-line化済み。さらに重複区間ごとの注釈struct複製を参照共有へ変更した。境界探索は`O(A log A)`、active注釈の出力は`O(K)`。全重複情報を表示する際の`K=O(A²)`は残る。 |
| runtime | 既存のpackage別writer lockと適応的checkpointに加え、active evaluationを64 shardへ分割し、writer lookupにread lockを用いる。Condition更新の全goroutine共通lockを解消する。writerはpackage単位で同期記録し、異常終了時の回収を維持する。 |

## 証拠モデル

test fileにも選択されたGo coverを適用し、実際のcounter metadataと事前inventoryを
照合する。test counterはprocess/sourceごとのfileに保存し、run IDとsource digestを
検証してからGo-cover producerへ統合する。独立したcounter directoryを使用するため、
AST journalの検証規則を緩めない。

初期値0のcounterは一時fileからrenameで公開する。初回hitだけ同期書込みし、同じ
counterの再実行は`sync.Once`の高速経路にする。終了時flushは不要で、panic、FailNow、
Goexitでも実行済みcounterを回収できる。file descriptorは書込みごとに閉じる。

テストcontextのprologueと元statementの間にcover境界を追加し、Run/Cleanup等の
wrapperにも元の開始位置を保持する。これによりテスト名の記録をstatement分母へ
混入させず、元statementとの対応を維持する。

## ローカル計測

macOS / Apple M2 Pro / Go 1.27.1。`BenchmarkRegularFileCopy`、各10回、2 sample。

| file | 自動copy（この環境ではclone） | streaming copy |
| --- | --- | --- |
| 64 B | 0.134–0.197 ms/op、448 B/op | 0.127–0.143 ms/op、33,344 B/op |
| 16 MiB | 0.150–0.198 ms/op、448 B/op | 28.94–34.70 ms/op、33,344 B/op |

大きいfileでのcopy時間とallocationを削減した。小さいfileの時間短縮は主張しない。
これはcopyと削除のmicrobenchmarkであり、module全体のtest実行速度ではない。
Linux/amd64向けworkspace test binaryのcross-buildは成功。Linuxのclone経路の実行はmacOSでは確認できず、Linux CIで確認する。

runtimeには16 goroutine・4,000評価・各64 conditionの生成runtime benchmarkを追加した。
同期I/Oは残るため、shard数だけを根拠に全体の高速化を主張しない。

## README整合性の確認

| Severity | Location | Finding | Action |
| --- | --- | --- | --- |
| P1 | README.md / README.ja.md のinclude-tests説明 | AST限定という説明が追加機能と不一致 | Statement/Functionも対象と明記し、CLI helpと日英仕様を同時更新 |

installは`go install github.com/shrydev2020/gomcdc/v2@latest`、実行は対象moduleから
`gomcdc test ./...`。実行名の変更はない。今回のREADME修正はinclude-testsの用途と
対応範囲に限定し、操作に不要な実装詳細はこの文書と仕様へ記載した。

HTMLの`BenchmarkSourceIntervalsOutputSize`では、1,000件の入れ子注釈について、
interval構築の割り当て量が118,883,210 B/opから8,991,493 B/opへ減った。
注釈全体の複製を参照へ置き換えた効果であり、tooltipやclassは省略していない。
非重複1,000件の割り当て量は548,608 B/opから444,608 B/op。

runtimeの同じ並行fixture（10回×3 sample）では、変更前83.1–106.9 ms/op、
変更後83.4–90.1 ms/opだった。範囲が重なるため全体の速度改善は確定していない。
condition更新の全体lockは除去したが、package writerの同期I/Oは残る。

## 検証

- Go 1.27.1: 通常全テスト、race全テスト、`go vet ./...`。
- Go 1.26.0: include-tests、recorder failure、cover metadataの対象テスト。
- 対象module自身を`-race`で実行する並行test counter fixture。
- 既存のsingle-run marker、Masking上限、runtime compaction/failure、HTMLテスト。
- Linux/amd64向けworkspace test binaryのcross-build（実行は未確認）。
- gomcdc自身のMC/DC計測もstrictを通過し、既存CIのmodule/package閾値を全て満たした。
  計測時点のmodule値はDecision 73.10%、Condition 71.25%、Unique-Cause 46.09%、
  Masking 45.56%。その後のcounter書換えのallocation削減はrace付き対象テストで再検証済み。
