# Runtime event journal I/O修正結果

## 結論

2026-07-17に案A（byte基準のadaptive compaction）を実装した。固定256 eventごとの
全量compactionを廃止し、1 MiB以上のappend tailとunique snapshot sizeから次回閾値を
決める。test/tool processの異常終了だけをdurability境界とし、compactionごとの`Sync`を
除去した。journal record形式、collectorのcoverage意味、CLI、report schemaは変更して
いない。

`internal/mcdc`のAST-only診断値はreal 44.07秒から8.42秒へ短縮し、80.9%高速化した。
一方、writer counter fixtureのrequested write bytesは2.1%しか減っていない。process kill
時にactive evaluationをaborted evidenceとして回収するため、begin/terminal recordの
同期的なappendを維持したためである。したがって「runtimeが要求する全write bytesを80%
削減」という計画上の性能条件は未達であり、SSDの実NAND書込み量も測定していない。

## 環境と測定上の制限

- Apple M2 Pro、darwin/arm64、macOS 26.5.2
- Go 1.26.5
- workspace: `/System/Volumes/Data`上のlocal filesystem
- 変更前後のwriter counterは同じtest-only generated source instrumentationで取得した。
  production hot pathへcounterや公開optionは追加していない。
- wall timeはfilesystem cacheを消さずに測定した。SSDへ直接I/Oする測定やphysical NAND
  telemetryは取得していない。
- full `./...` HTML self measurementの変更後runはAST phase途中で利用者が停止したため、
  そのrunは完了値として扱わない。後のrunではHTMLが完成したがcommand全体のwall timeを
  取得していないため、package表示時間だけを全体時間として扱わない。
- 変更前後のwall timeは同一machineの診断値だが、5回medianではない。percentageは
  bottleneck解消の大きさを示す診断値であり、release benchmarkの統計値ではない。

## 採用したpolicy

writerは実際に`Write`できたbyte数をappend tailへ加算する。次のcompactionは

```text
max(1 MiB, 2 * compacted snapshot bytes)
```

以上の新しいtailが溜まった時だけ行う。compaction失敗時は、現在のtail sizeまたは従来
閾値の2倍までretry閾値をbackoffする。これにより永続的なrename失敗で各eventごとに
全量snapshotを作り直さない。加算と閾値計算はoverflow時に飽和する。

正常なcompactionは同じdirectoryの一時fileをcloseし、正規journalへrenameしてから
reopenする。途中で残った一時fileは正規journalとして読まず、recoverable diagnosticに
する。正規journalが隣にある場合は、そのcommit済みevidenceを回収する。

memoryはunique completed evaluation、unique Clause evidence、active beginに比例する。
disk上の正規journalは概ね次で制限される。

```text
unique snapshot + active state + next-compaction threshold未満のappend tail
```

したがってduplicate loopの総反復回数には比例し続けない。unique evidence自体が増える
workloadではsnapshotも増えるため、その分のmemoryとdiskは必要である。

## Writer counter

fixtureは同じ1-condition evaluation 10,000回、9-conditionの異なるvector 300回、
terminalを持たないactive evaluation 1件を一つのprocessで記録する。counterはJSONLの
`Write`要求、要求byte、compaction、`Sync`を生成sourceのtest variantだけで数える。

| 指標 | 変更前 | 変更後 | 変化 |
|---|---:|---:|---:|
| requested write calls | 21,033 | 20,605 | 2.0%減 |
| requested write bytes | 4,167,162 | 4,080,702 | 2.1%減 |
| compaction | 80 | 3 | 96.3%減 |
| `Sync` | 80 | 0 | 100%減 |
| final journal | 70,298 B | 934,090 B | 増加、2 MiB上限内 |

final journalが大きくなったのは、毎回小さく畳み直す代わりにbounded tailを残す設計上の
trade-offである。最終sizeだけをSSD write量とはみなさない。fixtureの5回実行をまとめた
wall timeはreal 5.56秒から3.57秒となり、35.8%短縮した。

回収結果は変更後もsemantic vector 302件（decision 7が1件、decision 9が300件、
decision 11のabortedが1件）、decision 8/9のnot-evaluated evidenceを保持した。raw tailの
duplicate record数はcoverage結果へ含めない。

## Checked-in benchmark

`BenchmarkInjectedRuntimeWriter`はprobe binaryのbuildをtimer外で行い、各iterationを別の
data directoryで実行する。`-benchtime=3x -count=5`の結果は次のとおりである。

