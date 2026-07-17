# Runtime event journal I/O修正計画

## 目的

AST計測中のruntime event journalについて、異常終了時のpartial evidence回収を維持した
まま、重複eventの追記、全量compaction、`fsync`によるwrite amplificationと待ち時間を
削減する。

この計画の対象はevent記録経路である。Masking MC/DC探索、HTML生成、workspace copy、
standard-coverとのdual-runは別の処理であり、本修正の改善値へ混在させない。

「SSD書込みを削減した」という主張は、最終file sizeではなく、runtimeが要求したwrite
bytes、write回数、compaction回数、sync回数の変更前後比較に基づく。OS、filesystem、
SSD controller内部のwrite amplificationや実NAND書込み量は、hardware telemetryを取得
しない限り測定済みとは表現しない。

## 現状と暫定baseline

生成runtimeはDecision evaluationのbegin/terminalとClause eventをprocess固有のJSONLへ
同期的に追記する。現在は256 eventごとに次を行う。

1. unique evaluation、active begin、unique clauseを収集してsortする。
2. 一時fileへ全量を書き直す。
3. 一時fileを`Sync`してcloseする。
4. journal fileをrenameで置換する。
5. journalを再openする。

該当箇所は`internal/runtimecov/runtimecov.go`の`writeEventLocked`と`compactLocked`である。
compaction後のsnapshotが大きくなっても固定256 eventで再実行されるため、長いloopでは
同じunique evidenceを繰り返し書き直す。

2026-07-17にApple M2 Pro、darwin/arm64で得た暫定値は次のとおりである。この値は一回の
診断値であり、MOPTのbenchmark結果とは別に、RJI-001で複数回baselineを取り直す。

| 対象 | 通常 | AST計測 | 備考 |
|---|---:|---:|---|
| `internal/mcdc` package test | real 0.97秒、test 0.463秒 | real 44.07秒、test 43.180秒 | AST-only、MC/DC 4指標 |
| AST計測process | user － | user 6.23秒、sys 14.93秒 | wall timeの大半がCPU時間ではない |
| 最終event journal | － | 90 KiB、384 record | workspace全体は39 MiB |

利用者が実行した全指標のself measurementでは、standard-coverの最長packageが
`internal/cli` 35.086秒、AST計測が`internal/cli` 119.881秒、`internal/mcdc`
100.374秒だった。Go package testは並列実行されるため各package時間を合計しないが、
少なくともAST phaseの支配箇所はreport/HTML生成より前にある。

最終journalが90 KiBであることは、実行中の累積write bytesが90 KiBだったことを意味
しない。現時点では実NAND書込み量もruntimeの累積要求write bytesも未計測である。

## 維持する意味と制約

実装案は少なくとも次を不変条件として扱う。

- D9に従い、`EndDecision`へ到達したevaluationだけをcompletedとする。
- panic、`runtime.Goexit`、timeout、process interruption等でterminalへ到達しなかった
  evaluationをcompletedへ昇格しない。
- D23に従い、再帰、nest、loop、複数goroutine、複数processのEvaluationIDとprovenanceを
  混同しない。
- D24に従い、probeの戻り値、評価順序、短絡、panic、defer、副作用順序を変えない。
- D25に従い、test/build failure、timeout、panic、truncated tail、異常終了時も、commit済み
  evidenceからpartial reportを構成できる。
- 同一file内の`(runID, packagePath, PID)` provenanceを維持する。
- duplicate evidenceがcoverageを増やさず、record順序やmap iteration順で結果が変わらない。
- D32に従い、witnessを失わず、loop回数に比例する全履歴をmemoryへ保持しない。
- unique evaluation数、active evaluation数、unique clause数に必要なmemoryは明示する。
- truncated tailより前の正常recordを失わない。
- recorder failureはuser programの値やcontrol flowを変えず、既存のdiagnostic/integrity
  policyに従う。
- JSON report schema、text/HTMLのcoverage結果、CLI option、exit codeを変更しない。
- 既存journal record形式を読めるcollector互換性を維持する。

### 耐久性の境界

現仕様が要求する障害はtest processのpanic、`Goexit`、kill、timeout、tool cancellation、
truncated tailである。hostの突然の電源断、kernel crash、filesystem corruption、SSD故障後
までのdurabilityは明記されていない。

