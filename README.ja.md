# gomcdc

[English](README.md)

gomcdcは、statement、function、decision、condition、clause body、
clause selection、Unique-Cause MC/DC、Masking MC/DCを一つのreportへ
統合するGo向けカバレッジツールです。

## 動作要件

- Go 1.26.5
- Go Modules
- macOSまたはLinux

## インストール

```sh
go install github.com/shrydev2020/gomcdc@latest
```

## 実行

```sh
cd /path/to/your/module
gomcdc test ./...
gomcdc test --format html --output coverage-html ./...
gomcdc version
```

`gomcdc test` は計測対象moduleで実行してください。HTML reportは
`coverage-html/index.html` に出力されます。

reportの意味論と機械可読出力の契約は、[規範仕様](docs/specification.ja.md)、
[英語参考版](docs/specification.md)、
[JSON report schema](schema/report-v1.0.schema.json)を参照してください。

## 開発時の確認

```sh
go test -count=1 ./...
go test -count=1 -race ./...
go vet ./...
```

## ライセンス

MIT Licenseです。詳細は [LICENSE](LICENSE) を参照してください。
