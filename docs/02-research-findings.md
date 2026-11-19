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

## 7. Not researched

- Competitor tools for lint/preflight: Skeema, Atlas, squawk (Postgres), PlanetScale tooling.
- Postgres equivalents (pg_repack, pgroll, reshape, etc.).
- AWS RDS/Aurora specifics.
- gh-ost / pt-osc support for MySQL 8.4 and 9.7.
- Full online-DDL operation tables for 8.4 and 9.7.