RJI-002でこの境界を確認する。process異常終了だけを保証範囲とする場合、各compactionの
`fsync`を正当化するためにpower-loss durabilityを暗黙追加してはならない。power-loss
durabilityを新たに要求する場合は、性能cost、directory `fsync`、rename durabilityを含む
別の仕様変更として扱う。

## この計画で実施しないこと

- dual-runを一回へ統合しない。
- MC/DCのcoverage定義、witness探索、resource budgetを変更しない。
- reportやHTMLの表示・schemaを変更しない。
- 性能上の都合でaborted evidenceやpartial recoveryを黙って削除しない。
- 非同期channelへ置き換えるだけの変更を行わない。
- public CLI flagや環境変数でjournal tuningを利用者へ公開しない。
- OS cacheを消す、またはSSDへ直接I/Oする仕組みを製品へ追加しない。
- 最終file sizeだけを根拠にSSD endurance改善を主張しない。

## 作業項目

### RJI-001: writer baselineと観測手段を追加する

既存の`BenchmarkCollectDetailed`はcollectorが完成済みjournalを読む速度だけを測る。
今回のbottleneckである生成runtime writer、compaction、syncを測るbenchmark/fixtureを
追加する。

必須scenario:

- 同じ1-condition evaluationを10,000回完了するduplicate-heavy loop
- 全evaluation vectorが異なるunique-heavy入力
- unique snapshotがcompaction閾値を超えて成長する入力
- active beginを残して正常終了、panic、`Goexit`、外部killする入力
- duplicate/unique Clause eventが混在する入力
- nested evaluationと再帰
- 1、8、32 goroutineの同一package writer競合
- 複数package writerが並行する入力
- short write、create、close、rename、reopen等のrecorder failure

記録項目:

- wall time、`ns/op`、`B/op`、`allocs/op`
- logical event数とunique evaluation/clause数
- `Write`呼出し回数と要求write bytes
- compaction回数とcompactionで再書込みしたbytes
- `Sync`呼出し回数
- journalのpeak sizeとfinal size
- completed、aborted、not-evaluated decision、Clause evidenceの回収件数

write accountingはproductionのpublic API/CLIを増やさず、生成sourceのtest variantまたは
内部test hookで行う。計測自体がproduction hot pathへ無視できないbranch、atomic更新、
追加I/Oを持ち込まないようにする。

baseline採取自体で不要なSSD writeを繰り返さない。最初にtest sinkを使うbounded fixtureで
要求write bytes、compaction、sync回数を確定し、その後にだけ実filesystemでwall timeを
測る。full self measurementを観測手段の実装前に反復しない。RAM-backed filesystemを
利用できる場合はfailure/raceの反復へ使用できるが、実filesystemのlatency比較とは分ける。

変更前後の実filesystem比較は同じGo version、GOOS/GOARCH、filesystem、package pattern、
coverage選択で各5回以上測定し、medianと分散を保存する。ただしsemantic failureが出た時点
で残りの性能runを中止する。macOSの`fs_usage`等は補助証拠にできるが、cross-platformな
完了条件にはしない。

### RJI-002: failure/durability modelをtestで固定する

writer最適化の前に、journalがどの時点からcommit済みとみなされるかを明文化し、child
process fixtureで次の停止点を作る。

- begin書込み前後
- condition更新後、terminal書込み前
- terminal書込み中と直後
- compaction用一時file作成後
- snapshot書込み途中
- temporary close前後
- rename前後
- journal再open前後

各停止点で次を確認する。

- 既にcommitされたcompleted evaluationとClause evidenceが残る。
- terminalを持たないbeginはabortedとして扱われ、completedにならない。
- truncated final recordは、それ以前の正常recordを無効化しない。
- duplicate recordは分子を増やさない。
- compaction一時fileが残っても、正規journalのcommit済みevidenceを消さない。
- 異なるprocessの同じlocal EvaluationIDを混同しない。
- collectorが不完全なcompaction artifactを正規journalとして採用しない。

障害点はsleepやtiming競争だけで作らず、test variantのdeterministic checkpointで制御する。
既存の`TestCollectDetailedJournalModelInvariants`、concurrent runtime fixture、compaction
fixture、CLIのpanic/timeout/truncation integration testを新しいmodelの回帰gateにする。

### RJI-003: compaction policyを選定する

RJI-001/002の結果から、少なくとも次の案を比較する。

#### 案A: byte基準のadaptive compaction

