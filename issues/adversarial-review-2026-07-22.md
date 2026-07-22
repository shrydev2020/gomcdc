# gomcdc 敵対的仕様レビュー（2026-07-22）

> 以下の Findings は修正前の状態を記録したものである。現在の解消内容と証拠は
> 「修正結果」「再判定」「検証結果」を正とする。

## 修正結果

初回7件の後にもD26の境界に局所修正が残っていた。とくにsource上でpackage loadを
行ってから別workspaceを作る二段構造は、正当なmodule解決更新と任意cwdの写像を
表現できなかった。現在の総合判定は後述の「追加再レビュー」と「再判定」を正とする。

| Finding | 本質的な対応 | 回帰証拠 |
| --- | --- | --- |
| workspace外symlink/hardlink | source treeはcopy-by-valueとし、symlinkの最終到達先をsource境界内へ限定してcopy側へ再配置する。通常fileとhardlinkは新規inodeへ複製し、外部到達linkはworkspace作成自体を拒否する。 | `TestCreateCopiesModuleTree`、`TestCreateRejectsSymlinkThatResolvesOutsideSourceTree` |
| 単一-module `go.work` | source configurationからrequest-owned workspaceをpackage load前に一度だけ作る。go.work、module、呼び出し位置の相対topologyを保持し、解析とtestを同じcwdと設定上で実行する。複数main moduleだけを拒否する。 | `TestDiscoverSnapshotsSingleModuleWorkspace`、`TestCreatePreservesWorkspaceModuleAndInvocationTopology`、`TestSingleModuleGoWorkSettingsApplyToAnalysisAndTest/workspace-subdirectory` |
| READMEの禁止flag | 転送例を非競合の`-run`だけにし、日英README内の同一例を実際のCLI parserと競合flag判定へ通す。 | `TestREADMEForwardedGoTestExampleUsesOnlyAllowedFlags` |
| v1 binaryとv2文書の混同 | module pathをsemantic import versioningに従う`github.com/shrydev2020/gomcdc/v2`へ移行し、日英READMEを`@v2.0.0`の固定installへ更新した。 | `TestREADMEInstallsVersionedV2Module` |
| test前割り込みの結果軸 | test、measurement、integrityの結果はreport組立時にboolから推測せず、measurementが記録した実行事実から一度だけ生成する。未開始testと未開始integrityは`not-run`のまま保持する。 | `TestMeasurementResultsPreserveIndependentExecutionFacts`、`TestAssembleReportInputClassifiesCallerInterruption` |
| cleanupとreport公開順 | workspace finalizationをexactly-onceの状態遷移にし、通常経路ではreport構築前に確定する。失敗は`measurement=failed`と`workspace-cleanup-failed`へ格納してからreportを公開し、早期returnだけdeferで回収する。 | `TestCleanupFailureIsPublishedInReportBeforeExit`、`TestAssembleReportInputRecordsCleanupAsMeasurementFailure` |
| coverage alias | `--coverage`をD27のtokenとの完全一致grammarとし、大小文字、前後空白、空tokenをCLI errorにする。 | `TestParseCoverageCanonicalNames`、`TestParseCoverageRejectsUnknownAndEmpty` |
| 代替modfile | effectiveな`-modfile`選択を明示引数優先で確定してrequest-owned configurationへ再配置する。`-mod=mod`による`.mod`/`.sum`更新はその領域で完了し、sourceを不変と仮定する事後検査は行わない。 | `TestDiscoverFreezesAndRelocatesAlternateModFileAndSum`、`TestCreateMaterializesAlternateModFileAndSum`、`TestAlternateModFileModModeResolvesOnlyInsideRequestWorkspace` |
| go.work任意位置起動 | workspace rootだけを特別扱いせず、go.work、module、呼び出し位置の共通rootから各相対位置をrequest workspaceへ写像する。 | `TestCreatePreservesWorkspaceModuleAndInvocationTopology`、`TestSingleModuleGoWorkSettingsApplyToAnalysisAndTest/workspace-subdirectory` |

## 追加再レビュー

### [P1] go.work配下の起動位置をrootそのものに限定していた

