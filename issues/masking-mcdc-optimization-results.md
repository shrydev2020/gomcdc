# Masking MC/DC探索の最適化結果

## 結論

Masking MC/DCの探索を、evaluationごとのcompletion全列挙とcompletion pair直積から、
read-once AND/OR/NOT式木上で両completionを同時に解くjoint dynamic programmingへ
置き換えた。D19の意味は変更していない。

この変更は、completion数に対する指数的なmaterializationを除去する。ただし、重複除去後の
evaluation数を`E`、condition obligation数を`C`、constantとNOTを含む式node数を`N`、
一つのobligationで実際に調べるcandidate evaluation pair数を`K`とすると、探索時間は
obligationごとに`O(N + E + K N)`、全体で`O(C(N + E + E²N))`が上限である。このため、
根拠なしに「アルゴリズム的に最適」とは扱わない。

## 採用アルゴリズム

完全assignmentにおいて、あるsubtreeの値の変化がdecision rootまで伝播するかを`active`とする。
read-once式では、子subtreeの`active`は次の局所規則だけで決まる。

- ANDの左子は右子がtrue、右子は左子がtrueの場合だけ親の`active`を引き継ぐ。
- ORの左子は右子がfalse、右子は左子がfalseの場合だけ親の`active`を引き継ぐ。
- NOTはoperandへ`active`をそのまま引き継ぐ。

したがってcondition leaf `j`では、`active`がfalseであることと
`masked(E,z,j)`が同値になる。solver stateは、一つの式nodeについて次の4 bitである。

1. first completionで必要なsubtree結果
2. second completionで必要なsubtree結果
3. first completionの`active`
4. second completionの`active`

一つのnodeにつき16 stateをmemo化する。target leafでは両値が異なり、両方の`active`がtrueで
あることを要求する。non-target leafでは値が異なる場合に両方の`active`がfalseであることを
要求する。rootには観測された二つのdecision結果と`active=(true,true)`を与える。このleaf
制約がD19のpivotal条件とmasking条件を直接表す。

candidate evaluationは、target値とdecision結果の4 bucketへ分類する。両方が反転するbucket
間だけを元の決定的なevaluation順で走査し、成立し得ないpairをsolverへ渡さない。

入力evaluationは、completed/aborted/invalidを分類した後、値copyを直接canonical順へsortし、
隣接する同一vectorをin-placeでdedupする。analysis中のcondition sliceはread-onlyで借用し、
resultへ出すwitnessだけをdeep cloneする。これにより入力不変性を保ったまま、旧map key、
全vectorの事前clone、mapからの再収集を除去する。

## 正確性の範囲

正確性は、metadata検証が保証する次のmodelに対して成立する。

- 各condition occurrenceを一度だけ含むread-once tree
- nodeはcondition、constant、NOT、AND、OR
- condition indexはoccurrenceごとに一意
- 観測vectorはGoの左から右への短絡評価と整合するcompleted evaluation

`active`規則は、leafを反転した影響が各ancestorを通過する必要十分条件である。各nodeでAND/OR
の成立する子値pairをすべて調べるため、DPは観測値とsubtree結果に整合するassignment pairを
漏れなく扱う。leaf制約によりtargetは両completionでpivotalとなり、値が異なるnon-targetは
両completionでmaskedとなる。逆にD19 witnessが存在すれば、そのsubtree値と`active`はDPの
いずれかの遷移に対応するためsolverへ到達する。

completionは観測済みconditionを固定し、not-evaluated conditionだけをcounterfactualに補う。
元programのconditionや副作用は再実行しない。観測vector自体はsolver前にGoの短絡規則で
検証する。

小規模式については、最適化実装とhelperを共有しないtruth-table oracleが全assignmentを列挙し、
AND/ORの全association、nested NOT、full/alternating/singleton evaluation suiteを比較する。
返されたcompletionとmasked conditionもoracleで再検証する。入力evaluation順を反転した結果も
一致させる。

## 決定性

重複vectorは既存のcanonical順へ正規化する。candidate pairはfirst index、second indexの順、
AND/ORの子値候補は固定順、DP stateは固定indexで処理する。map iteration順にはwitness選択を
依存させない。

## Resource limitとmemory

各condition obligationのdefault上限は次のとおりである。

| 単位 | Default | 数えるoperation |
| --- | ---: | --- |
| candidate evaluation pair | 1,000,000 | joint solverへ渡す観測pair |
| joint search state | 4,000,000 | 初めて展開する`node × 4-bit state` |
| primary workspace | 64 MiB | flatten済み式、memo、2 completion buffer、candidate index backing array |

上限ちょうどでwitnessへ到達した場合は`covered`である。次のoperationまたはallocationが上限を
超える場合だけ`analysis-incomplete`とする。構造検査がtargetをpivotalにできないと証明した
場合だけ`infeasible`とする。全candidate pairを予算内で調べ切った場合は`not-covered`である。

search workspaceは`O(N + C + E)`であり、上表のbyte limitをallocation前に検査する。検証済み
input data、result witness、preprocessing、goroutine stackはworkspace単位に含めない。

