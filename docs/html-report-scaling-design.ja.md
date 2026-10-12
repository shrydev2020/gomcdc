# HTML coverage report のメモリ診断と分割設計

2026-10-12の設計記録。実装した範囲と実測値は[実装結果](html-report-scaling-results.ja.md)を参照。以下の診断値は設計時点のもの。

## 結論

ブラウザの主な負担は、全観測・全ソース・全証拠を一つの HTML document に展開することにある。生成済みレポートでは、閉じた詳細も含めて約404万 DOM node が生成された。JavaScript heap は約0.6 MiBなので、JS heap だけを測ると問題を見落とす。

推奨は **容量・要素数に上限のある静的 HTML ページ群**への変更。概要から対象を選び、必要な範囲だけを document として読み込む。既存の色分け・意味論的な表・オフライン閲覧・JavaScript 不要を維持する。一つの package や file を一ページにするだけでは不十分で、巨大ソース・巨大 Decision・実行観測・索引にも分割を適用する。

変更する既存契約は D31 の「単一 self-contained HTML」。入口の `--format html --output <directory>` と `index.html` は維持し、配布単位をディレクトリ全体にする。HTML/CSS の相対参照だけで `file://` から閲覧でき、サーバー・外部通信・CDN は不要。これは既存仕様を黙って満たしたことにせず、実装時に英日仕様を改訂する。

## 1. 調査対象と測定結果

調査時の HEAD: `d65af18212e97107d18f320d497a1a9d470fcdeb`。未コミットの HTML 改善が存在したため、HEAD・作業ツリー・生成済み成果物を区別した。既存変更は編集していない。

- 生成済み `coverage-html/index.html`: 85,887,511 bytes、SHA-256 `830efdd0010ed5ca32ca3830d6f840e88a7b3f83ba1f1b9cfc64d28d15def5ed`。
- 作業ツリーの `internal/report/template/report.html`: SHA-256 `9d49902e55bbadc9a08bdc8f29a8d1fb5574d6d8403d8b53e366d6a14a0033c7`。
- 生成済み HTML は observations を表として保持し、`content-visibility` はない。作業ツリーは observations を `<pre>` に変更し、描画遅延 CSS を追加済み。**生成済み成果物はこの変更より前のもの**。
- 成果物は28 packages、70 files、533 functions、2,244 evaluation tables、220,376 observations、5,845 findings、2,337 witness tables を含む。

### 容量と要素数の内訳

| 項目 | HTML容量 | HTML要素数 | 解釈 |
|---|---:|---:|---|
| 全体 | 81.91 MiB | 2,374,851 | 調査対象の実ファイル |
| evaluation tables | 66.32 MiB | 2,075,248 | 容量の約81%、要素の約87% |
| findings | 4.24 MiB | 70,140 | 概要直下に全件存在 |
| witness tables | 2.86 MiB | 90,517 | 閉じた Decision の中も含む |
| source code blocks | 2.72 MiB | 7,020 | source spans は6,880個 |

source blocks の `title` 属性値だけで903,519 bytesあり、タグを除いたソース本文は escape 後784,595 bytes。今回の最大要因はソースではないが、tooltip 展開は別の増幅要因になる。上記の分類以外にも状態表示・階層・見出しなどがある。

### ブラウザ実測

macOS、専用の一時プロファイル、Chrome headless 154.0.8037.98、1440×1000、`file://`。各ケースでブラウザを起動し直した。CDP `Memory.getDOMCounters` / `Performance.getMetrics` と OS の RSS を測定。

| ケース | HTML容量 | DOM nodes | 最大 renderer RSS | load到達時間 |
|---|---:|---:|---:|---:|
| 生成済みレポートそのまま | 81.91 MiB | 4,045,143 | 897.0 MiB | 4,320 ms |
| 全観測をテキストログ化＋描画遅延した診断用変換 | 60.92 MiB | 569,107 | 368.5 MiB | 624 ms |
| 同じ概要・package集計のみを抜き出した診断ページ | 約0.03 MiB | 1,619 | 178.1 MiB | 50 ms |

RSS は専用ブラウザ内の renderer 群の最大値を load 直後に採取したもの。タブに厳密に帰属させた値、ピーク、全ブラウザ合計、反復測定の中央値ではない。Chrome 自体の基礎使用量、macOS のメモリ圧縮・回収も含むため、比率をそのまま保証値にしない。DOM nodes は text node 等を含み、HTML要素数とは異なる。

