# 04. Roadmap

## 1. Build order (each step shippable)

1. `parse` + `classify` + core lint rules with golden tests; CI-friendly output (JSON).
2. Remaining rules, `advise`, JSON report.
3. `topo` + `preflight`/`check`, with docker-compose integration tests (primary + replicas; one replica artificially delayed to model cross-region).
4. `state` + gh-ost/pt-osc adapter + `plan/run/status`, with the watchdog.
5. `verify` + `cutover` + `rollback`.
6. `backfill`.
7. Failure injection: kill the tool in each phase, promote a replica, purge binlog, fill disk, lag a replica.
8. Real Cloud SQL validation (capabilities, flags, failover) before any production claim.
9. Postgres only later.

Open-source release stages: v0.1 `lint` (zero risk, instant value), v0.2 `check` (read-only),
v0.3 `plan` + engine wrapper, v0.4+ cut-over/verify/rollback/backfill. Do not ship anything that
mutates production data until failure handling is tested.

## 2. Learning milestones (hands-on first)

| # | Milestone | You learn | Lab |
|---|---|---|---|
| 1 | Local MySQL playground | Docker MySQL 8.4/9.7, primary + 2 replicas, GTID, ROW binlog | Write to primary, read replicas, `SHOW REPLICA STATUS`, break and fix replication |
| 2 | The logs | Buffer pool, redo, undo, binlog | `mysqlbinlog`; watch `INNODB_TRX` and history list length with an open transaction |
| 3 | Online DDL by hand | INSTANT/INPLACE/COPY, MDL | Add a column with each algorithm on a 10M-row table; hold a transaction open and watch an ALTER queue queries; set `lock_wait_timeout` |
| 4 | gh-ost and pt-osc by hand | Ghost-table recipe, cut-over, throttling | Run both on one table; kill mid-run; compare overhead |
| 5 | Parse and classify SQL (Go) | AST walking, rule engines, golden tests | Build `zdm lint` |
| 6 | Live inspection (Go) | `information_schema`, `performance_schema`, GTID sets, fingerprint | Build `zdm check` |
| 7 | State machines | Idempotency, checkpoints, inspect-before-act | Phase machine; test killing at every step |
| 8 | Backfill | Keyset pagination, throttling by lag | 50M rows into a shadow table; kill and resume |
| 9 | Change capture (stretch) | Binlog protocol, row events, ordering | `go-mysql-org/go-mysql` mirror of one table; compare with gh-ost |
| 10 | Verify, cut-over, rollback | Checksums, atomic rename, GTID wait | Inject lag, a long transaction, a failover |

Milestones 1-4 involve no code and build intuition. Milestone 9 is the hard one; wrap gh-ost first.

## 3. Spikes

| # | Spike | Answers | Needs Cloud SQL |
|---|---|---|---|
| S1 | gh-ost against a real Cloud SQL replica (8.4 and 9.7) | Do grants and binlog access work? Which flags? | Yes |
| S2 | pt-osc on Cloud SQL | Is SUPER/TRIGGER available? Trigger limits? | Yes |
| S3 | Forced restart or failover during a gh-ost run | Reproduce the stall from gh-ost #1169; tune the watchdog | Yes |
| S4 | Cross-region replica lag under backfill | Realistic lag numbers for gating | Yes |
| S5 | Local docker: primary + 2 replicas, one delayed | Validate swap-GTID wait and lag gate | No (can build locally) |
| S6 | `lock_wait_timeout` behavior on Cloud SQL; blocked `RENAME` | Confirm the bounded retry loop | Yes |
| S7 | `SHOW GRANTS` for the Cloud SQL default user | Privilege question | Yes |
| S8 | gh-ost and pt-osc on MySQL 8.4 and 9.7, incl. FK tables on 9.7 | Version compatibility | No (local). **Partly done**: gh-ost works on both; pt-osc and FK-on-9.7 open (see 02 §7) |
| S9 | Read full online-DDL tables for 8.4 and 9.7 | Generate the classifier | No |

