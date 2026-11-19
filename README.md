# zdm

Safety and orchestration for online MySQL schema changes: lint, preflight, resumable state,
replica-aware cut-over, verify, rollback. Designed to work with gh-ost, pt-osc, or native DDL.

> **Status: design phase.** Only the scaffold exists. No commands are implemented yet, and this
> is not safe to point at a production database. It is also a learning project.

**Targets:** MySQL 8.4 LTS and 9.7 LTS. MySQL 8.0 (EOL) is not supported.

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