テキストログ版は既存 HTML の各表の行・セルを維持した診断用変換。現行テンプレートは run/package/process を独立列にするため、現行コードの正確な生成結果とは異なる。概要のみのケースもリンク先を備えた完成品ではない。後二者は原因の切り分けであり、分割版の完成・高速化保証を示さない。

元レポートは4,973個の details を閉じた状態でも約404万 nodes が存在した。最初の package と Decision を開く操作も確認した。長時間使用で増え続ける「リーク」は今回測定しておらず、確認できたのは巨大な初期常駐量である。

生の数値と計測手順は [測定データ](html-report-memory-evidence/measurements.json) と [再現手順](html-report-memory-evidence/reproduce.md) に保存した。

## 2. 原因と現在の改善の限界

### 2.1 初期documentに全内容がある

`internal/report/template/report.html:24,28–33` は全 Findings、全 Packages、全 Files、全 Functions、全 Conditions と実行観測を列挙する。`details` は表示を折りたたむが、今回の測定でも内容の DOM は作られた。

`content-visibility:auto` は画面外の layout/paint 等を省略する機能であり、HTML を別ファイルとして遅延読み込みする境界ではない。[CSS Containment の仕様](https://www.w3.org/TR/css-contain-2/#content-visibility) も描画処理の省略を規定する。DOM・文字列が入力総量に依存する問題は残る。

### 2.2 evaluationを表からpreに変えるだけでは文字列は残る

旧成果物の表は observation 数と condition 列数に比例して node を作る。作業中の text log 化はこの要因に有効。ただし全観測の文字列を最初に読み込む点は同じ。長大なログを開いた際のテキスト layout も別途負担になる。

`internal/report/html_log_test.go` の element 数一定テストは有用だが、入力が1万件から1億件に増えたときの text bytes や RSS を制限しない。

### 2.3 ファイル数以外の増加軸もある

- Findings とナビゲーションも全件常駐する。
- `htmlWitnessView` は一つの witness ごとに Decision の全 Conditions を行へ展開する。C個の condition に対して W個の witness があれば O(W×C)。両方が C に比例すれば二次的に増える。
- source の全注釈を全区間の tooltip に連結するので、重なりの多い注釈が繰り返される。
- 一つの巨大 package/file/Decision だけを含む入力でも壊れる。単純な「package別ページ」では対処できない。

### 2.4 Go側の生成メモリは別の責務

`html.go:45–49` は全 `sourceView` と全 `htmlFindings` を作ってからテンプレートを実行する。`source.go:291–295` は各 interval に active annotation のポインタ配列を作る。構造体コピー削減後も、重なり参照の総数 K に比例し、病的な入れ子では K=O(A²) が残る。

さらに offline 経路の `resultfile.Result.SourceFiles` は元ソースを clone する。これらはブラウザ RSS の直接原因ではないが、巨大入力での生成時ピークに関わる。ブラウザの改善と生成時追加メモリを別々に計測する。

## 3. 推奨構成：読む単位と読み込む単位を揃える

| ページ | そのページに読み込むもの | 読み込まないもの |
|---|---|---|
| Overview | 全体11指標、run status、状態別件数、上限内のpackage索引 | 全source、全findings、全evaluations |
| Package / File索引 | 現在の階層と一定件数の子要素・集計 | 子の本文・証拠 |
| Findings | 選択範囲の一定件数、metric・status・理由・正確な位置 | 全義務一覧 |
| Source | 上限内の連続したソース範囲、6種類の色分け | ファイル全体や全annotation説明 |
| Decision / Condition | 対象の状態、短いソース文脈、選択したconditionの証拠 | 他の全conditionのwitness・全ログ |
| Witness | A/B比較、実測とcompletionの別列、出所、一定件数のcondition行 | 他のwitness |
| Evaluations | 対象Decisionの一定範囲の元観測と列見出し | 全Decision・全観測 |
| Producers / Diagnostics | producer状態と一定件数の診断・元run | 全入力runの巨大詳細 |

すべて通常のリンクで遷移する。左は現在位置と周辺項目、上は指標とパンくず、本文はソースまたは証拠という現在の見方を維持する。全 package tree を全ページへ複製しない。ページ索引自体も上限を超えたら階層化し、何万ページ分のリンクを一ページへ置かない。

### 3.1 上限は件数だけでなく容量と要素数で決める

以下は初期の設計予算で、達成済みの性能値ではない。

- 1 HTML document: **1 MiB以下、5,000 HTML要素以下**。本文・属性・ナビゲーション・CSS・escape後の増加を含める。
- 通常の索引・findings: 100件を目安に、上記予算に達する前に次ページへ送る。
- ソース: 400行を目安に同じ予算で分割。巨大な一行は元の行番号・byte範囲付きの継続区間に分ける。
- evaluations: 200観測を目安に分割。condition が多い場合は行と列の両軸で分割し、evaluation identity と出所を各区画に残す。
- witness: 1対象condition・1strategy を読む単位とし、比較行も予算で分割。対象conditionの行、A/BのDecision result、出所は各ページから必ず分かるようにする。
- 異常に長い expression / test name / reason は識別情報と全文へのリンクを示し、全文も継続ページで読めるようにする。省略表示をデータ削除にしない。ブラウザに渡す直前のシリアライズ結果で最終予算を検証する。

これで「ブラウザが読む document のデータ量」は全リポジトリサイズから切り離せる。ただしブラウザの履歴・キャッシュ・複数タブ・OS管理まで絶対上限を保証するものではない。戻る／進むを含む連続閲覧の実測を合格条件にする。

ディスク出力の総量は別で、全観測を保持する以上、観測数に比例する。全witness比較を静的生成する場合は総出力に O(W×C) が残り得る。ページ分割で総出力まで一定になるとは主張しない。出力bytes・file数・生成時間も測り、それ自体が制約になる入力では、証拠を要求時に組み立てるviewerへの契約変更を次の判断点とする。

### 3.2 表示を簡略化せず、読み込み範囲を制御する

- Statement / Decision / Condition / MC/DC / Clause / Combined の6モードと現行の色の意味を維持する。選択しているモードは常に文字で明示する。
- 分割source間の通常リンクではCSS radio状態は引き継げない。JS不要を守る設計では、次ページの初期modeを明示し、ブラウザの暗黙の状態復元には依存しない。モード引継ぎを必須にする場合は、小さなJS補助またはmode別出力が別途必要になる。
- 数値、分子/分母、percentage、status text は残す。色だけで判定させない。
- Findings → 対象source範囲 → condition証拠、を直接リンクで移動できるようにする。証拠ページには短いsource文脈を添え、巨大ファイルを開かなくてもA/Bと式を比較できる。
- MC/DC witness は意味論的なtableとして残す。Condition occurrence番号、target/masked/unobserved、not-evaluated、実測・completion assignmentを区別する。
- unknown / unsupported / infeasible / analysis-incomplete / partial run は missing coverage と統合しない。producer integrity/completeness/mapping/usabilityも独立させる。
- observations は重複を含む元順序・件数を保持する。評価IDだけで同一視せず、run/package/process/Decisionの文脈を含める。全件読めるページ列を用意し、「代表数件だけ」を最適化にしない。
- ページ番号だけでなく「220,376件中201–400件」「元ソース401–800行」を表示。前後移動・先頭・親階層へのリンク、表見出しの再表示、キーボード操作を備える。
- 小さなページでは仮想スクロールを必須にしない。通常のテキスト選択・コピー・読み上げを維持する。完全な元ソースをコピーする用途には、埋め込みスナップショットから生成した元ファイルへのリンクを用意する。

**明示するトレードオフ:** ブラウザのページ内検索と印刷は現在のページに限定される。単一HTMLの全文検索と等価ではない。package/file/function と metric/status の有界な索引で対象をたどれるようにするが、任意語の全レポート検索は代替できない。これを必須とする場合は、別途検索インデックスを分割して読むJS viewer、またはローカルサーバーが必要。単一ファイル・JS不要・全文検索・全規模で有界メモリを同時に約束しない。

### 3.3 ソース注釈の重なり増幅を取り除く

色の決定と注釈詳細の列挙を分離する。

1. ファイル内の annotation を開始/終了byte位置のイベントにする。
2. sweep 中は metric×state の active件数を維持し、各区間へ固定数の色状態を出す。各区間に全active annotation配列を保存しない。
3. 長文tooltipを区間ごとに連結しない。tooltipは短い状態表示とし、正確な対象・出所・詳細はannotation索引の該当箇所に表示する。キーボードでも開けるリンクを用意する。
4. annotation索引は注釈を一度ずつ格納し、開始位置と範囲で探せる分割ページを作る。ページごとの全重なりリストも大量複製しない。既存注釈の範囲と対象identityから証拠へリンクする。
5. 指標別のseverity優先順位は既存 `htmlSourceSeverity` と同じ。Combined表示と個別modeを混同しない。

色区間生成は A annotations / I intervals に対し概ね O(A log A + I×固定指標数)、追加メモリはイベントと現在のページに比例する設計にする。全重なり組合せを出力する O(K) を色付けの必須処理から除く。注釈詳細を全件読む総コストそのものは消さない。

Goのoffsetはbyte単位として維持する。日本語、CRLF、tab、範囲がページ境界を跨ぐ場合、mapping失敗を検証し、ブラウザの文字indexと取り違えない。元ソースがない場合は明示し、現在のcheckoutを読み直して別のソースで補わない。

## 4. 生成・出力の責務

`analysis.Result` と保存済みsourceが表示根拠である。表示のためにテスト・計測・MC/DC解析・policy判定をやり直さない。

- `WriteHTMLWithSources(io.Writer, ...)` は一documentの契約であり、複数fileを裏で作る意味に変更しない。
- directory出力用に `WriteHTMLDirectory` 相当の具体的な入口を設け、CLIのHTML経路を移す。既存関数の廃止または移行期間は内部呼び出し元・テストを確認して決める。巨大な単一HTMLへの暗黙fallbackは行わない。
- ファイル単位のsource注釈生成、findingsの逐次列挙、bounded pageのシリアライズを行う。全 `htmlReport`・全表示用 `Findings`・全出力文字列を同時に作らない。
- ページIDはpackage/path/entity identityから決定し、入力の列挙順序に依存させない。source範囲とevaluation identityから該当ページを求める小さな索引を生成する。巨大manifestをindex.htmlへ埋め込まない。
- 同じ入力・同じ予算・同じrenderer versionで同じ本文とページ分割になるようにする。古い `#pkg-0...` の外部bookmark互換までは自動的に得られない。レポート内リンクはすべて新しい安定IDに統一する。
- 表示用データ・ページID・分割予算は公開のanalysis JSON schemaへ混ぜない。

**生成メモリの範囲:** 現行CLIは `analysis.Result` や保存結果全体を読み込む。この設計で制限するのはHTML表示用の追加メモリとブラウザへの入力であり、計測・解析・saved-result Decode全体を一定メモリにするものではない。元ソースcloneの除去を行うなら、rendererが読み取り専用の参照を使うことを明示して実施する。

### 複数file公開を途中で壊さない

現行 `internal/cli/cli.go:884–927` は一時fileから `index.html` へのrenameで公開する。ページ群ではindexだけ先に置換すると、新旧ページが混ざる。

1. 出力先配下の新しい専用generation directoryへ全ページを生成する。利用者の既存fileを上書きしない。
2. ページ予算とリンクの存在を検証し、write/closeエラーを確認する。
3. 完成したgenerationを参照する入口を最後に一時fileから `index.html` へ置換する。
4. 失敗時は新しい未公開generationだけを削除し、旧indexと旧generationを残す。前に開いたページからの遷移も旧generation内で完結する。
5. 古いgenerationは描画処理から勝手に削除しない。保存容量と既存タブのリンク寿命のトレードオフを明示し、削除は別の明示的な操作にする。

公開generation名は入力内容・renderer version・ページ予算から決定し、一時directory名だけをランダムにする。同一generationが既に存在する場合は完全性を検証して再利用するか、明示的に失敗する。既存の公開ページ群へ上書き追記しない。

保存入力と出力の同一性検査は入口だけでなく実際に書くパスへ適用し、symlink/hardlink保護を維持する。生成先はuser pathをそのままfile名へ使わず、衝突を検査した内部IDにする。

## 5. 採用しない対処

| 対処 | 採用しない理由 |
|---|---|
| detailsを閉じる、display:none、content-visibilityだけ | 全内容の読み込み境界を作らない |
| 全JSONをscript内に埋めてDOMだけ仮想化 | データ文字列・parse後オブジェクトが総量に比例する |
| 巨大HTMLを圧縮・minifyするだけ | 展開後の文字列とDOMを制限しない |
| package/file単位だけの分割 | 一つの巨大file・Decision・ログで再発する |
| ログやwitnessを上位N件だけ残す | 証拠を失う |
| 色付け・比較表を撤去する | 読みやすさを失う |
| 全文検索のために全sourceを常駐させる | 初期メモリの問題を検索層へ移す |

単一HTMLの配布が必須なら、JSと外部データ選択などの契約を別途検討する。通常の自己完結HTMLへ全データを埋め込んだまま、読み込み量が一定になるとは説明しない。

## 6. 実装順序と合格条件

### 実装順序

1. 実際の現行rendererで比較用レポートを生成し、保存結果・ソース・テンプレートのdigestを固定する。既存の生成済みHTMLと取り違えない。
2. bounded page生成と複数fileの公開処理を実装し、Overview / 階層索引 / Findingsを分割する。全詳細を裏で埋め込まない。
3. source範囲、condition/witness、evaluations、producers/diagnosticsを分割し、ソース注釈生成から重なりの全組合せ保存を除く。
4. 保存結果からの再描画、出所・意味・総件数の保存、リンク・アクセシビリティ、巨大入力の実測を通す。
5. D31英日仕様と既存HTMLテストを更新する。内容保存と有界な読み込みを検証するテストにし、「すべてがindex.htmlにある」という前提は廃止する。

text log化は有効な先行改善として扱えるが、それだけで巨大リポジトリ対応完了にはしない。新しいCIやrelease処理の追加はこの設計の前提にしない。

### 検証行列

| 増加軸 | 入力例 | 検証すること |
|---|---|---|
| package/file/function数 | 同じ規模の対象を1×/10×/100× | 初期documentと索引の容量・要素数が予算内 |
| observations数 | 一Decisionに1万/10万/100万件 | 初期ロードと1ログページが総観測数に比例しない |
| 巨大source | 一file・一行が予算を超える入力 | 分割・copy・元byte位置・境界色を保持 |
| conditions/witness数 | 一Decisionの多数condition | 行・列分割、A/B、target、出所が保たれる |
| annotation重なり | 同数の離散/深い入れ子注釈 | 色生成でO(A²)の参照/tooltip列挙をしない |
| metadata文字列 | 長いtest name/reason、日本語、改行/tab | escape後も予算内、全文に到達できる |
| 閲覧履歴 | 100回の対象切替、戻る/進む、複数mode | RSS、DOM、応答時間が際限なく増えないか |
| 失敗 | 書込失敗、リンク切れ、cancel、同一path | 旧出力を壊さず、不完全版を成功公開しない |

必須の機能検証:

- 全ページ合計で元observationsの件数・値・順序・provenanceが一致する。重複と未完了観測も消えない。
- witness A/Bとcompletionの値を元analysisと比較する。ページ区切りが条件の意味を変えない。
- ソース本文を範囲順に復元すると元bytesと一致する。mapping unknownは色で偽装しない。
- 全生成リンクの参照先・fragmentが存在し、深いリンクを直接開いて対象が見える。
- Chrome / Firefox / Safariの `file://`、JS無効、ネット接続なしで主要導線が使える。未測定ブラウザを対応確認済みにしない。
- 色モード、表の列名、凡例、未達と判定不能の別表示をスクリーンショットとキーボード操作で確認する。

性能の仮合格値は、固定端末で初期表示1秒以内・ページ遷移p95が200ms以内を目指す。実装前に端末・cold/warm条件を固定し、最低5回測定する。renderer RSSは空ページとの差分と最大値、DOM/text量、生成Go heap/allocations、出力容量を別々に報告する。1×から100×に増やしても同じページの読み込み量が増えないことをまず必須条件とし、絶対RSS値の保証と混同しない。

## Semantic Boundary Review

- Loaded terms: analysis.Result、source annotation、Findings、page、generation、index。
- Vague terms rejected: 全部を所有する `ReportManager`、意味の曖昧な `DataStore`、無制限に保持する `cache` は導入しない。
- Accepted abstractions and their invariants: analysisは結果の根拠、Findingsと色はその表示上の派生、pageは読み込み予算を守る単位、generationは一式が完成してから公開する単位。indexは参照先を示し、coverageを再判定しない。
- Responsibility theft risks: rendererによる再解析、paginationによる観測削除、色状態とcoverage statusの混同、現在のcheckoutでのsource補完を禁止する。
- Required boundary change: 一documentのwriterとdirectory出力を分離する。sourceの色区間生成と詳細注釈の列挙を分離する。公開単位をindex単体から完成済みgenerationへ変更する。

## 今回確認していないこと

現行作業ツリーから同じ入力を再生成した性能、分割版の実装・RSS、長時間のリーク、Firefox/Safari、巨大リポジトリ100倍での挙動、生成Go heapの新しい実測は未確認。今回の成果は既存HTMLの原因特定、制御比較、およびこれらを検証できる設計である。
