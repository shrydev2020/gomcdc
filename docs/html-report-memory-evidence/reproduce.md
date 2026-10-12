# HTMLメモリ診断の再現

これは2026-10-12の設計調査の証拠で、製品のHTML生成機能ではない。

`measurements.json` に元成果物のdigest、HTML内訳、3ケースのChrome/CDP/OS測定値を保存した。時刻ではなくdigestで同一成果物を確認すること。元成果物にはevaluation tablesがあり、作業ツリーにあるtext log化はまだ反映されていなかった。

## 前提

- Python 3、Node.js、Playwright、Chrome、macOSの`ps`。
- `measure.cjs` が `require('playwright')` で既存インストールを解決できること。必要なら既存node_modulesの親を `NODE_PATH` に設定する。新しい依存追加は不要。
- `CHROME_EXECUTABLE` は省略時にmacOS標準のGoogle Chromeパス。通常利用中のブラウザに接続せず、一時プロファイルを使用する。

## 操作

リポジトリルートから、調査対象と生成先を絶対パスで指定する。

```sh
python3 docs/html-report-memory-evidence/diagnostic-transforms.py \
  "$PWD/coverage-html/index.html" /tmp/gomcdc-html-diagnostic

node docs/html-report-memory-evidence/measure.cjs \
  "$PWD/coverage-html/index.html" interact > /tmp/gomcdc-html-diagnostic/baseline.json

node docs/html-report-memory-evidence/measure.cjs \
  /tmp/gomcdc-html-diagnostic/text-log-approximation.html \
  > /tmp/gomcdc-html-diagnostic/text-log-metrics.json

node docs/html-report-memory-evidence/measure.cjs \
  /tmp/gomcdc-html-diagnostic/overview-only.html \
  > /tmp/gomcdc-html-diagnostic/overview-metrics.json
```

`interact` は最初のpackageにスクロールし、最初のDecisionを開く。大量の全detailsを一度に開く操作ではない。原本は変更しない。

## 変換と測定の限界

- 変換スクリプトは調査対象の既知のHTML構造向け。一般的なHTML parserや製品rendererではない。
- 表の各行・各セルのテキストをtab区切りにし、画面外のlayoutを遅延するCSSを追加する。行は省かないが、現行製品のfield quotingやrun/package/process別列を正確に再現しない。
- 概要だけのページはsummaryとpackage集計を抽出したもの。全レポートの代わりに配布できるものではない。
- `no-evaluations.html` は補助的な削除比較用で、保存したブラウザ測定3ケースには含めていない。
- renderer RSSはプロセス群の瞬時値。設計書の数値はその最大であり、ページに厳密に帰属させた値でもpeakでもない。
- `elapsedMs` はfresh browserでのnavigation開始からloadまで。知覚される表示完了や入力応答時間と同義ではない。
- 本調査は各ケース1回。製品変更の合否判定ではcold/warm、繰り返し、連続遷移、他ブラウザの測定を追加する。
