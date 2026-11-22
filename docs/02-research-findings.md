# 02. Research findings

Gathered 2026-11-19 via web search. Tags: **[sourced]** (named source), **[verify]**, **[spike]**.
Nothing here was run against a real Cloud SQL instance.

## 1. Licenses

| Project | License | Evidence |
|---|---|---|
| gh-ost | MIT, copyright GitHub | Fetched `LICENSE` from the repo **[sourced]** |
| Vitess | Apache 2.0 | Fetched `LICENSE` from the repo **[sourced]** |
| pt-online-schema-change (Percona Toolkit) | Open source; exact license not fetched | **[verify]** before bundling. Calling it as an external binary avoids most issues |

Free software does not mean free to run: Vitess needs a full routing layer, and its online DDL
is only safe when traffic goes through Vitess (VReplication docs) **[sourced]**.

## 2. gh-ost vs pt-osc

| | pt-osc | gh-ost |
|---|---|---|
| Capture | Triggers, same transaction | Binlog, async; acts like a replica |
| Foreign keys | `--alter-foreign-keys-method`; FK names get a leading underscore; self-referencing FKs unsupported | Not supported; aborts unless `--discard-foreign-keys` removes them on the new table |
| Existing triggers | Refuses unless `--preserve-triggers` | "Triggers are not supported" |
| Privileges | PROCESS, SUPER, REPLICATION SLAVE + table privs incl. TRIGGER | Replication privileges; `--assume-rbr` skips the step that needs SUPER |
| Needs | PK or unique key | Shared PK/unique key; `binlog_format=ROW`, `FULL` row image |
| Throttling | Triggers keep firing when copy is throttled | True pause |

Sources: gh-ost `requirements-and-limitations.md` and README, Percona docs, mironsoft and bigiron guides.
Rough timing from one blog (100 GB table: native 1-2 h, gh-ost 2-4 h, pt-osc 3-5 h) is a secondary
source, not a benchmark **[verify]**.

Selection rule from the sources: FKs, existing triggers or Galera -> pt-osc; otherwise gh-ost where binlog
access exists; skip both when native INSTANT/INPLACE qualifies or the table is small.

## 3. Cloud SQL

| Finding | Source |
|---|---|
| Binary logging is supported on read replicas (MySQL 5.7 and 8.0 at the time of the doc). Enabled via the same API as the primary; "enable binary logging" and "enable PITR" are interchangeable | Cloud SQL replication docs **[sourced]** |
| Primary needs PITR/binlog enabled to support read replicas | same |
| Replicas `log_slave_updates=ON` by default; `binlog_format=ROW` and `binlog_row_image=FULL` defaults | OLake guide, secondary **[verify]** |
| Datastream CDC doc lists flags for a binlog reader: `binlog_row_image=full`, `net_read_timeout`, `net_write_timeout`, `wait_timeout`, `max_allowed_packet` | Google Datastream docs **[sourced]** |
| Only listed flags are supported; some flag changes restart the instance or remove it from the SLA | Cloud SQL flags docs **[sourced]** |
| gh-ost issue #770: users asked whether gh-ost can run on Cloud SQL; a comment says replica binary logging is now possible | GitHub issue **[sourced]** |
| gh-ost issue #1169: on a Cloud SQL 5.7 primary (`--allow-on-master`), a maintenance restart left gh-ost hung: errors about the lost connection, never reconnected, never exited | GitHub issue **[sourced]** |
| Whether SUPER / REPLICATION SLAVE / REPLICATION CLIENT are available to Cloud SQL users | **Not found** **[spike S7]** |
| Cloud SQL versions: MySQL 8.4 is the default (8.4.11), 9.7 listed (9.7.2, Aug 6 2026); EOL majors enrolled in paid extended support since May 1 2025 | Cloud SQL version policy **[sourced]** |

## 4. MySQL versions (as of Nov 2026)

| Version | Status |
|---|---|
| 8.0 | End of life; active support ended April 2026 (8.0.46); Oracle Sustaining Support after |
| 8.4 LTS | Current LTS |
| 9.7 LTS | New LTS, reached via 8.4 (Oracle documents consecutive-LTS upgrades) |
| 9.0-9.6 | Innovation releases |

Changes that matter for tooling:

