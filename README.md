# zdm

Safety and orchestration for online MySQL schema changes: lint, preflight, resumable state,
replica-aware cut-over, verify, rollback. Designed to work with gh-ost, pt-osc, or native DDL.

> **Status: early.** Only `zdm lint` exists (offline, read-only). Nothing here touches a database
> yet. It is also a learning project.

**Targets:** MySQL 8.4 LTS and 9.7 LTS. MySQL 8.0 (EOL) is not supported.

## Usage

```
alembic upgrade head --sql | zdm lint -        # any tool's SQL, or a file path
zdm lint migration.sql --format json --fail-on warning
```

Exit code: 0 clean, 1 findings at or above `--fail-on` (default `error`), 2 usage or read error.
Statements the parser cannot read are reported as `unparsed`, never silently skipped.

## Documentation

Design, research and decisions live in [docs/](docs/README.md):
[concepts](docs/01-concepts.md), [research](docs/02-research-findings.md),
[architecture](docs/03-architecture.md), [roadmap](docs/04-roadmap.md),
[decisions](docs/05-decisions-and-open-questions.md).

## Development

Requires Go 1.24+ and [golangci-lint](https://golangci-lint.run) v2.

```
make build    # ./bin/zdm
make check    # vet + lint + test (same as CI)
make fmt      # gofumpt + goimports
```

## What this is not

It does not replace gh-ost, pt-osc or Vitess. It is the safety layer around them. It is a safety aid,
not a guarantee: always rehearse on production-sized data.

## License

Apache-2.0.
