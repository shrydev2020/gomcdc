# gomcdc

[日本語](README.ja.md)

`gomcdc` is a Go coverage tool for statement, function, decision, condition, clause body, clause selection, Unique-Cause MC/DC, and Masking MC/DC analysis.

The installed command is `gocoverage`.

## Specification

- [Normative specification in Japanese](docs/specification.ja.md)
- [English translation](docs/specification.md)

The Japanese specification is authoritative.

Definitions, metric names, JSON fields, thresholds, and completion criteria belong to the specification and are not duplicated in this README.

## Project status

The repository is under active development and does not yet conform to every requirement in the `1.0-draft` specification.

Known incomplete areas include the compiler-aware Clause Selection backend and the final JSON schema.

Do not use the current output as a certification or safety-compliance claim.

## Requirements

- Go 1.26.5 or a later security-patched Go 1.26 release
- Go Modules
- macOS or Linux

## Install

```sh
go install github.com/shrydev2020/gomcdc/cmd/gocoverage@latest
```

## Run

```sh
gocoverage test ./...
gocoverage test --format html --output coverage-html ./...
```

The HTML report entry point is `coverage-html/index.html`.

Each file page shows the original source bytes with byte-range annotations for
statement, decision, condition, clause, and both MC/DC strategies. The
Statement, Decision, Condition, and Combined views are CSS-only; the report
does not load JavaScript or external resources.

The draft specification defines the final CLI.

Until implementation conformance is complete, use `gocoverage test -h` to inspect the behavior of the checked-out revision.

## Trust boundary

`gocoverage` builds and runs the target module's tests with the current user's permissions.

The temporary workspace is an instrumentation boundary, not a security sandbox.

Run the tool only on code you trust, and do not expose production credentials to untrusted tests.

Reports may contain module-relative paths, source expressions, package names, and test output.

## Development

```sh
go test -count=1 ./...
go test -count=1 -race ./...
go vet ./...
```

## Package boundary

Implementation packages remain under `internal/` because this repository does not publish a supported Go library API.

The intended public contracts are the `gocoverage` command and the versioned report schema after specification conformance.

## License

No license has been added yet.

Do not assume redistribution or modification rights until the repository owner adds a license.
