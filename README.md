# gomcdc

[日本語](README.ja.md)

gomcdc is a Go coverage tool for statement, function, decision, condition,
clause body, clause selection, Unique-Cause MC/DC, and Masking MC/DC analysis.

The installed command is gocoverage.

## Specification

- Normative specification in Japanese: docs/specification.ja.md
- English reference translation: docs/specification.md

The specification is the source of truth for metric definitions, JSON fields,
thresholds, and completion criteria. This README only describes how to use the
current checkout.

## Status

This repository is not a 1.0 release yet. The current implementation passes the
repository test, race, and vet suites, but two specification gates remain:

- exact case-selection evidence for expression and type switches;
- promotion of the report schema from 1.0-draft to the final version.

Completion means that every item in section 24 of the normative specification is
implemented and covered by the fixture integration suite. Until then, treat
reports as engineering diagnostics, not as certification evidence.

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

The HTML report is written to coverage-html/index.html.

Each file page shows the original source bytes with byte-range annotations for
statement, decision, condition, clause, and both MC/DC strategies. The
Statement, Decision, Condition, and Combined views are CSS-only; the report
does not load JavaScript or external resources.

The command for this checkout is gocoverage test. Run gocoverage test -h to see
the options supported by the installed revision.

## Security

gocoverage builds and runs the target module's tests with the current user's
permissions.

The temporary workspace is not a security sandbox. Run the tool only on code
you trust, do not expose secrets to its tests, and remember that reports may
contain module-relative paths, source expressions, package names, and test
output.

## Development

```sh
go test -count=1 ./...
go test -count=1 -race ./...
go vet ./...
```

## Package boundary

Implementation packages remain under internal/ because this repository
publishes a command, not a supported Go library API. The public interfaces are
the gocoverage command and its report schema.

## License

No license file is present in this repository. Redistribution and modification
rights are therefore not defined here.
