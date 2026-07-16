# Masking MC/DC探索の修正計画

## 目的

Masking MC/DCの判定意味を維持したまま、現在のcompletion列挙、completion pairの
直積、式木の反復評価による計算量とallocationを見直す。

本計画は、アルゴリズムの解法を事前に固定しない。アルゴリズムの設計と実装は、
推論effortを最高に設定したSOLモデルへ依頼する。この文書では、その依頼に必要な
目的、不変条件、評価方法、成果物、完了条件だけを定義する。

「最適」という表現は、計算量の根拠または実測なしには使用しない。完了判定は、
正確性の証明範囲、計算量、benchmark、resource limit時の挙動を明示できることとする。

## 現状認識

現在のMasking探索は、condition occurrenceごとに次を行う。

1. candidate evaluation pairを走査する。
2. 各evaluationについて、観測済み短絡pathに整合し、targetをpivotalにするcompletionを
   列挙する。
3. 二つのcompletion集合の直積を調べる。
4. 値が異なるnon-target conditionごとに、両completionでmaskされるか式木を再評価する。

探索予算により停止性と`analysis-incomplete`への遷移は定義されたが、これは計算量や
allocationを改善するものではない。また、現在の`BenchmarkMaskingCompletions`は
全conditionが観測済みの入力を中心としており、未観測conditionが多い場合、witnessが
遅い場合、witnessがない場合、resource limitへ到達する場合を代表していない。

## 変更しない意味

SOLモデルは、少なくとも次を不変条件として扱う。

- Goの`&&`と`||`について、左operandを先に評価し、必要な場合だけ右operandを評価する。
- 観測vectorの`not-evaluated`を`false`または`true`と同一視しない。
- completionはcounterfactual assignmentであり、元programのconditionや副作用を再実行
  しない。
- completedかつ構造検証済みのevaluationだけをMC/DC証拠に使用する。
- abortedまたは不正なevaluationをwitnessへ昇格しない。
- 同じsource textを持つconditionも、別occurrenceなら別conditionとして扱う。
- targetは両completionでpivotalでなければならない。
- 値が異なるnon-target conditionは、D19のmasking条件を両completionで満たす。
- 正当なwitnessが見つかったobligationは`covered`とする。
- exact searchがresource limit内に完了しなかった場合は`analysis-incomplete`とし、
  `not-covered`または`infeasible`へ変換しない。
- 構造上不可能であることを証明できた場合だけ`infeasible`とする。
- 入力順序やmap iterationへ依存せず、witnessとreportを決定論的にする。
- JSON schema、text/HTMLのcoverage状態、`--strict`の意味を変更しない。

## 作業項目

### MOPT-001: 変更前baselineを作る

アルゴリズム変更前に、`MaskingStrategy.Analyze`全体を測るbenchmarkを追加する。
completion生成だけのbenchmarkを改善根拠にはしない。

測定軸:

- condition数`C`
- 重複除去後のevaluation数`E`
- evaluationごとの未観測condition数`U`
- AND/ORのbalanced、left-skewed、right-skewed、NOTを含む式形
- witnessが早く見つかる、遅く見つかる、存在しない場合
- resource limitへ到達する場合
- Unique-Causeの既存経路への非回帰

記録項目:

- `ns/op`
- `B/op`
- `allocs/op`
- 探索したevaluation pair数、completion数、completion pair数
- `covered`、`not-covered`、`infeasible`、`analysis-incomplete`の結果

特定machineの絶対値だけを合否基準にせず、同一環境での変更前後比較を保存する。

### MOPT-002: SOLモデルによるアルゴリズム設計と実装

SOLモデルには、推論effortを最高に設定して依頼する。実装前に、現在の式modelが
condition occurrenceを一度だけ含むAND/OR/NOT treeであることを確認し、その制約を
利用できるか評価する。

検討対象の概要は次のとおりとするが、採用方法はSOLモデルの分析に委ねる。

- 線形時間で一つのpivotal completionを得る既存処理を、安全なfast pathとして利用
  できるか。
- 一つのcomplete assignmentについて、各conditionのpivotal/masked状態を式木全体の
  反復評価ではなく、一回の解析またはbitsetで表現できるか。
- candidate evaluationをtarget値とdecision resultで分類し、成立しないpairの走査を
  避けられるか。
