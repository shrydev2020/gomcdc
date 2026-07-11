# gomcdc

[English](README.md)

`gomcdc`は、Statement、Function、Decision、Condition、Clause Body、Clause Selection、Unique-Cause MC/DC、Masking MC/DCを解析するGo向けカバレッジツールです。

インストールされるコマンド名は`gocoverage`です。

## 仕様

- [日本語の規範仕様](docs/specification.ja.md)
- [英語翻訳](docs/specification.md)

日本語仕様を規範版とします。

定義、指標名、JSON field、閾値、完成条件は規範仕様だけに置き、READMEへ重複記載しません。

## 開発状況

本リポジトリは開発中であり、`1.0-draft`仕様の全要件にはまだ適合していません。

未完成の主な領域は、compiler-aware Clause Selection backend、正式指標名への移行、分母0に対するnullable percentage、最終JSON schemaです。

現在の出力を安全認証または規格適合の根拠として使用しないでください。

## 動作要件

- Go 1.26.5以降のGo 1.26系列セキュリティ修正版
- Go Modules
- macOSまたはLinux

## インストール

```sh
go install github.com/shrydev2020/gomcdc/cmd/gocoverage@latest
```

## 実行

```sh
gocoverage test ./...
```

最終的なCLIはdraft仕様で定義します。

実装適合が完了するまでは、checkoutしたrevisionの挙動を`gocoverage test -h`で確認してください。

## 信頼境界

`gocoverage`は、現在のユーザー権限で対象moduleをbuildし、そのtestを実行します。

一時workspaceは計装境界であり、security sandboxではありません。

信頼できるcodeだけを実行し、信頼できないtestへproduction credentialを与えないでください。

reportにはmodule-relative path、source expression、package名、test outputが含まれる場合があります。

## 開発時の確認

```sh
go test -count=1 ./...
go test -count=1 -race ./...
go vet ./...
```

## package境界

本リポジトリはGo library APIを公開していないため、実装packageを`internal/`配下に置きます。

仕様適合後の公開契約は、`gocoverage`コマンドとversioned report schemaです。

## ライセンス

現時点ではライセンスを追加していません。

リポジトリ所有者がライセンスを追加するまで、再配布または改変の権利があると仮定しないでください。
