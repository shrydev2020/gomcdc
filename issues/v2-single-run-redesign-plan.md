# v2 single-run再設計 実装計画

## 目的

v2は、全11指標を要求したmeasurement sessionでtest suiteを物理的に二度実行する現行の
`dual-run-standard-cover`を廃止し、各対象packageのtest binaryをsession中に一度だけ実行して、
同じ実行からGo cover、AST、compiler-awareの各evidenceを得る。

これは単なるrun数削減ではない。元sourceから得たInventoryだけをcoverage obligationのauthority
とし、producerのraw evidenceを検証・正規化してからInventoryへ射影する計算モデルへ移行する。
Statement、Function、Decision、Condition、Clause、MC/DCの定義、strict判定、threshold判定、
partial evidence、exit codeは性能のために弱めない。

## Authorityと禁止事項

優先順位は次のとおりとする。

1. v2では全指標要求時も各packageのtest binaryをmeasurement sessionごとに一度だけ実行する。
2. Inventoryだけが分母を決める。cover profile、計装後source、runtime journalは分母を増減しない。
3. raw evidenceはintegrity、provenance、producer compatibility、mapping、completenessを確認するまで
   coverageへ使用しない。
4. single-run失敗時にdual-runへ切り替えるproduction fallbackを設けない。
5. 対応がpartialまたはambiguousなcover regionを位置の重なりだけでcoveredにしない。
6. v1のdual-run実装は決定的fixtureのtest oracleとしてのみ保持する。

段階実装は順序を決めるだけであり、v2の完了条件を「内部model追加」へ縮小しない。

## 計算モデル

```text
Original source
    │
    ├── Inventory construction ───────────────┐
    │       obligation authority              │
    │                                         │
    └── Instrumentation planning              │
            ├── rewritten source              │
            ├── generated-region manifest     │
            └── CoverageCorrespondence        │
                                              │
one measurement session                       │
    └── one test execution per package        │
            ├── Go cover evidence             │
            ├── AST evidence                  │
            └── compiler-selection evidence   │
                    │                         │
                    ▼                         │
              Raw evidence                    │
                    │                         │
                    ▼                         │
              Evidence acceptance             │
                    │                         │
                    ▼                         │
              AcceptedEvidence                │
                    │                         │
                    └── Coverage projection ──┘
                                │
                                ▼
                              Report
```

reportはproducer failureを推測しない。実行層が確定したrun outcome、producer outcome、
coverage projectionを表示・集約する。

## Scopeの区別

- `InventoryScope`: 元source上でobligationになり得るproduction file、internal test file、
  external test package fileの集合。build tag、GOOS、GOARCH、CGOはloaderと同じ条件を使う。
- `InstrumentationScope`: 選択指標のproducerがrewriteまたはcompile-time hookを適用するfileの集合。
- `ExecutionScope`: `go test`がbuild・実行するpackageとtest binaryの集合。
- `ReportScope`: Inventoryのうちexclude設定とmetric選択を適用した表示・集約対象。

generated helper、gomcdc runtime copy、cgo生成物、dependency、stdlib、vendorは、明示的に
Inventoryへ採用されない限りobligationを作らない。4つのscopeが一致すると仮定しない。

## Instrumentation Ordering Contract

combined-runは次の順序を契約とする。

1. build-activeな元sourceからInventoryを構築する。
2. 元sourceとInventoryからAST rewrite計画、generated-region manifest、
   CoverageCorrespondenceを構築する。
3. disposable workspace内のcopyだけをAST rewriteする。
4. `go test -coverprofile`により、rewrite済みsourceへ選択中のGo toolchainの公式coverを適用する。
5. compiler-aware hookは同じcompileへ適用するが、元Inventoryを変更しない。
6. 実行後のcover regionは、事前計画したCoverageCorrespondenceと一致した場合だけ採用する。
7. generated/originalを跨ぐregion、未知region、partial/ambiguous relationはmeasurement failureとする。

installed GOROOTと元module sourceは変更しない。

## CoverageCorrespondence

`SourceMap`はfile名・物理位置・論理位置の変換だけを担当し、coverage成立を決めない。
coverage成立はcover regionと元Inventory obligationの論理関係で表す。

```go
type CorrespondenceRelation string

const (
    RelationExact     CorrespondenceRelation = "exact"
    RelationCoversAll CorrespondenceRelation = "covers-all"
    RelationPartial   CorrespondenceRelation = "partial"
    RelationAmbiguous CorrespondenceRelation = "ambiguous"
    RelationGenerated CorrespondenceRelation = "generated"
)
```

- `exact`: 一つのcover regionが一つのInventory blockと同じstatement obligationを保証する。
- `covers-all`: 一つのcover regionが列挙されたobligationすべての実行を保証する。
- `generated`: gomcdc生成statementだけを表し、分子・分母の対象外とする。
- `partial`: obligationの一部しか保証できないためcoverageへ使用しない。
- `ambiguous`: 複数の対応候補を一意に決められないためcoverageへ使用しない。

