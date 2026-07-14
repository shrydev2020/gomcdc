# gomcdc

[日本語](README.ja.md)

gomcdc combines statement, function, decision, condition, clause body, clause
selection, Unique-Cause MC/DC, and Masking MC/DC coverage in one report.

## Requirements

- Go 1.26.5
- Go Modules
- macOS or Linux

## Install

```sh
go install github.com/shrydev2020/gomcdc@latest
```

## Run

```sh
cd /path/to/your/module
gomcdc test ./...
gomcdc test --format html --output coverage-html ./...
gomcdc version
```

Run `gomcdc test` from the target module. The HTML report is written to
`coverage-html/index.html`.

See the [normative specification](docs/specification.ja.md),
[English reference](docs/specification.md), and
[JSON report schema](schema/report-v1.0.schema.json) for report semantics and
the machine-readable output contract.

## Development

```sh
go test -count=1 ./...
go test -count=1 -race ./...
go vet ./...
```

## License

MIT. See [LICENSE](LICENSE).
