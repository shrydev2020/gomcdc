# gomcdc

[English](README.md)

gomcdcは、statement、function、decision、condition、clause body、
clause selection、Unique-Cause MC/DC、Masking MC/DCを計測するGo向け
カバレッジツールです。

インストールされるコマンドは gocoverage です。

## 仕様

- 日本語の規範仕様: docs/specification.ja.md
- 英語の参考訳: docs/specification.md

指標の定義、JSON field、閾値、完成条件は仕様書を正とします。
READMEでは、現在のcheckoutを使うために必要な情報だけを説明します。

## 開発状況

これはまだ1.0リリースではありません。現在の実装はrepositoryのtest、
race、vetを通過していますが、次の2つの仕様上の完了条件が残っています。

- expression switch / type switchのcase選択を正確に記録するbackend
- report schemaを1.0-draftから最終versionへ移行すること

完成とは、規範仕様24節の全項目を実装し、fixture integration suiteで
検証できる状態です。それまではreportを認証や規格適合の証拠として
扱わず、開発時の診断情報として利用してください。

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
gocoverage test --format html --output coverage-html ./...
```

HTML reportは coverage-html/index.html に出力されます。

各file pageには、元source bytesへstatement、decision、condition、clause、
両MC/DCのbyte-range annotationを重ねて表示します。Statement、Decision、
Condition、Combinedの切替はCSSだけで行い、JavaScriptや外部resourceを
読み込みません。

このcheckoutのコマンドは gocoverage test です。利用可能なoptionは
gocoverage test -h で確認できます。

## セキュリティ

gocoverageは現在のユーザー権限で対象moduleをbuildし、そのtestを実行します。

一時workspaceはsecurity sandboxではありません。信頼できるcodeだけを
実行し、testへsecretを渡さないでください。reportにはmodule-relative path、
source expression、package名、test outputが含まれる場合があります。

## 開発時の確認

```sh
go test -count=1 ./...
go test -count=1 -race ./...
go vet ./...
```

## package境界

このrepositoryが公開するのはcommandであり、Go library APIではありません。
そのため実装packageは internal/ 配下に置いています。公開interfaceは
gocoverage commandとreport schemaです。

## ライセンス

このrepositoryにはlicense fileがありません。再配布・改変の条件は、
repository所有者がlicenseを追加するまで定義されません。