`exact`と`covers-all`だけをprojectionへ渡す。profile rangeの包含・行重複・basename一致だけで
relationを昇格させない。Function Coverageはv1仕様D13を維持し、一つ以上のstatement unitを持つ
Functionをobligationとし、所有statement unitが一つ以上coveredならcoveredとする。
Statement obligationはInventory blockとblock内statement indexの組で識別し、一つのblockを
一つのobligationへ潰さない。

## Evidence状態

`VerifiedEvidence`という、意味保存までrunごとに証明したように読める名称は使用しない。
受理後の値は`AcceptedEvidence`と呼ぶ。producer状態は単一enumに潰さず、少なくとも次を分ける。

```go
type ProducerOutcome struct {
    Integrity    IntegrityStatus
    Completeness CompletenessStatus
    Mapping      MappingStatus
    Usability    UsabilityStatus
}
```

truncated tailを持つvalid prefix、完全なmappingを持つpartial execution、corrupt frame、unknown
regionを区別する。partial evidenceを受理できるのは、受理可能なprefixと対応obligationが明確な場合
だけである。

## 実装順序

### V2-001: 契約とcorrespondence model

- CoverageCorrespondenceの型、clone、deterministic ordering、不変条件検証を実装する。
- obligationなしの`exact/covers-all`、obligationを持つ`generated`、重複region、重複obligation、
  不正rangeを拒否する。
- `partial/ambiguous`がprojectionへ入らないfail-closed testを追加する。

### V2-002: correspondence planner

- 元Inventory、rewrite計画、generated-region manifestからcover region対応を事前構築する。
- Go 1.26の実cover profileとplanner出力をdifferential testで比較する。
- 複数statement block、multiline expression、switch clause、`//line`、`_test.go`、generated helper、
  build tagをfixtureに含める。

### V2-003: combined-run実験経路

- AST workspaceで`CoverProfile`とruntime event outputを同時に有効化する。
- 各package test binaryの実行回数を副作用markerで検証する。
- 決定的fixtureではv1 dual-run oracleとsemantic reportを比較する。
- 非決定的fixtureでは完全一致を要求せず、schema、provenance、coverage monotonicity、
  impossible evidence不在を検証する。

この段階ではpublic CLIのmeasurement modeを変更しない。silent fallbackも追加しない。

### V2-004: evidence acceptanceとproducer outcome

- runtime、cover、compiler evidenceのintegrity・completeness・mapping・usabilityを分離する。
- `verifiedRuntimeEvidence`を意味に沿った`acceptedRuntimeEvidence`へ置換する。
- report入力からtransport provenance解釈を排除する。

### V2-005: production cutover

- 全指標要求をcombined single-runへ切り替える。
- report schemaとmeasurement modeのv2表現を決定し、JSON schema、README、日英仕様を同時更新する。
- dual-runはproduction経路から除去し、oracle test helperだけに残す。
- failure、panic、timeout、interrupt、partial multi-packageで取得済みevidenceを維持する。

### V2-006: journal再評価

- 初期実装は既存journalでcorrectnessを確立する。
- binary化する場合はappend-only framed formatをbaselineとし、run中compactionを前提にしない。
- 非同期writerは導入しない。同期bufferingとflush boundaryはdurability requirementとprofileで決める。

## 現在の実装状況

- V2-001: 完了。型、不変条件、deep copy、決定的順序、fail-closed projectionを実装済み。
- V2-002: 進行中。元Inventoryとrewrite後Inventoryからの事前planner、generated/original境界、
  同一行の列情報が失われた場合の等数・順序対応、MCDC計装不要fileのidentity correspondenceを
  実装済み。build tag、`_test.go`を含むdifferential fixtureは未完了。
- V2-003: 進行中。単純fixtureでは一度の`go test`からGo coverとAST evidenceを同時取得し、
  実行回数1回、plannerと実profileのregion一致を確認済み。switch、select、fallthrough、panic、再帰、
  concurrency、labelを含む既存fixtureではcorrespondenceの投影可能性と意味保存を確認済み。
  v1 dual-runとの全11指標oracle比較は未完了。
- V2-004以降: 未着手。production CLIは現行dual-runのままであり、fallbackは追加していない。

## 検証と性能gate

Correctness gate:

- `go test -count=1 ./...`
- `go test -race -count=1 ./...`
- `go vet ./...`
- 全11指標の決定的fixtureでv1 oracleとのsemantic一致
- packageごとのtest binary実行回数が1 sessionにつき1回
- 未知・partial・ambiguousなcover relationがmeasurement failureになる
- denominatorが元Inventoryだけから決まり、生成statementで増えない

Performance gateはcorrectness後に適用する。

- Phase 1: full self measurement 55秒以下
- Phase 2: 45秒以下
- Target: 35〜42秒

単純な既存phase時間の減算を理論下限とは呼ばない。wall time、CPU profile、allocation、journal bytes、
cover profile bytes、最終report bytesを分けて記録し、full self measurementを根拠なく反復しない。

## 初期非目標

- v2開始時点でのUI・HTML再設計
- production fallbackとしてのdual-run維持
- process-globalまたは永続cache
- run中journal compaction
- 非同期writer goroutine
- 対応不能regionを行番号やbasenameで推測して成功扱いすること

これらはv2 completionから外すという意味ではなく、single-runのcorrectnessを確立する前に混ぜない
ための順序制約である。