| scenario | evaluations/op | median ns/op | journal-B/op |
|---|---:|---:|---:|
| duplicate-heavy | 10,000 | 181,206,264 | 692,170 |
| unique-heavy（13 conditions） | 5,000 | 165,538,444 | 1,483,499 |

これは外部probe process全体のbenchmarkなので、親benchmark processの`B/op`と
`allocs/op`はwriter allocationを表さない。代わりに、製品挙動へcounterを入れずに
wall timeと最終journal bytesを測る。変更前の同一benchmarkは存在しなかったため、
unique-heavyの10% non-regression条件はこの結果だけでは判定していない。

## End-to-end比較

同じcoverage選択で次を実行した。

```text
gomcdc test \
  --coverage=decision,condition,mcdc-unique,mcdc-masking \
  --format=json \
  --output=<temporary-json> \
  ./internal/mcdc
```

| 指標 | 変更前 | 変更後 | 変化 |
|---|---:|---:|---:|
| command real | 44.07秒 | 8.42秒 | 80.9%短縮 |
| package test表示 | 43.180秒 | 7.112秒 | 83.5%短縮 |
| user | 6.23秒 | 3.39秒 | － |
| sys | 14.93秒 | 5.07秒 | － |
| final journal | 90 KiB、384 lines | 95 KiB、406 lines | 同程度 |

変更後summaryはdecision 252/314（80.25%）、condition 291/368（79.08%）、
Unique-Cause 102/157（64.97%、infeasible 27）、Masking 117/184（63.59%）だった。
writer policyはobligation、witness探索、coverage射影を変更しない。

## pprof

変更後の`internal/mcdc` AST testへ`-cpuprofile`を渡した。profile duration 6.04秒、sample
5.03秒では、`compactLocked`のcumulativeは0.26秒（5.17%）だった。一方、
`writeRecord`は4.32秒（85.88%）、`syscall.rawsyscalln`は4.42秒（87.87%）だった。

したがって案A後のruntime writerでは、全量compactionよりも各begin/terminalの小口write
syscallが次の支配箇所である。CPU profileはblocked I/O時間やdevice書込み量を測れないため、
この比率をphysical SSD writeとは解釈しない。

## Failureと回帰確認

- truncated tail以前の正常record、duplicateの冪等性、terminalなしbeginのaborted化、
  process-local EvaluationID衝突をjournal model testで確認した。
- 正規journalとorphan compaction artifactが並存する場合、正規evidenceを回収しartifactを
  recoverable diagnosticにするtestを追加した。
- rename failureをtest variantへ注入し、retry backoff、元journalへの追記、diagnostic、
  semantic evidence不変を確認した。
- 8 goroutineの同一writer競合を、生成probe自体を`go run -race`して確認した。
- `GOMCDC_DATA_DIR`未設定とrecorder path failureのどちらでも、probeの値とcontrol flowが
  変わらないことを確認した。
- `go test -count=1 ./...`、`go test -race -count=1 ./...`、`go vet ./...`は通過した。

## Durability boundary

保証対象はtest/tool processのpanic、`Goexit`、timeout、signal、kill等のprocess異常終了と、
回収時のtruncated tailである。host power loss、kernel crash、filesystem corruption、
storage-device failure後のdurabilityは保証しない。この境界を日英仕様D25へ追加した。

各recordのappend後に`fsync`していたわけではなく、従来の`Sync`もcompaction一時fileだけを
対象としていた。directory `fsync`もなかったため、従来policyをpower-loss durableとは
扱わない。power-loss durabilityを追加する場合は、fileとdirectoryのsync、renameのplatform
契約、性能costを含む別の仕様変更が必要である。

## 未達条件と次の判断

案Aはobserved end-to-end遅延、compaction、`Sync`を大きく減らしたが、requested write
bytes 80%削減とduplicate fixture wall time 70%短縮は満たさなかった。案Bとしてactive
lifecycleとunique snapshotを別形式へ分ければ小口JSONLを減らせる可能性はあるが、kill時の
active begin、terminal commit point、複数goroutine、旧collector互換性を同時に再設計する
必要がある。「duplicateだからterminalだけ省略する」とbeginがabortedへ誤分類されるため、
単独では採用できない。

