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
go install github.com/shrydev2020/gomcdc/cmd/gomcdc@v1.0.0
```

## Run

```sh
gomcdc test ./...
gomcdc test --format html --output coverage-html ./...
gomcdc version
```

The HTML report is written to coverage-html/index.html.

Each file section shows the original source bytes with byte-range annotations for
statement, decision, condition, clause, and both MC/DC strategies.

## Development

```sh
go test -count=1 ./...
go test -count=1 -race ./...
go vet ./...
```

## License

MIT. See [LICENSE](LICENSE).