Run S7 and S1 first: they decide whether the wrapper design holds.

## 4. Before heavy investment

- **Competitor survey:** Skeema, Atlas, squawk, PlanetScale tooling, `pt-table-checksum` etc. Check whether `zdm lint` duplicates one of them.
- **Demand check:** gh-ost issues, MySQL forums, a few DBAs.
- **Name check:** `zdm` on GitHub, registries, trademarks.

## 5. Open-source plan

- License: Apache 2.0 or MIT. Confirm pt-osc licensing before bundling.
- README that sells in 30 s: problem, 10-second demo (`zdm lint` catching a bad ALTER), install, honest "what this is not".
- Docs: concepts, safety guide, per-provider compatibility matrix (only tested claims).
- CI on real MySQL 8.4 and 9.7 (Docker) with a published matrix and failure-injection tests.
- Releases: goreleaser, signed binaries, checksums, Homebrew tap, container image.
- Governance: `CONTRIBUTING.md`, templates, code of conduct, private security reporting address.
- Plan file schema and JSON report are **public contracts**: version them from day one; semver.

## 5b. Status

Done: lint, rules, `check`, docker lab, plan file, state, engine adapter with watchdog, `advise`,
`verify` (chunked checksums of old vs new table, quiesced use only), `rollback`/`finalize` (atomic rename with bounded retries, tested against a real metadata-lock blocker), `cleanup` (list/drop helper tables and triggers a killed engine left), `backfill` (keyset chunks, lag throttle, checkpoint resume, stop file), `plan`/`run`/`status` (verified end to end on 8.4 and 9.7: gh-ost with concurrent writes, pt-osc on FK parent and child tables).
Design change: gh-ost and pt-osc perform their own copy and cut-over, so zdm's phases are
preflight, migrate, post-check. zdm's own forward cut-over (step 5) is not built; rollback and quiesced
verification are. Live-table verification is an open problem (engines do not offer it).

One 100M-row gh-ost run with concurrent writes passed on 9.7 (`07-large-table-run.md`).

**Gated cut-over (gh-ost).** `zdm run` passes `--postpone-cut-over-flag-file`. When gh-ost reports
"Row copy complete", zdm polls `performance_schema.metadata_locks` (joined to `threads` and `innodb_trx`,
`preflight.WaitQuiet`) and removes the flag only when no holder on the table is older than the plan's
`lock_timeout_seconds`. Young holders are ignored: they finish before the swap's lock timeout. Long
autocommit SELECTs are caught too, because the age is the larger of the transaction age and the
processlist time. The wait is unbounded (stop with Ctrl-C). pt-osc has no equivalent and relies on
`lock_wait_timeout`. Measured in `08-cutover-pause.md`.

Defaults are now `lock_timeout_seconds: 1`, `max_attempts: 10` (gh-ost's `--default-retries` also governs
copy retries; its own default is 60). A zdm-owned bounded cut-over was **not** built: gh-ost owns the
cut-over, and killing its waiting session would break it without reimplementing it.

**Not done** (do not claim production readiness): warning when `innodb_online_alter_log_max_size` is small for the write rate (needs a write-rate estimate, and zdm has no INPLACE path yet); Cloud SQL spikes S1-S4, S6, S7 (need a real instance);
failure injection for binlog purge, failover or promotion, full disk; live-table verification; lint
rules that need live facts (row counts, replica state); zdm's own forward cut-over; goreleaser config;
the CI integration matrix has never run (it only runs on GitHub); no remote repository yet; name clash
with DataStax `zdm-proxy` unresolved (see `06-landscape.md`).

## 6. Immediate next step (historical, milestone 1 is done)

Milestone 1: create a Docker lab in this repo (`docker/`): MySQL 8.4 and 9.7, one primary and two replicas,
GTID on, `binlog_format=ROW`, `binlog_row_image=FULL`, replicas read-only, an injectable delay on one
replica. Both `mysql:8.4` and `mysql:9.7` images were confirmed to exist.