- **Replication syntax (8.4):** `CHANGE MASTER TO`, `SHOW SLAVE STATUS` and other MASTER/SLAVE statements removed. Use `CHANGE REPLICATION SOURCE TO`, `SHOW REPLICA STATUS` **[sourced]** (MySQL 8.4 "what is new").
- **FK parent key (8.4):** FKs must reference a unique key in the parent (Percona summary) **[sourced]**.
- **Auth:** `mysql_native_password` disabled by default in 8.4, removed in 9.x; use `caching_sha2_password` **[sourced]** (Percona, mysql-server issue #838).
- **FKs and binlog (9.7):** FK constraints and cascades are handled by the SQL layer; cascade changes are logged as row events **[sourced]** (9.7 release notes). Impact on gh-ost's FK limitation: unknown **[spike]**.
- **GTID defaults:** one GitHub issue reports `gtid_mode` / `enforce_gtid_consistency` default OFF on 8.4 and ON on 9.7 **[verify]**.
- **9.7 additions:** new GTID set library; `replica_allow_higher_version_source` for cross-version replication.
- **8.4.9:** dropping columns on tables with virtual columns using `LOCK=NONE` now works, an example of rules shifting between minors.
- **Online DDL (9.7 manual snippet):** add column INSTANT yes; drop column INSTANT yes; rename column INSTANT yes; reorder columns no; rename table INSTANT yes. Row-version limit still exists. The full 8.4/9.7 tables were **not read** (the MySQL site returned 403; one fetch was rejected) **[verify]**.

## 5. Vitess

- DDL strategies: `vitess` (VReplication, preferred), `gh-ost`, `pt-osc`, plus native MySQL ones.
- gh-ost and the `vitess` strategy do not support foreign keys (older doc version), VReplication has a brief write outage at cut-over, and is only safe when all traffic goes through Vitess **[sourced]**.
- Useful as a design reference: its online DDL executor and VReplication code.

## 6. Zero-downtime protocol sources

Expand/migrate/contract guidance (Erzon, Encore): old and new code must work with both schemas;
backfill in 1k-10k batches with pauses, resumable, verified before switching reads; contract is the
point of no return. Rehearse on production-size staging to learn lock and backfill times.

## 7. Spike S8 (local, run by hand): gh-ost 1.1.11 on MySQL 8.4.13 and 9.7.4 **[spike]**

Lab: docker primary + 2 GTID replicas, 50k-row table, gh-ost run on a replica with
`--assume-master-host` pointing at the primary (binary SHA-256 checked against the release's SHA256SUMS).

- `ADD COLUMN` completed on **9.7.4** and **8.4.13**; the new column appeared on the primary and replicas.
  The cut-over lock-and-rename lasted about 1s on the tiny table.
- gh-ost aborts on a table with child-side or parent-side foreign keys (checked on 8.4 only).
  This backs the rule "FKs present means use pt-osc". FK tables on 9.7 are **not** tested.
- Behind Docker port mapping gh-ost fails with `unexpected database port reported: 3306`;
  `--skip-port-validation` fixes it. Expect the same behind proxies, so the wrapper must expose it.
- `--switch-to-rbr` and `--assume-rbr` are mutually exclusive.
- gh-ost leaves the old table as `_t_del` unless `--ok-to-drop-table`; the wrapper must report it.
- Not tested: pt-osc on either version, load, throttling, failure injection.

## 7b. Spike: pt-online-schema-change 3.7.2 through zdm **[spike]**

Run by hand, then by the integration test, against the lab on MySQL 8.4 and 9.7.
- Adding a column to an FK child table and to an FK parent table both worked under concurrent
  inserts; the child's foreign key survived (`--alter-foreign-keys-method=rebuild_constraints`).
- There is no `--lock-wait-timeout`; the bound is `--set-vars lock_wait_timeout=N`.
- Replica auto-discovery (`processlist,hosts`) finds replicas with an empty host behind Docker/NAT and
  aborts the real run (the dry run does not notice). zdm uses `--recursion-method=none` plus an
  explicit `--check-replica-lag=h=...,P=...,F=credfile`.
- pt-osc writes, so it must connect to the primary. gh-ost is the one that reads from a replica.
- With `--no-drop-old-table` the old tables (`_child_old`, `_parent_old`) keep their foreign keys,
  so they still constrain the live parent until someone drops them.
- A dry run is not a faithful rehearsal: two of the bugs above only showed in the real run.

## 7c. Failure injection: backfill on MySQL 9.7 **[spike]**

200,000-row table, chunk 500, run by hand on the lab.

| Injection | Result |
|---|---|
| `kill -9` mid-run, then rerun | 100,500 rows done, checkpoint at key 100000. The rerun updated the remaining 99,500 rows and finished; all 200,000 rows set. Nothing lost, nothing done twice beyond one idempotent chunk. |
| Replica SQL thread stopped | zdm refuses to continue: "cannot read replica lag, refusing to continue blind". It does not treat the lag as zero. |
| Replica fully stopped (`STOP REPLICA`) | Same refusal. |

Engine killed mid-`run` (1.5M rows, `kill -9` of zdm, 9.7):

| Engine | Left behind | Resume |
|---|---|---|
| gh-ost | `_t_gho`, `_t_ghc`; the process died with zdm. Live table untouched. | First attempt failed: `table _t_gho already exists`. Fixed: a resumed run passes `--initially-drop-ghost-table --initially-drop-socket-file`. Then completed, 1.5M rows, new column present. |
| pt-osc | `_t_new` and **three triggers on the live table**, still firing on every write. | Fails: "has triggers but --preserve-triggers was not specified". No force flag exists, so `zdm cleanup` was added; after it, the rerun completed. |

A killed client's `INSERT ... SELECT` keeps running on the MySQL server and holds a metadata lock, so the first
`cleanup` hit error 1205 on `DROP TABLE _t_new`. Found by the automated crash test (`make e2e`); cleanup now retries
lock timeouts and reports only objects it really dropped.

The throttle itself (waiting while lag is over `--max-lag`) is covered by the integration test against the
delayed replica. Not yet injected: binlog purge, failover or promotion, full disk, engine kill during `run`.

## 8. Not researched

- Competitor tools for lint/preflight: Skeema, Atlas, squawk (Postgres), PlanetScale tooling.
- Postgres equivalents (pg_repack, pgroll, reshape, etc.).
- AWS RDS/Aurora specifics.
- gh-ost with FK tables on 9.7; pt-osc licence terms for redistribution.
- Full online-DDL operation tables for 8.4 and 9.7.
