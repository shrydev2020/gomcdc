# 初期HTMLメモリ診断の再現

2026-10-12、単一HTMLの構造とブラウザ使用量を調べた。`measurements.json`に元HTMLのdigest、HTML内訳、3ケースの測定値を保存している。対象はevaluationを表で表示する初期の生成物。

## 環境

Python 3、Node.js、Playwright、Chrome、macOSの`ps`を使う。スクリプトは`require('playwright')`でインストール先を解決する。必要に応じて`NODE_PATH`へnode_modulesの親を設定する。

`CHROME_EXECUTABLE`の既定値はmacOS標準のGoogle Chromeパス。ブラウザは一時プロファイルで起動する。

## 操作

リポジトリルートから、対象の単一HTMLと診断出力先を絶対パスで指定する。元HTMLは`measurements.json`のdigestで識別する。

```sh
python3 docs/html-report-memory-evidence/diagnostic-transforms.py \
  /absolute/path/to/original-single.html /tmp/gomcdc-html-diagnostic

node docs/html-report-memory-evidence/measure.cjs \
  /absolute/path/to/original-single.html interact \
  > /tmp/gomcdc-html-diagnostic/baseline.json

node docs/html-report-memory-evidence/measure.cjs \
  /tmp/gomcdc-html-diagnostic/text-log-approximation.html \
  > /tmp/gomcdc-html-diagnostic/text-log-metrics.json

node docs/html-report-memory-evidence/measure.cjs \
  /tmp/gomcdc-html-diagnostic/overview-only.html \
  > /tmp/gomcdc-html-diagnostic/overview-metrics.json
```

`interact`は最初のpackageへスクロールし、最初のDecisionを開く。

## 比較ケース

| 出力 | 調査内容 |
|---|---|
| 元HTML | 初期表示とDecision展開時の使用量 |
| `text-log-approximation.html` | 観測表をtab区切りテキストに変換し、画面外layoutを遅延した場合 |
| `overview-only.html` | summaryとpackage集計を抽出した場合 |
| `no-evaluations.html` | 観測部分を取り除いた補助比較用の生成物 |

変換処理はこの元HTMLの構造を対象とする。テキスト化では表の全行・セルをtab区切りにする。製品の分割版については[同一入力の測定結果](../html-report-scaling-results.ja.md)を参照する。

測定は各ケース1回。RSSは専用ブラウザのrendererプロセス群の最大瞬時値、`elapsedMs`はnavigation開始からloadまでを示す。
