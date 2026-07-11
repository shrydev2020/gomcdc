# gomcdc

[English](README.md)

gomcdcは、statement、function、decision、condition、clause body、
clause selection、Unique-Cause MC/DC、Masking MC/DCを計測するGo向け
カバレッジツールです。

## 動作要件

- Go 1.26.5以降のGo 1.26系列セキュリティ修正版
- Go Modules
- macOSまたはLinux

## インストール

```sh
go install github.com/shrydev2020/gomcdc/cmd/gomcdc@latest
```

## 実行

```sh
gomcdc test ./...
gomcdc test --format html --output coverage-html ./...
```

HTML reportは coverage-html/index.html に出力されます。

各file pageには、元source bytesへstatement、decision、condition、clause、
両MC/DCのbyte-range annotationを重ねて表示します。

## 開発時の確認

```sh
go test -count=1 ./...
go test -count=1 -race ./...
go vet ./...
```

## ライセンス

MIT Licenseです。詳細は [LICENSE](LICENSE) を参照してください。