- append-only record形式を維持する。
- 固定256 eventではなく、compaction後のsnapshot sizeとappend tail bytesを基準にする。
- minimum tail sizeとsnapshotに対する比率を設け、snapshotが大きい場合に数eventごとに
  全量再書込みしない。
- process異常終了だけが保証範囲なら、compactionごとの`Sync`を除去または大幅に減らす。
- close、atomic rename、reopen中の停止点をRJI-002で検証する。

この案はjournal形式とcollectorをほぼ維持できるため、最初に実装・評価する。
minimum bytesや比率はRJI-001の結果から決め、根拠のないmagic numberにしない。

#### 案B: unique snapshotとactive lifecycleの分離

- completed unique evaluation/Clauseのsnapshotと、現在activeなevaluationのlifecycleを
  分離する。
- duplicate-heavy loopでcompleted vector全体を再記録しない。
- active beginを異常終了時にabortedへ回収できることを維持する。

案Aが性能目標を満たさない場合だけ検討する。terminalだけを省略すると対応するbeginが
collector上でabortedになり得るため、「duplicate terminalだけを書かない」という変更は
単独では採用しない。

#### 案C: buffered/asynchronous writer

goroutine待ちがA/B後も支配的な場合だけ検討する。panic、`Goexit`、`os.Exit`、timeout、
SIGKILLではdeferやbackground flushを期待できないため、bounded channelを追加するだけの
設計は採用しない。evidence commit point、backpressure、shutdown不能時の回収を先に証明
する。

選定基準:

- RJI-002の全failure invariantを満たす。
- requested write bytesとsync回数がloop回数に対してどのように増えるか説明できる。
- memoryとdiskの上限をunique evidence、active evaluation、tail sizeで説明できる。
- record/provenance modelを不必要に変更しない。
- race-freeで、別package writerを不必要に直列化しない。
- 実装の複雑さに対してend-to-end改善が確認できる。

### RJI-004: 実装と境界testを更新する

採用案の実装では次を行う。

- `writeRecord`が書いたbytesをpolicyへ正確に返す。
- compaction triggerをevent countから選定したresource単位へ変更する。
- snapshot、active begin、Clauseの決定論的順序を維持する。
- compaction一時fileの命名、permission、cleanup、collectorの扱いを定義する。
- compaction失敗後も元journalへ安全に追記またはdiagnosticを残せるようにする。
- generated runtime sourceが`gofmt`可能で、対象moduleへ追加依存を持ち込まないようにする。
- close、rename、reopen、orphan temporaryの正しさを、現行CI対象のUbuntuとmacOSの両方で
  確認する。OS固有のatomicityを未検証のplatformへ一般化しない。

必須test:

- eventが0件でjournalを作らない場合
- `GOMCDC_DATA_DIR`未設定時にwriterがno-opとなり、probeの値を保存する場合
- compaction閾値の直前、ちょうど、直後
- snapshot自体がminimum thresholdより大きい場合
- unique evidenceが増えない長いduplicate loop
- unique evidenceが継続して増えるloop
- active evaluationを保持したままcompactionする場合
- terminalとcompactionが競合する場合
- package別writerとgoroutine競合
- orphan temporary、truncated tail、rename/reopen failure
- disk full、permission failure、短いwrite相当のfault injection
- recorder failureでもprobeが入力値を返すこと
- 旧journalを新collectorが同じ意味で読めること

固定「最大record数」のtestは、採用policyのresource上限へ置き換える。単にassertionを
緩めず、final journalが`unique snapshot + active state + bounded tail`で制限されることを
確認する。

### RJI-005: end-to-end比較とpprofを行う

writer fixtureだけで完了せず、少なくとも次を変更前後で比較する。

```text
go test ./internal/mcdc

./gomcdc test \
  --coverage=decision,condition,mcdc-unique,mcdc-masking \
  --format=json \
  --output=<temporary-json> \
  ./internal/mcdc

./gomcdc test \
  --format=html \
  --output=<temporary-directory> \
  ./...
```

full self measurementはpackageごとの表示時間だけでなく、standard-cover、AST test、event
collection、MC/DC analysis、report生成のwall timeを別々に記録する。必要ならCLIへ公開
しない内部timing observerをtest variantへ置く。

CPU/heap pprofは、I/O改善後に新しい支配箇所を確認するために取得する。pprofだけでは
blocked I/Oと実書込み量を十分に説明できないため、RJI-001のwriter countersと併用する。

