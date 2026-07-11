# 再レビュー指摘の妥当性と修正計画

## 総評

「元source全文、byte offset、C0/C1/C2 annotation、重複range分割、HTML escape、
self-contained HTMLが実装済み」という総評は妥当である。未実装と断定する内容では
ない。

一方、source viewの実装には、同じspanへ複数metricのclassを付与することから生じる
意味の混線がある。これは見た目の好みではなく、選択したmetricと別metricの状態を
誤認させるため、最優先で扱う。

| 指摘 | 判定 | 優先度 | 補足 |
| --- | --- | --- | --- |
| C0/C1/C2のmode分離 | 妥当 | P1 | 現在のCSSは非対象segmentを薄くするだけで、対象spanに残る別metricのbackgroundを無効化しない。 |
| Combined表示 | 妥当 | P1 | 同じspanに複数のbackground状態があり、CSS cascadeの一色に潰れる。tooltipだけでは視覚的なCombinedを満たさない。 |
| condition EntityID | 妥当 | P1 | conditionとMC/DC annotationがdecision IDだけを使うため、decision内のoccurrenceを一意に識別できない。 |
| HTML projectionのBuild混入 | 妥当 | P2 | JSON/textでは`FileReport.Source`が`json:"-"`でも、Build時にsource bytes・annotation・range分割を実行している。 |
| location変換失敗の可視化 | 部分的に妥当 | P2 | まずbyte offsetが不正ならline/columnへfallbackする。fallbackも不正な場合にzero-lengthとしてannotationを捨てるため、その失敗が診断されない点が問題。 |
| tooltip情報量 | 妥当 | P2 | 正確性欠陥ではないが、source-centered UIの実用性を下げる。report modelの情報を表示できていない。 |
| intervalの二次計算量 | 妥当・既存issueと重複 | P3/PERF-002 | `issues/self-review-validity-and-remediation.md` のPERF-002と同じ論点。出力される重なり数が多い場合の下限も考慮する。 |

## SRC-001: metric modeを意味的に分離する

### 根拠

`internal/report/html.go` は一つのsegmentへ、重なるannotationの全metric classと
状態classを付与する。`report/template/report.html` は、選択中metric以外のsegmentを
`opacity`で薄くするだけで、同じsegmentに残った別metricのbackgroundを消していない。

したがって、Statement modeでもdecision/conditionの状態classが同じspanに残り、
選択metricの状態と違う色が表示される可能性がある。

### 修正方針

最も安全なのは、metricごとのprojectionを構造的に分けることである。

```go
type SourceMetricView struct {
	Metric   string
	Segments []SourceSegment
}

type SourceFileView struct {
	Path  string
	Views []SourceMetricView
}
```

各単独viewへは該当metricのannotationだけを渡す。Combinedは、全metricを一色の
backgroundへ潰すのではなく、明示的な視覚チャネル（例: C0 background、decision
underline、condition border）を定義する。視覚チャネルを定義できない間は、Combined
を「全annotationの一覧」と誤認させない形へ変更するか、単独viewだけを提供する。

単にCSSのselectorを追加してbackgroundを上書きする方法は最小修正にはなるが、metric
追加のたびにcascade依存が増え、Combinedの意味問題を解決しないため恒久策にはしない。

### 完了条件

- Statement/Decision/Condition modeで、非選択metricの状態色が表示されない。
- 同一rangeに複数metricが重なるfixtureを追加する。
- Combinedで各metricの状態を同時に判別できるか、提供しない場合はUI上でCombinedを
  提供しないことが明示される。
- HTML snapshotとsource rangeの決定論性を維持する。

## SRC-002: condition occurrenceの識別子を一意にする

### 根拠

`internal/report/source.go` のcondition、Unique-Cause MC/DC、Masking MC/DC
annotationは、同じdecision内の全conditionにdecision IDを設定している。coverage
model上のconditionはsource textではなく、decision内のindexを持つoccurrenceである。

### 修正方針

まずHTML projection内のIDを次のように固定する。

```text
<decision-id>:condition:<index>
<decision-id>:condition:<index>:mcdc-unique
<decision-id>:condition:<index>:mcdc-masking
```

