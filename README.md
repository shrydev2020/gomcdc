# gomcdc

[日本語](README.ja.md)

gomcdc is a Go coverage tool for statement, function, decision, condition,
clause body, clause selection, Unique-Cause MC/DC, and Masking MC/DC analysis.

## Requirements

- Go 1.26.5 or a later security-patched Go 1.26 release
- Go Modules
- macOS or Linux

## Install

```sh
go install github.com/shrydev2020/gomcdc/cmd/gomcdc@latest
```

## Run

```sh
gomcdc test ./...
gomcdc test --format html --output coverage-html ./...
```

The HTML report is written to coverage-html/index.html.

Each file page shows the original source bytes with byte-range annotations for
statement, decision, condition, clause, and both MC/DC strategies.

## Development

```sh
go test -count=1 ./...
go test -count=1 -race ./...
go vet ./...
```

## License

No license file is present in this repository. Redistribution and modification
rights are therefore not defined here.