今回測った要求writeは10,300 evaluationを含むfixture全体で約4 MiBであり、実NAND write
ではない。案Bの意味保存riskに対してこの量を無条件に「SSD寿命問題」とは判定できない。
次に案Bへ進む場合は、binaryまたは固定slotのactive lifecycle modelを先に仕様・crash-point
testへ落とし、案Aをrollback可能な基準として比較する。

## 第2段階: full self measurementの残存遅延

案A後に完了した全11指標HTML runでは、standard-coverのcritical packageは`internal/cli`
33.510秒、ASTは`internal/cli` 38.855秒だった。修正前のAST `internal/cli` 119.881秒からは
67.6%短縮している。AST `internal/mcdc`も100.374秒から13.853秒へ86.2%短縮した。
standard-coverとASTは意味の異なるevidenceを得る2回のtestなので、package表示上のcritical
pathだけでも約72秒残る。

### 重複report build

`runCoverage`は一度構築したsummaryでstrict/thresholdを判定した後、policy結果とerrorsを
反映するためにcoverage hierarchyを再構築していた。`report.Build`はUnique-CauseとMasking
MC/DCを含むため、これはpost-test解析の完全な重複だった。

一回だけbuildし、構築済みreportの`Run.Results`と`Errors`だけを正規化・copyするようにした。
8-condition、24-decision、全truth vectorのfocused benchmark（`-benchtime=3x -count=5`）は
次のとおりである。

| policy反映 | median ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| coverage hierarchyを再build | 11,377,167 | 約13.74 MB | 約68,578 |
| run results/errorsだけ更新 | 5,715,000 | 約6.87 MB | 約34,292 |

wall timeは49.8%、allocation bytesと回数は約50%減った。旧二回build相当とのJSON完全一致、
coverage summary/hierarchy不変、caller errors sliceのcopyをtestで確認した。変更後profileでは
`report.Build`のcumulative 3.52秒中、`buildDecisionReport`が3.12秒であり、残した一回のbuild
ではMC/DC解析とevaluation report構築が正当な支配処理である。

### 不要なshadow GOROOT

`internal/cli`の全AST指標isolated profileでは、変更前test 25.967秒、command real 31.85秒、
CPU sample 6.67秒のうちfilesystem syscallが6.38秒だった。`compileraware.Prepare`が毎回
GOROOT全体のsymlink viewを作り、cleanupがそれを削除していた。

`cmd/go`がoverlayを拒否するのはselected GOROOTが`GOMODCACHE`配下のdownloaded toolchain
である場合なので、その場合だけshadow viewを維持した。Homebrew等の独立GOROOTでは実source
pathをoverlay targetとして使い、installed fileは変更しない。

| focused measurement | 変更前 | 変更後 | 変化 |
|---|---:|---:|---:|
| `compileraware.Prepare` real | 3.01秒 | 1.06秒 | 64.8%短縮 |
| `compileraware.Prepare` sys | 2.73秒 | 0.67秒 | 75.5%短縮 |
| `internal/cli` AST package | 25.967秒 | 17.815秒 | 31.4%短縮 |
| 同command real | 31.85秒 | 20.57秒 | 35.4%短縮 |
| 同command sys | 23.20秒 | 10.08秒 | 56.6%短縮 |

変更後profileのfilesystem syscall sampleは6.38秒から0.08秒へ減った。残る約18秒の大半は
integration testが待つnested Go subprocessであり、親test processのCPU profileには現れない。
この比較は同一machineの各1回の診断値で、cacheを制御した5回medianではない。test集合や
coverage対象を減らして得た改善ではない。

focused test、通常test、race、vet通過後に、全11指標HTML self measurementを一回実行した。

| final full self measurement | 時間 |
|---|---:|
| command real | 61.99秒 |
| user | 75.60秒 |
| sys | 58.39秒 |
| standard-cover critical package | `internal/cli` 27.455秒 |
| AST critical package | `internal/cli` 28.177秒 |
| AST `internal/runtimecov` | 25.309秒 |
| AST `internal/mcdc` | 11.194秒 |

直前runのpackage表示critical path `33.510 + 38.855 = 72.365`秒に対し、今回は
`27.455 + 28.177 = 55.632`秒で23.1%短縮した。package表示はphase setupやpost-test処理を
含むcommand全体ではないため、この23.1%をfull command改善率とは表現しない。変更前runには
command realがないので、61.99秒を以後のfull self baselineとする。HTMLは正常に完成し、
`index.html`は12,371,526 bytesだった。