- **原因:** loaderがpackage load後にcwdをmodule基準または`workingDir == workspaceRoot`の二択へ分類していた。workspace作成は常にmoduleを固定位置へ置くため、それ以外の相対位置を表現できなかった。
- **解消:** cwd分類をloaderから削除した。package load前にsource go.work、main module、呼び出し位置の相対topologyをrequest-owned workspaceへ写像し、loaderと`go test`へ同じmapped cwdを渡す。
- **証拠:** `TestCreatePreservesWorkspaceModuleAndInvocationTopology`と`TestSingleModuleGoWorkSettingsApplyToAnalysisAndTest/workspace-subdirectory`は、workspace内の別directoryから相対patternでmain moduleを指定する。

### [P1] -mod=modによる正当なmodule設定更新を競合として拒否していた

- **原因:** source設定をfreezeした直後にsource tree上でpackage loadを実行し、その後のbyte差分を外部変更とみなしていた。このモデルではGo command自身が生成するmissing alternate `.sum`を表現できない。
- **解消:** package loadより前にmodule tree、go.work/go.work.sum、alternate `.mod`/`.sum`をrequest-owned workspaceへ配置した。module解決、source分析、計装、testは同じworkspaceを使い、`AssertSourceUnchanged`と二つ目のcopyを削除した。
- **証拠:** `TestAlternateModFileModModeResolvesOnlyInsideRequestWorkspace`はlocal module proxyから実際にsumを解決し、実行後もsource alternate `.sum`が存在せず、primary/alternate `.mod`の内容が不変であることを確認する。

## 第一次再レビュー結果（後に不十分と判明）

以下は第一次再レビュー時の判断を履歴として残す。この時点の対応はsource package
loadとtest workspaceの二段構造を温存しており、後述の追加再レビューで不十分と判明した。

### [P1] 代替modfileがmodule設定のauthorityに含まれていない