### RJI-006: 結果と仕様を更新する

`issues/runtime-journal-io-optimization-results.md`へ次を保存する。

- 変更前後の環境とcommand
- writer counters、benchmark、end-to-end時間
- 採用案と不採用案の理由
- process異常終了とpower-loss durabilityの境界
- memory/disk使用量の増加式または上限
- crash-point test結果
- pprof上の新しいhotspot
- SSD実NAND書込み量を測っていない場合、その制限

D25/D32の意味を変更せず実装だけを改善した場合、仕様へalgorithm詳細を追加しない。
durability境界やresource上限が利用者に影響する契約になる場合だけ、日本語/英語仕様を
同時に更新する。READMEへ内部compaction方式や測定machine固有値を追加しない。

## 性能完了条件

RJI-001で取り直す変更前medianに対し、同一環境で次を満たす。

- duplicate-heavy 10,000回fixtureのrequested write bytesを80%以上削減する。
- 同fixtureの`Sync`回数を90%以上削減する。
- 同fixtureのwall timeを70%以上短縮する。
- `internal/mcdc` AST-only self measurementのwall timeを70%以上短縮する。
- unique-heavy fixtureのwall time、requested write bytes、peak journal sizeのいずれも、
  理由なしに10%を超えて悪化させない。
- final journalは、loop総回数ではなく`unique snapshot + active state + bounded tail`で
  説明できる上限に収まる。
- full self measurementでcoverage summary、witness、diagnostic、exit resultが変更前と
  一致する。

filesystem benchmarkには分散があるため、時間条件は5回以上のmedianで判定する。
write bytes、compaction、sync回数は決定論的counterで判定する。時間目標を満たしても
evidenceを失う案は不採用とする。

`issues/self-review-validity-and-remediation.md`にあるAST-only 2倍以内、両MC/DC込み5倍以内
は既存の性能目標として別途比較するが、現在のD32本文に数値保証はない。達成しない場合
にcoverage意味を弱めず、残るCPU、lock、build、test側bottleneckを結果文書へ分離する。

## 品質完了条件

- D9、D23、D24、D25、D32の意味が維持される。
- panic、`Goexit`、timeout、SIGINT/SIGTERM、外部kill、truncated tailからpartial evidenceを
  回収できる。
- completed、aborted、not-evaluated、Clause event、provenanceの結果が変更前と一致する。
- duplicate、record reorder、process-local ID collisionでcoverageが変わらない。
- compaction途中の停止でcommit済みevidenceを失わない。
- `go test -count=1 ./...`が通る。
- `go test -race -count=1 ./...`が通る。
- `go vet ./...`が通る。
- 現行CI matrixのUbuntu/macOSで通常testとjournal failure fixtureが通り、Linuxでraceが
  通る。
- gomcdc自身の11指標baselineを下回らない。差が出る場合はobligation単位で説明する。
- benchmark、writer counters、pprof、failure test結果をresults文書へ保存する。
- 根拠なしに「SSD寿命を延ばした」「physical writeを削減した」と表現しない。

## 実施順序

1. RJI-001で変更前writer/end-to-end baselineを固定する。
2. RJI-002でfailure pointとdurability境界をtestにする。
3. RJI-003で案Aを最初の実装案として設計reviewする。
4. RJI-004で案Aを実装し、writer fixture、failure、boundary、fault、race testを実行する。
5. RJI-005で変更後比較を行い、性能条件を満たさない場合だけRJI-003へ戻って案B、
   さらに必要な場合だけ案Cを設計reviewする。
6. self measurementとpprofを確定し、通常test、race、vet、self-MC/DC baselineを確認する。
7. RJI-006のresultsと、必要な場合だけ仕様を更新する。

## Rollback条件

次のいずれかが発生した変更はreleaseせず、最後にfailure invariantを満たしたpolicyへ戻す。

- commit済みevaluationまたはClause evidenceを失う。
- active evaluationをcompletedへ誤昇格する。
- normal test failureとintegrity failureの区別を変える。
- duplicate eventでcoverageが増える。
- compaction artifactを別processの正規journalとして受理する。
- memoryまたはdisk使用量がloop回数に対して無制限になる。
- race、deadlock、probeの値・control flow変更が発生する。

public migration flagは追加しない。journal record形式を維持し、変更のrollbackはwriter
policyの差し戻しで可能にする。