## 検討して採用しなかった案

- 線形`pivotalCompletion`だけをfast pathにする案: 一つのcompletionの存在は示せるが、D19を
  満たす別completionが必要なpairを完全には判定できない。
- completion streaming: materializationは減るが、最悪時のcompletion pair直積と重複する
  式評価が残る。
- assignmentごとのpivotal bitset: 一つのassignment評価は速くなるが、assignment列挙自体が
  指数的に残る。
- 汎用SAT/BDD: 現在のread-once treeより広い問題を解ける一方、依存追加、witness順、resource
  単位が複雑になる。16-state/tree-nodeの専用DPで現在のmodelをexactに解けるため採用しない。

## Benchmark

Apple M2 Pro、darwin/arm64、Go 1.26、`-benchmem -count=5`で、各5回の中央値を比較した。
絶対値を他machineの合否基準にはしない。

| Case | Before ns/op | After ns/op | 変化 | Before B/op | After B/op | Before allocs/op | After allocs/op |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| balanced/full, C=8 | 18,835 | 13,281 | -29.5% | 22,264 | 15,672 | 361 | 152 |
| left-skewed/full, C=8 | 19,471 | 14,622 | -24.9% | 23,320 | 16,024 | 364 | 153 |
| right-skewed/full, C=8 | 19,072 | 12,933 | -32.2% | 23,320 | 16,024 | 364 | 153 |
| NOT balanced/full, C=8 | 19,197 | 13,534 | -29.5% | 22,264 | 15,704 | 361 | 152 |
| high-unobserved, C=16 | 12,242,736 | 29,525 | -99.8% (414.7x) | 13,221,112 | 44,896 | 148,245 | 213 |
| Unique-Cause/full, C=8 | 9,559 | 8,098 | -15.3% | 13,000 | 10,696 | 90 | 69 |

high-unobserved caseはevaluationごとの未観測condition数が14.5で、witnessが存在する。変更前は
49,151 completionsをmaterializeしてから最初のcompletion pairを確認した。変更後はcandidate
evaluation pair 1件、joint search state 47件、primary workspace 1,784 bytesで同じwitnessを
得る。

benchmarkはearly/full suite、late witness、witnessなし、resource limit、balanced、左右skew、
NOT、高未観測、およびUnique-Cause非回帰を含む。custom metricとしてcondition数、evaluation数、
未観測数、covered/incomplete数、candidate pair、search state、workspace byteを出力する。

### 重厚case

全assignmentを`2^C`列挙せず、read-once式木から有効な短絡evaluation traceを直接生成する
`BenchmarkMaskingAnalyzeHeavy`を追加した。表は同じmachineでの5回中央値である。

| Case | ns/op | B/op | allocs/op | E | target pair | target state | target結果 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| balanced/full, C=32 | 171,913 | 185,952 | 789 | 33 | 1 | 72 | covered |
| guarded/no-witness, C=24 | 6,969,408 | 134,200 | 432 | 222 | 5,760 | 501,120 | not-covered |
| guarded/default-state-limit, C=48 | 128,487,969 | 33,618,872 | 1,028 | 92,670 | 25,206 | 4,000,000 | analysis-incomplete |
| guarded/evaluation-pair-limit, C=48 | 94,268,288 | 33,618,872 | 1,028 | 92,670 | 10,000 | 1,454,720 | analysis-incomplete |
| high-unobserved, C=64 | 408,647 | 713,472 | 1,200 | 2 | 1 | 191 | covered |

C=48 caseのworkspace metricは746,776 bytesであり、`B/op`との差は主に92,670件の入力vectorを
検証・sortする入力比例のpreprocessingである。search workspace budgetはsolver memoryを制限する
が、検証済みinput dataとpreprocessingは仕様どおりその単位に含まない。

### pprof

`guarded/no-witness-C24`を`-benchtime=5s`でCPU/heap profileした。CPU profileでは
`jointSolver.solve`がflat 50.70%、`solveBinary`がflat 22.22%で、joint solver全体の累積は
88.89%だった。completion生成やconditionごとの式木再評価はhot pathに残っていない。

最初のheap profileではproduction allocation 186.79 MBのうち`prepareEvaluations`がflat
78.16 MBだった。map/key/全condition slice cloneをsort + in-place dedupへ変更後、同じprofileの
production allocationは133.73 MB、`prepareEvaluations`は21.50 MBへ低下した。benchmarkの
C=24は210,288から134,200 B/op、887から432 allocs/opへ、C=48 default-limitは83,491,696から
33,618,872 B/op、279,568から1,028 allocs/opへ減少した。変更後の主要allocationは
`computeFeasibility`、観測vector filter、入力sort buffer、candidate bucketである。

## 検証項目

- 独立semantic oracleとの全生成case比較
- AND、OR、nested、NOT、constant、同一source textの別occurrence
- short-circuit vector、複数witness、入力逆順、witnessなし、構造的infeasible
- evaluation pair、search state、workspace byteのlimit直前とlimitちょうど
- aborted、不正vector、metadata不整合
- 通常test、race、vet、gomcdc自身のMC/DC baseline
