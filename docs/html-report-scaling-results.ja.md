# HTMLページ分割の実装結果

2026-10-12。[設計](html-report-scaling-design.ja.md)に基づき、CLIのHTML出力をページ群に変更した。

## 変更

- `index.html` は概要と索引へのリンクだけを読み込む。package/file/function、Findings、source、condition、witness、evaluations、producer診断は別ページ。
- 1文書の上限はescape後1 MiB・5,000要素。通常の索引は最大100項目、evaluationsは最大200観測かつ16条件列の組に分け、容量・要素数でさらに分割する。長文fieldも全文を継続ページに保存する。
- ソースはbyte位置を持つ連続範囲に分割。色モード6種類を保ち、詳細な注釈は索引に一度ずつ記載する。色の計算はmetric/stateごとのactive件数を使い、各区間に重なり注釈配列を複製しない。
- MC/DCはA/Bの結果とtestを並べ、condition比較tableを先に表示する。詳細なrun/package/process/evaluation出所は同じページで展開できる。completionを観測値と混同しない。
- CSSを共通fileにし、短い通常fieldには余分なspanを付けない。ページ分割による総出力の増加も抑える。
- generation全体の容量・要素数・リンク先を確認し、最後に入口を置換する。同一内容のgenerationは検証後に再利用し、旧generationは既存タブ用に残す。壊れた既存generationやsymlinkは再利用しない。
- 単一document用 `WriteHTMLWithSources` は内部APIとして維持し、CLIは `WriteHTMLDirectory` を使う。単一document APIのメモリ上限を保証する変更ではない。

## 同じ入力による比較

リポジトリ全体の保存済み結果を使い、テストを再実行せずに両形式を生成した。入力は220,307 observations。設計時の生成済みHTMLにあった220,376件とは別のsnapshotなので、設計時の897 MiBを直接の比較元にはしていない。

旧形式も作業ツリーにあったtext log化・CSS描画遅延を含む。同じ保存結果を `WriteHTMLWithSources` と新しいCLIで描画して比較した。

Chrome headless 154.0.8037.98 / macOS / Apple M2 Pro / 1440×1000 / JS無効。各形式を新しいブラウザで5回開き、load到達時に測定。PlaywrightのBFCache無効化フラグを除去した。

| 項目 | 旧単一HTML（text log改善済み） | 分割版の入口 |
|---|---:|---:|
| HTML bytes | 63,328,326 | 5,621 |
| DOM nodes | 約571,346 | 212 |
| 最大renderer RSSの中央値 | 387.9 MiB | 176.5 MiB |
| load時間の中央値 | 595 ms | 16 ms |

RSSは専用Chrome内のrenderer群の最大瞬時値で、厳密なタブ帰属・ピーク・全ブラウザ合計ではない。Chromeの基礎メモリも含む。HTML/DOMの削減率をRSSの削減率として扱わない。

分割版で異なるevaluationページを100回連続閲覧した1試行では、10ページ後182.3 MiB、50ページ後292.8 MiB、100ページ後292.3 MiB、戻る/進む後295.3 MiBだった。遷移load時間のp95は21 ms、最大41 ms。履歴・キャッシュを含む使用量であり、無限回の遷移で一定であることを証明するものではない。

全13,532 HTMLページを検査した結果、最大ページは92,836 bytes・2,657要素だった。元の220,307観測が最初の条件列群に一度ずつ現れることも確認した。HTML総量は約130 MiBであり、全出力が小さくなる変更ではない。

生データは [implementation-measurements.json](html-report-memory-evidence/implementation-measurements.json)。同じ2出力に対する比較は、既存Playwrightが解決できる環境で次のように再実行できる。

```sh
node docs/html-report-memory-evidence/compare-memory.cjs \
  /absolute/path/to/single.html /absolute/path/to/paged/index.html
```

## 検証

- `go test ./...`。
- report/CLIのHTML・保存結果再描画・統合テストを `-race` で実行。
- `go vet ./...`。
- 451観測×35条件の表を両軸で分割し、全列の値、順序、出所、重複ID、aborted/not-availableを元modelと照合。
- 巨大な一行、1,200行以上のソース、日本語・CRLF・HTML特殊文字・改行/tab付きの長文reasonを復元して比較。
- 全生成リンクとfragment、全HTMLの容量/要素数、空入力、同一入力再生成、cancel、既存generation破損/symlink、旧generation保持を確認。
- package数を10から1,000に増やしてもoverview本文が同一であることを確認。
- JavaScript無効・外部requestゼロで、package→file→source移動、色モード選択、witness/evaluation表をChromeで確認。比較表が画面の下へ押し出されないことをスクリーンショットで確認。

入れ子annotationの色生成benchmarkは、1,000件で約353 KB/op・0.31 ms/op、10,000件で約3.53 MB/op・3.4–3.7 ms/op（各3回）。新しい色生成経路の結果であり、旧interval生成や解析全体の速度比較ではない。

## 利用上の変更と残る範囲

入口は従来どおり`coverage-html/index.html`。配布・移動にはディレクトリ全体が必要。ブラウザの検索・印刷は現在のページだけを対象とし、sourceモードは各ページでStatementから始まる。

全観測とwitnessを保持するため、出力総容量・file数は入力に比例して増える。古いgenerationを自動削除しないため、繰り返し生成時は保存容量も増える。軽くなったのは一度にブラウザへ読み込む量であり、保存結果のDecode・解析model全体の常駐量を一定にする変更ではない。

Firefox/Safari、スクリーンリーダー、長時間のリーク、OSディスクキャッシュを空にしたcold条件、100倍の実リポジトリ全体での計測は未実施。Windows固有の公開動作も新たに検証していない。