- **場所:** [`internal/loader/flags.go:49-59`](../internal/loader/flags.go#L49-L59)、[`internal/modulecontext/context.go:18-27`](../internal/modulecontext/context.go#L18-L27)、[`internal/modulecontext/context.go:145-195`](../internal/modulecontext/context.go#L145-L195)、[`internal/workspace/workspace.go:100-110`](../internal/workspace/workspace.go#L100-L110)
- **違反する要件:** D26は、`--`以降の非競合引数を解析と全runへ同じ意味で適用し、解析とtestで同じmodule設定を使用するよう要求している（[`docs/specification.ja.md:245-247`](../docs/specification.ja.md#L245-L247)）。
- **観察:** `-modfile`はbuild flagとして受理されるが、freeze、変更検出、作業用workspaceへの再配置は通常の`go.mod`と`go.work`だけを対象とする。`GOFLAGS`から指定した場合も同じauthority外になる。
- **外部影響:** 解析時に有効だった代替modfileの相対module設定が、作業用workspaceのtestでは別の意味になり得る。通常の`go test`が成功する構成でも、gomcdc側だけがtest failureを返す。
- **必要な修正条件:** effective module configurationのauthorityに、選択された代替modfileと対応するsum fileを含める。解析とtestの双方が、同じsnapshotから検証・再配置された設定と選択情報だけを使用する。
- **当時の対応（不十分）:** primary `go.mod`、代替`.mod`、対応`.sum`をfreezeし、source package load前後の変更を競合として拒否した。このためmissing `.sum`を生成する正当な`-mod=mod`も拒否した。

### [P1] 単一-module go.workをworkspace rootから利用できない

- **場所:** [`internal/loader/loader.go:101-103`](../internal/loader/loader.go#L101-L103)、[`internal/loader/loader.go:137-147`](../internal/loader/loader.go#L137-L147)、[`internal/cli/integration_test.go:77-97`](../internal/cli/integration_test.go#L77-L97)
- **違反する要件:** D26が対象外とするのはactiveな複数main moduleを持つ`go.work`であり、単一main moduleの正当なpackage patternをmodule内起動に限定する要件はない（[`docs/specification.ja.md:245-247`](../docs/specification.ja.md#L245-L247)）。
- **観察:** package loadが単一main moduleを正しく選択した後も、呼び出し元directoryがmodule root外なら拒否する。このため、go.work rootから配下moduleのpackage patternを指定する標準的な呼び出しを処理できない。既存integration testはmodule directory内からの起動だけを検証している。
- **外部影響:** 同じgo.workとpackage patternで通常の`go test`が成功しても、gomcdcはpackage load failureとして終了する。
- **必要な修正条件:** source moduleの所有範囲と呼び出し元directoryの意味を分離し、元のgo.work rootに対する呼び出し位置を作業用workspaceへ写像する。module内起動とworkspace root起動の双方をintegration testで固定する。
- **当時の対応（不十分）:** module内起動とworkspace root起動を二つの分類で写像した。workspace内のそれ以外のdirectoryを仕様根拠なく拒否した。

## Findings

### [P0] 作業用コピーが外部を指すシンボリックリンクをそのまま再作成する

- **場所:** [`internal/workspace/workspace.go:304-312`](../internal/workspace/workspace.go#L304-L312)、[`internal/workspace/workspace_test.go:23-85`](../internal/workspace/workspace_test.go#L23-L85)
- **違反する要件:** D32 は、source 複製時に symlink/hardlink を介して workspace 外へ書き込まないことを要求している（[`docs/specification.ja.md:309-315`](../docs/specification.ja.md#L309-L315)）。
- **観察:** コピー処理はリンク先が module 内に収まるかを検証せず、元のリンク文字列をそのまま作業用コピーへ再作成する。既存テストも「リンクを保存すること」だけを固定しており、リンク先の境界を検証していない。
- **外部影響:** 作業用コピー内で行われる build/test の書き込みが、作業用コピー外の状態へ到達し得る。これは「一時 workspace 内だけを変更する」という分離条件を満たさない。
- **最小修正:** source root から外れるリンクを拒否するか、安全な module 内リンクだけをコピー内の対象へ解決し直す。回帰テストでは、コピー後のいかなる書き込みも workspace root 外を変更しないことを検証する。
- **記載方針:** 影響と是正条件だけを示し、具体的な再現手順や利用方法は記載しない。

### [P1] 単一 main module の `go.work` まで一律に拒否している

- **場所:** [`internal/loader/loader.go:160-171`](../internal/loader/loader.go#L160-L171)、[`internal/loader/loader_test.go:63-75`](../internal/loader/loader_test.go#L63-L75)、[`internal/cli/measurement.go:166-170`](../internal/cli/measurement.go#L166-L170)
- **違反する要件:** D26 が対象外としているのは「active な複数 main module を持つ `go.work`」だけであり、解析と test には同じ module 設定を使う必要がある（[`docs/specification.ja.md:245-247`](../docs/specification.ja.md#L245-L247)）。
- **観察:** loader は active な `go.work` が存在するだけで失敗する。拒否テストの fixture は名前に反して `use ./module` 一件だけであり、仕様上許される単一 module の拒否を正解として固定している。measurement も常に `GOWORK=off` を設定する。
- **外部影響:** 有効な単一-module workspace で CLI が終了コード 2 となり、workspace 由来の replace/use 設定を解析と test に同じ意味で適用できない。
- **最小修正:** `go.work` の active main module 数を調べ、複数の場合だけ明示的に拒否する。単一の場合は、その module 設定を作業用コピーへ安全に反映し、解析と test の両方で同じ設定を使う。既存テストは単一-module 成功と複数-module 拒否へ分割する。

### [P1] README の公式例が CLI 自身の禁止 flag を渡して失敗する

- **場所:** [`README.ja.md:137-138`](../README.ja.md#L137-L138)、[`README.md:143-145`](../README.md#L143-L145)、[`internal/cli/cli.go:562-588`](../internal/cli/cli.go#L562-L588)
- **違反する要件:** D26 はユーザーまたは `GOFLAGS` から渡された `-count` を CLI error と定めている（[`docs/specification.ja.md:245-247`](../docs/specification.ja.md#L245-L247)）。
- **観察:** README は `gomcdc test ./... -- -count=1 -run TestCritical` を正規の転送例として掲載しているが、実装は `-count` を検出して拒否する。この拒否は既存テストでも期待動作として固定されている。
- **外部影響:** インストール直後の利用者が文書どおりに実行すると、計測ではなく invalid CLI usage（終了コード 4）になる。
- **最小修正:** 日英 README の例から `-count=1` を除き、非競合引数だけを示す。README 内のコマンド例を実際の CLI parser に通す軽量テストを追加する。

### [P1] インストール手順が v2 文書と互換性のない v1.0.1 を固定している

- **場所:** [`README.ja.md:29-36`](../README.ja.md#L29-L36)、[`README.md:30-37`](../README.md#L30-L37)、[`README.ja.md:87-91`](../README.ja.md#L87-L91)
- **違反する要件:** 現在の規範仕様と README は仕様/report schema 2.0 を公開契約としている（[`docs/specification.ja.md:1-5`](../docs/specification.ja.md#L1-L5)、[`docs/specification.ja.md:285-289`](../docs/specification.ja.md#L285-L289)）。
- **観察:** 既定の install command は `@v1.0.1` を固定する。repository 内の同 tag は specification/schema 1.0 であり、現在の tag 一覧にも v2 release はなく、最新 tag `v1.1.2` の report schema は 1.1 である。
- **外部影響:** README の手順で導入した binary は、その直後に同じ README が説明する schema 2.0、producer outcome、現行 CLI 契約を提供しない。文書と配布物を同時に満たせない。
- **最小修正:** v2.0.0 を正式に tag/release して install command をその版へ更新するか、v2 を未リリースとして明示し、既定 README を実際に配布中の v1 契約へ戻す。単に `v1.1.2` へ更新しても v2 との不一致は解消しない。

### [P1] test 開始前の割り込みを「test failed / integrity passed」と報告する

- **場所:** [`internal/cli/report_input.go:47-98`](../internal/cli/report_input.go#L47-L98)、[`internal/cli/report_input_test.go:51-63`](../internal/cli/report_input_test.go#L51-L63)
- **違反する要件:** D28 は test、measurement、integrity、strict、threshold を独立 field とし、未実行状態には `not-run` を用いる（[`docs/specification.ja.md:269-283`](../docs/specification.ja.md#L269-L283)）。
- **観察:** `testResult == nil` でも `interrupted` が真なら overall status を failed に上書きし、そこから `run.results.test=failed` を生成する。一方、integrity failure が記録されていないだけで `run.results.integrity=passed` になる。既存 unit test は test 未実行の fixture で `failed` を期待し、この混同を固定している。
- **外部影響:** 自動処理は「test が実際に失敗した」と「test は始まっていない」を区別できず、実施していない integrity 検証を成功と誤認する。部分 report の意味が終了コード 130/143 と整合しない。
- **最小修正:** test result が存在しない場合は test を `not-run` とする。要求 producer の収集・検証が始まっていない場合は integrity も `not-run` とし、実行済みの軸だけを実結果で埋める。overall の `failureKind=interrupted` と signal 由来の終了コードは維持する。

### [P2] cleanup 失敗を report 公開後に判定するため、成果物が終了結果を説明できない

- **場所:** [`internal/cli/cli.go:265-270`](../internal/cli/cli.go#L265-L270)、[`internal/cli/cli.go:281-315`](../internal/cli/cli.go#L281-L315)、[`internal/cli/measurement.go:68-75`](../internal/cli/measurement.go#L68-L75)
- **違反する要件:** D28 は終了コード 2 を measurement、instrumentation、integrity、report failure に対応させ、overall result の各軸を別 field で保持する（[`docs/specification.ja.md:269-283`](../docs/specification.ja.md#L269-L283)）。cleanup を新しい未報告の失敗分類として追加する authority はない。
- **観察:** 正常系の cleanup は defer され、report 書き込み後に実行される。cleanup が失敗すると process は終了コード 2 へ変わるが、すでに出力済みの report の `run.results` と `errors` は成功のままである。
- **外部影響:** CI の終了コードと保存された JSON/HTML/text が互いに矛盾し、report だけを受け取る consumer は失敗理由を復元できない。
- **最小修正:** 通常 cleanup を report の最終公開前に実行し、失敗を適切な `run.results` 軸と `errors` へ格納する。defer は予期しない早期 return に対する best-effort fallback に限定する。

### [P2] `--coverage` が未定義の大文字・空白付き alias を黙って受理する

- **場所:** [`internal/config/metrics.go:53-63`](../internal/config/metrics.go#L53-L63)
- **違反する要件:** D27 は正式値を小文字の11指標と `all` に限定し、適合条件 9 は指標 alias の公開を禁じている（[`docs/specification.ja.md:251-260`](../docs/specification.ja.md#L251-L260)、[`docs/specification.ja.md:321-330`](../docs/specification.ja.md#L321-L330)）。
- **観察:** parser は各 token に `TrimSpace` と `ToLower` を適用してから照合するため、正式値ではない大小文字や前後空白の表記も同じ指標として受理する。この互換動作を許可する仕様記述はない。
- **外部影響:** 無効入力が成功し、CLI の canonical token 境界が曖昧になる。後から厳密化すると、利用者にとって互換性破壊になる。
- **最小修正:** token を正式値へ完全一致させ、非 canonical 表記は終了コード 4 で拒否する。もし空白正規化を意図するなら、先に D27 へ明示的な grammar として追加する。

## 再判定

**本レビューで確認したP0/P1/P2 findingはすべて解消済みと再判定する。**
workspace分離、終了結果の独立軸、公開README、CLI grammarへの修正を維持し、
module解決のwrite authorityをrequest-owned workspaceへ限定した。go.work配下の起動位置は
root特例ではなく相対topologyとして扱い、alternate modfileの正当な更新も同じ領域で行う。
D26について、解析とtestのmodule設定および起動位置に既知の未解消差分はない。

release準備ではmodule pathを`github.com/shrydev2020/gomcdc/v2`へ移行し、READMEの
install commandを`@v2.0.0`へ固定した。`v2.0.0` tagはmasterのCI成功後に同じcommitへ
付与する。

## Authority と調査範囲

- 規範 authority は [`docs/specification.ja.md`](../docs/specification.ja.md) とした。英語版は参考訳として扱った。
- 公開境界として日英 README、CLI parser、report model/schema、workspace/loader/measurement 実装、対応する unit/integration test、repository 内の release tag を確認した。
- issue 文書や将来案は、規範仕様を変更する authority として扱っていない。
- 外部 release server の状態や未取得 tag は調査対象外である。ただし、README が固定する `v1.0.1` 自体の不一致は repository 内の tag 内容だけで確定する。
- 分離要件については使い捨て fixture で境界挙動を確認したが、本書には具体的な再現方法を残していない。

## 検証結果

以下は修正後のworktreeで成功した。

- `go test -count=1 ./...`
- `go test -race -count=1 ./...`
- `go vet ./...`
- workspace外link拒否、absolute internal link再配置、hardlink copy-by-valueの回帰test
- 単一-module `go.work`でworkspace-level replaceを解析とtestの両方へ適用するintegration test
- 複数main moduleの明示拒否test
- test開始前割り込みの`test=not-run`、`integrity=not-run` test
- cleanup failureをreport公開前に`measurement=failed`と`errors`へ反映するintegration test
- 日英READMEの転送例をCLI parserへ通すtest
- canonical coverage tokenだけを受理するparser test
- 明示`-modfile`、`GOFLAGS`、両方指定時の明示優先を解析とtestの統合経路で確認するtest
- 代替modfileと対応sum fileのrequest-owned再配置、local replacement再配置test
- missing alternate sumを`-mod=mod`で実生成し、source設定が不変であることを確認するintegration test
- 単一-module `go.work`をmodule directory、workspace root、workspace内の別directoryから実行するintegration test
- CIと同じself-MC/DC report生成およびcritical-package baseline判定

追加再レビューでは、source package loadとtest workspaceを分離する旧二段構造を廃止した。
全suiteだけでは検出できなかったmissing sum更新とworkspace内任意cwdの2境界も専用fixtureで
固定した。
