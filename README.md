# zdm

Safety and orchestration for online MySQL schema changes: lint, preflight, resumable state,
replica-aware cut-over, verify, rollback. Designed to work with gh-ost, pt-osc, or native DDL.

> **Status: pre-release, not production-ready.** Tested only on a local Docker lab (MySQL 8.4 and 9.7,
> one primary, two replicas) with gh-ost 1.1.11 and pt-osc 3.7.2, up to a 100M-row table
> ([results](docs/07-large-table-run.md)). **Not tested on Cloud SQL**, on billions of rows, or through a
> failover or binlog purge. Try it on a copy first. It is also a learning project.

**Targets:** MySQL 8.4 LTS and 9.7 LTS. MySQL 8.0 (EOL) is not supported.

## Usage

```
alembic upgrade head --sql | zdm lint -        # any tool's SQL, or a file path
zdm lint migration.sql --format json --fail-on warning
```

Exit code: 0 clean, 1 findings at or above `--fail-on` (default `error`), 2 usage or read error.
Statements the parser cannot read are reported as `unparsed`, never silently skipped.

```
export ZDM_DSN='user:pass@tcp(primary:3306)/'     # keeps the password out of `ps`
zdm check --replica-dsn 'user:pass@tcp(replica1:3306)/' --max-lag 10
```

`zdm check` is a read-only preflight: version (8.4 / 9.7), GTID and binlog settings, binlog
retention, long-open transactions, and replication state and lag of every replica you list.
Its connections set `transaction_read_only=1`, so the server refuses writes. Needs the `PROCESS`
and `REPLICATION CLIENT` privileges. Run the lab tests with `make lab-up && make test-integration`.

```
zdm plan --schema app --table orders --alter "ADD COLUMN note VARCHAR(64) NULL" --out plan.json
zdm run plan.json --replica-dsn 'user:pass@tcp(replica1:3306)/'            # dry run: preflight + engine dry run
zdm run plan.json --replica-dsn '...' --execute                            # really run; resumable
zdm status plan.json
```

`plan` inspects the table and picks the engine: gh-ost, or pt-online-schema-change when the table has
foreign keys, triggers or runs on Galera; it refuses tables with no primary or non-null unique key.
`run --execute` goes preflight, engine (copy and cut-over), post-check, saving progress to
`plan.json.state` after each phase. Re-running after a crash resumes at the failed phase. The engine
binary (`gh-ost` or `pt-online-schema-change`) must be on `PATH`. Credentials go to the engine through a
0600 temp file, never the command line. The post-check confirms the table exists, no helper tables
are left, and the old table is kept (or dropped) as the plan says. It does **not** compare row data yet.
```
zdm verify plan.json                                   # checksum the new table against the old one
zdm rollback plan.json --discard-writes-since-swap     # put the old table back
zdm finalize plan.json --drop-old-table                # drop it for good, once you are sure
```

```
zdm backfill --dsn ... --schema S --table T --set "c = 1" --where "c IS NULL" --replica-dsn ...   # dry run: counts
zdm backfill ... --execute                                                                       # really update
```

`backfill` updates existing rows in primary-key chunks (default 1000), sleeping between chunks and
waiting while any `--replica-dsn` is more than `--max-lag` behind. Unknown lag stops the run; it is never
treated as zero. It resumes from `--checkpoint`, and creating `--stop-file` stops it cleanly. `--set` must
be idempotent: the last chunk may run again after a crash.

If zdm or the engine is killed mid-run, the engine's helper objects stay behind: gh-ost's `_t_gho` and
`_t_ghc`, and for pt-osc `_t_new` plus three triggers that keep slowing every write to the live table.
`zdm cleanup plan.json` lists them; `zdm cleanup --engine-is-stopped plan.json` drops them (only after
you checked the engine is not running). gh-ost leftovers are dropped automatically when `run --execute`
resumes; pt-osc needs `cleanup` first.

`verify` reads only (read-only session). It checksums both tables chunk by chunk over their common
columns, in primary-key order, and prints the key ranges that differ. It is only meaningful while
neither table is changing: after a live change, rows written since the swap are expected to differ.
The checksum is CRC32-based, so it catches corruption and missing rows, not a deliberate collision.

`rollback` is one atomic `RENAME TABLE` under a `lock_wait_timeout` set at connect time (the plan's
`lock_timeout_seconds`). A blocked attempt changes nothing and is retried up to `max_attempts`; the
schema is inspected before each attempt, so re-running after a crash is safe. The rejected table is
parked as `_<table>_rolled_back`, never dropped. Rows written since the change exist only in the
parked table, hence the required flag. Both commands need write privileges.

Integration tests need the engines on `PATH`: `make lab-up && make test-integration`.

## Documentation

Design, research and decisions live in [docs/](docs/README.md):
[concepts](docs/01-concepts.md), [research](docs/02-research-findings.md),
[architecture](docs/03-architecture.md), [roadmap](docs/04-roadmap.md),
[decisions](docs/05-decisions-and-open-questions.md).

## Development

Requires Go 1.26+ and [golangci-lint](https://golangci-lint.run) v2.

```
make build    # ./bin/zdm
make check    # vet + lint + test (same as CI)
make fmt      # gofumpt + goimports
make e2e      # Docker: MySQL 8.4 and 9.7 lab, integration + crash/failure tests (needs Docker, percona-toolkit)
```

Docs: [docs/](docs/README.md) has the design, research findings, roadmap, landscape and the 100M-row run.

## What this is not

It does not replace gh-ost, pt-osc or Vitess. It is the safety layer around them. It is a safety aid,
not a guarantee: always rehearse on production-sized data.

## License

Apache-2.0.
