# HTMLページ分割の測定結果

2026-10-12、ページ分割を導入したcommit `2992ecb`の測定。[設計](html-report-scaling-design.ja.md)と生データを合わせて参照できる。

## 条件

リポジトリ全体の保存結果から、単一HTMLと分割HTMLを生成した。入力は220,307観測。測定時の単一HTMLは`WriteHTMLWithSources`によるtext log表示、分割HTMLはCLIの`WriteHTMLDirectory`による出力。

Chrome headless 154.0.8037.98 / macOS / Apple M2 Pro / 1440×1000。JavaScriptを無効にし、各形式を新しいブラウザで5回開いてload時点に測定した。BFCacheはChromeの通常設定を使用し、OSのディスクキャッシュは維持した。

RSSは、そのブラウザに属するrendererプロセス群の最大瞬時値。ブラウザの基礎メモリとページ表示に使う領域を含む。load時間はnavigation開始からloadイベントまで。

## 比較

| 項目 | 単一HTML | 分割版の入口 |
|---|---:|---:|
| HTML bytes | 63,328,326 | 5,621 |
| DOM nodes | 約571,346 | 212 |
| 最大renderer RSSの中央値 | 387.9 MiB | 176.5 MiB |
| load時間の中央値 | 595 ms | 16 ms |

分割版の全13,532 HTMLページを検査した。最大ページは92,836 bytes・2,657要素、HTML総量は約130 MiB。最初の条件列群に元の220,307観測が一度ずつ現れ、他の列群も同じ観測番号を参照していた。

異なるevaluationページを100回連続閲覧した1試行では、10ページ後182.3 MiB、50ページ後292.8 MiB、100ページ後292.3 MiB、戻る/進む後295.3 MiBだった。遷移load時間はp95 21 ms、最大41 ms。

## 機能検証

- 全Goテスト、report/CLIの対象テストのrace検査、`go vet ./...`が成功。
- 451観測×35条件の分割表で、全列の値、順序、出所、重複ID、aborted状態を照合。
- 巨大な一行、1,200行以上のソース、日本語・CRLF・HTML特殊文字と長文reasonを復元して照合。
- 全生成リンクとfragment、ページ上限、空入力、再生成、cancel、既存世代の破損・symlink、旧世代保持を確認。
- package数を10から1,000に増やしたときのoverview本文の一致を確認。
- Chromeでpackage→file→sourceの移動、色モード、witness/evaluation表を操作し、スクリーンショットで視認性を確認。

色生成benchmarkは1,000件の入れ子注釈で約353 KB/op・0.31 ms/op、10,000件で約3.53 MB/op・3.4–3.7 ms/op（各3回）。

## 構造整理後の再検証

公開・ページ分割・表示を分離した版でも、同じ保存結果から13,532ページと220,307観測を再生成した。最大ページは92,904 bytes・2,657要素。Chromeでソース色とwitness表を確認した。

各形式5回の再測定で、renderer RSS中央値は単一HTML 360.1 MiB、分割版176.9 MiB。入口のDOMは242 nodes、load中央値は39 msだった。生データの`refactoringVerification`に記録した。

エラーの原因保持、重複ソース・欠けたID、不正な座標、ページ参照の不整合を回帰テストへ追加し、全Goテスト・対象race検査・静的検査が成功した。

## 対象情報と観測案内の追加後

2026-10-13、同じ220,307観測から13,532ページを生成した。最大ページは95,284 bytes・2,678要素。Chromeで指標名、詳細ページの対象情報、未達成条件の観測集計、対象条件を含む観測表への移動を確認した。JavaScript無効で実行し、ソースの色分けとwitness表も確認した。生データの`readabilityVerification`にページ検査と操作結果を記録した。RSSとload時間は前節の測定値を参照する。

## 再現

[implementation-measurements.json](https://github.com/shrydev2020/gomcdc/blob/a3c9484bf628c23974ced60f480d0d2237c59237/docs/html-report-memory-evidence/implementation-measurements.json)に測定時の入力digest、ページ検査、ブラウザ測定の生データを保存している。リンク先は測定時のコミットに固定している。入力のSHA-256は`7bc4416ce84679887a4de6192af514edf73ceef0b78990e6a9bc39ca507bec7e`。Playwrightが利用できる環境で、同じ2出力を次のように比較する。

```sh
node docs/html-report-memory-evidence/compare-memory.cjs \
  /absolute/path/to/single.html /absolute/path/to/paged/index.html
```

[初期調査の再現手順](https://github.com/shrydev2020/gomcdc/blob/a3c9484bf628c23974ced60f480d0d2237c59237/docs/html-report-memory-evidence/reproduce.md)には、ページ分割の実装前に行った原因調査を記録している。

## 運用条件

配布には出力ディレクトリ全体が必要。検索・印刷は現在のページを対象とし、ソース表示は各ページでStatementモードから始まる。観測数に応じて出力容量とファイル数が増え、繰り返し生成すると旧世代も保存される。

この測定の対象はChromeによる100ページまでの閲覧。Firefox/Safari、長時間閲覧、保存結果の読み込み・解析に使うGoメモリは別の測定対象となる。
