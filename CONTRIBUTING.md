# Contributing

Contributions are licensed under the [Apache License, Version 2.0](LICENSE), as section 5 of the license describes;
there is no separate contributor agreement.
Report security issues through the [security policy](SECURITY.md), not a public issue.

## Before you start

Bug fixes, tests and documentation corrections are welcome as pull requests.
For changes to the exported API, trust boundaries, error policy or compatibility, open an issue first.
Those decisions are recorded in [docs/decisions](docs/decisions/README.md) and approved before implementation.
The [support matrix](docs/support.md) separates implemented behavior from deferred scope.

## Development

Use Go 1.27 or later;
the module keeps its `go 1.27.0` baseline.
Run these from the repository root before opening a pull request:

```bash
gofmt -l .
go vet ./...
go mod verify
go test -race ./...
sh scripts/check-consumer.sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./...
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...
git diff --check
```

`gofmt -l .` should print nothing.
Tests use the standard `testing` package, local HTTP test servers and synthetic fixtures.
Never commit real credentials, and do not call live AWS services from unit tests.

## Style

Follow [Effective Go](https://go.dev/doc/effective_go) and the [Google Go style guide](https://google.github.io/styleguide/go/).
Do not wrap code or prose at a fixed column.
In Markdown and comments, put each sentence on its own line, and break after a semicolon between independent clauses.

## Commits and pull requests

Pull requests are squash-merged, so the pull request title becomes the commit message.
Write it as a [Conventional Commit](https://www.conventionalcommits.org/): `feat:`, `fix:`, `docs:`, `refactor:` and so on.
Mark a breaking change with `!` in the title, such as `refactor!: rename X`, so Release Please records it.
Describe the resulting behavior, how you validated it and any limitations.

Release Please owns CHANGELOG.md, versions and tags;
do not edit them by hand.
[AGENTS.md](AGENTS.md) records the full working agreement used in this repository.