これは現在の`SourceAnnotation`の一意性を直す最小変更である。canonical JSONへ
`ConditionID`を追加することは、外部consumerがcondition単位リンクを必要とする場合に
別途判断する。不要なschema拡張を先に行わない。

### 完了条件

- 一つのdecision内の全condition annotationのEntityIDが異なる。
- 同じsource textを持つcondition occurrenceもindexで区別される。
- MC/DC strategy別IDが衝突しない。
- IDはsource revision内で決定論的である。

## SRC-003: HTML source projectionをreport.Buildから分離する

### 根拠

`report.Build`の最後で`attachSourceViews`を実行している。`WriteJSON`と`WriteText`も
`Build(input)`を呼ぶため、HTMLを出力しない場合でもsource bytesのstring化、annotation
生成、offset変換、range分割が走る。`FileReport.Source`が`json:"-"`であることは、
不要な計算とメモリ確保を防がない。

### 修正方針

- `Build(input)`はcoverage hierarchy、evidence、summaryだけを構築する。
- `WriteHTML`だけが`Build(input)`後に`BuildSourceViews(report, input.SourceFiles)`を
  実行する。
- HTML専用の`HTMLReport`またはtemplate入力型を導入し、canonical report modelへ
  presentation専用フィールドを混ぜない。

### 完了条件

- JSON/text出力でsource全文を保持・変換しない。
- HTMLのsource viewは従来どおり元source位置へ対応する。
- JSON schema、text出力、HTMLの内容をそれぞれsnapshotで固定する。
- `Build`がsource projectionなしでも既存の集計結果を返す。

## SRC-004: source mapping失敗をunknownとして表示する

### 根拠

`sourceRangeOffsets`は不正offsetに対してline/column fallbackを試みる。fallbackでも
有効なrangeにならない場合、`normalizeAnnotations`がzero-length annotationを除外する。
そのためcoverage entity自体は存在するのに、source viewだけでは「entityがない」のか
「mappingできなかった」のか区別できない。

### 修正方針

offset変換を、rangeだけでなく結果状態も返す関数にする。

```go
type SourceMappingResult struct {
	Start, End int
	Status     string // mapped, unknown
	Reason     string
}
```

mapping不能entityはannotationを黙って捨てず、HTML側で件数とreasonを表示する。
`not-covered`、`unsupported`、`unknown`、`infeasible`とsource mapping failureを
同じcoverage状態へ混ぜない。

### 完了条件

- 不正offset、line/column fallback成功、fallback失敗を個別fixtureで確認する。
- mapping failureがsource diagnosticsに出る。
- mapping failureを通常のnot-coveredへ算入しない。

## UX-001: tooltipをmetric別の証跡へ拡張する

これはP1の意味混線を直した後に行う。現状の短い`Decision: true-only`等は正しいが、
true/false/not-evaluated数、condition index、expression、両MC/DC statusを十分に
表示していない。

metricごとのviewでは、例えば次を表示する。

```text
Condition #1: b
true: covered | false: not covered | not evaluated: 12
Unique-Cause MC/DC: not covered
Masking MC/DC: covered
```

同一rangeに複数annotationがある場合は、metric名を明示して情報を連結する。HTMLの
`title`属性だけで行構造を表現しにくい場合は、`aria-label`またはdetails要素を使う。

## 実施順序

1. **SRC-001:** modeとCombinedの表示意味を固定するテストを追加し、単独viewを構造
   的に分離する。
2. **SRC-002:** condition/MC/DC EntityIDを修正する。
3. **SRC-003:** HTML projectionを`Build`から分離し、JSON/textの不要なsource処理を
   除く。
4. **SRC-004:** mapping failureのdiagnosticを追加する。
5. **UX-001:** tooltipを拡張する。
6. **PERF-002:** 前回issueのsweep-line検討を、SRC-001のprojection形状が確定した後に
   行う。

## 不要な変更

- condition単位のHTML IDを直すためだけにcanonical JSON schemaへ`ConditionID`を追加
  しない。
- sweep-lineへ置き換えれば必ずHTML全体が`O(A log A)`になるとは主張しない。
- source viewの欠陥を理由に、C0/C1/C2のcoverage計算やreport集計を作り直さない。