- completionを全件materializeして直積を取る代わりに、streaming、圧縮制約、joint
  solver、tree dynamic programmingのいずれかでexact witnessを探索できるか。
- resource budgetを抽象的なcheck回数ではなく、実際の主要operation数とmemory使用へ
  対応させられるか。

SOLモデルが提出する設計説明には、次を含める。

- 採用アルゴリズムと、採用しなかった主要案の理由
- 正確性が成立する式modelと前提
- 時間計算量と空間計算量
- 最悪時に指数探索が残る場合、その入力条件とresource limitの位置
- witness選択の決定論性
- Goの短絡評価規則との対応
- `analysis-incomplete`と`infeasible`を区別する方法

### MOPT-003: 独立oracleと境界testを強化する

最適化後の結果を、最適化された実装自身から独立したoracleと比較する。

必須case:

- 小規模な全read-once AND/OR/NOT式と全観測vectorの網羅比較
- `a && b`、`a || b`、nested、NOT、constant
- 同一source textの別occurrence
- short-circuitで片側だけ未観測になるvector
- witnessが複数存在する場合の決定論性
- evaluation入力順を反転した場合
- witnessがない場合
- 構造的infeasibleの場合
- completion、pair、operation、memoryの各limit境界
- limitちょうどでwitnessが見つかる場合
- limit前に正当なwitnessが見つかる場合
- aborted、不正vector、expression metadata不整合

oracleが同じhelper、同じcache、同じbitset計算を共有して正解を自己証明しないようにする。

### MOPT-004: 実装後に仕様と利用者向け説明を更新する

アルゴリズム確定後にだけ、READMEと仕様へ次を反映する。

- resource limitの単位とdefault
- `analysis-incomplete`になる条件
- 利用者がlimitを変更できる場合は、その設定方法
- exact searchが保証される範囲
- benchmarkで確認した改善範囲

内部実装の詳細をREADMEへ列挙しない。仕様はcoverage意味と失敗状態、READMEは利用時に
必要な情報へ限定する。

## SOLモデルへの依頼概要

依頼時には次を明示する。

```text
推論effortを最高に設定する。
docs/specification.ja.mdのD18、D19と、internal/mcdcの実装・oracle test・benchmarkを読む。
既存のcompletion列挙を前提として小手先の高速化をせず、read-once AND/OR/NOT treeという
modelからexact Masking MC/DC探索を再導出する。
本計画の「変更しない意味」を破らない。
設計、計算量、正確性根拠、実装、独立oracle、変更前後benchmarkを提出する。
根拠なしに「最適」と主張しない。
```

## 実施順序

1. MOPT-001で変更前baselineを固定する。
2. SOLモデルがMOPT-002の設計説明を作成する。
3. 設計の前提とD19適合性をreviewする。
4. SOLモデルが実装し、MOPT-003を通す。
5. 同一環境でbenchmarkを再実行し、変更前後を記録する。
6. 通常test、race、vet、self-MC/DCを実行する。
7. MOPT-004を実装結果に合わせて更新する。

## 完了条件

- D18/D19とGoの短絡評価意味が維持される。
- 小規模全探索oracleと最適化後の結果が一致する。
- witness、coverage status、resource limit時の状態が決定論的である。
- 変更前後benchmarkが同一条件で保存され、改善点と退化点が説明される。
- memory使用量が入力とbudgetに対してどのように制限されるか説明できる。
- `go test -count=1 ./...`が通る。
- `go test -race -count=1 ./...`が通る。
- `go vet ./...`が通る。
- gomcdc自身のMC/DC baselineを下回らない。下げる場合は失われるobligationと理由を
  別途reviewする。
- READMEと仕様が最終実装と一致する。
- 未証明の範囲を残したまま「アルゴリズム的に最適」と完了報告しない。

## この計画で実施しないこと

- coverage定義やD19を性能上の都合で弱めない。
- `not-evaluated`をbooleanへcoerceしない。
- exact探索をheuristic結果へ黙って置き換えない。
- JSON schemaやCLI optionを、具体的な利用者要件なしに追加しない。
- Go compiler-aware backend、runtime journal、report architectureを同時に再設計しない。
- SOLモデルの設計前に、特定のsolverまたはdata structureを採用決定しない。
