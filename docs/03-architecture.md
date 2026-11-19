# 03. Architecture

Version 3 of the plan (generic MySQL, local + GCP, async replication, multi-region).
Targets **MySQL 8.4 LTS and 9.7 LTS only**. 8.0 is unsupported (EOL).

## 1. Goals and non-goals

**Goals**
- Plan, guard and orchestrate online schema changes; delegate heavy copy/capture to gh-ost, pt-osc or native DDL.
- Lint migrations offline (CI-friendly), preflight live databases read-only.
- Resumable, crash-safe, topology-aware execution with a bounded-retry cut-over, verify and rollback.

**Non-goals for v1**
- Own binlog capture engine (optional, late; this is what gh-ost mostly is).
- Postgres (later, behind a dialect interface added only when needed).
- App-level dual-write helpers.

**"Zero downtime" means:** no failed user requests. Writes on the table may queue at most
`lock_wait_timeout` per cut-over attempt (about 1-3 s). It never means literally zero pause.

## 2. Pipeline

```
 0 PREFLIGHT → 1 SHADOW → 2 CAPTURE → 3 BACKFILL → 4 CATCH-UP → 5 VERIFY → 6 SWAP → 7 SOAK/ROLLBACK → 8 CONTRACT
```

| Phase | Action | Resume key |
|---|---|---|
| 0 Preflight | Lint DDL; check version, `gtid_mode`, `binlog_format=ROW`, `binlog_row_image`, binlog retention, disk, replica lag, idle txns, MDL holders; fingerprint topology | stateless |
| 1 Shadow | Create ghost table with the new schema (empty, instant) | control table |
| 2 Capture | Start capture, record start GTID | GTID position |
| 3 Backfill | Keyset chunks by PK, sleep, lag throttle, checkpoint | last PK |
| 4 Catch-up | Apply captured changes until lag is under threshold | GTID position |
| 5 Verify | Counts, chunk checksums, sampled and recent-change diff | rerunnable |
| 6 Swap | Drain to a GTID boundary, then bounded-retry atomic `RENAME` | idempotent check |
| 7 Soak | Keep old table, reverse sync for N days, `rollback` available | state |
| 8 Contract | Drop old structure (separate command, human gate) | n/a |

Invariants:
- Capture starts **before** backfill. Captured changes are applied after chunk copies and win. Applies are idempotent and ordered by GTID/log position.
- Every phase is resumable; every step inspects the database before acting.
- Nothing destructive (drop, kill, force) without an explicit flag and a human gate.

## 3. Strategy selection (`zdm plan`)

```
 INSTANT-capable?                  → native, pin ALGORITHM=INSTANT; phase 0 + bounded lock only
 secondary index, lag budget ok?   → native INPLACE LOCK=NONE
 otherwise (rebuild / large)       → shadow table:
       FKs, existing triggers, Galera, or no binlog access → pt-osc (triggers)
       else                                                 → gh-ost (reader = replica)
```

- Triggers are the safe default; gh-ost is preferred where binlog access works and the table has no FKs/triggers.
- Cloud SQL: capability probe decides; fall back to pt-osc or refuse with a reason.
- Capture ranking: log-based > triggers > app dual-write (opt-in with warnings).
- Classifier is **keyed by exact server version** (8.4.x, 9.7.x), generated from that version's manual and tested against real MySQL in CI.

## 4. Cut-over

```yaml
cutover:
  lock_wait_timeout: 2s       # session-level, set at connect time (init command), not via a transaction-opening statement
  max_attempts: 20
  backoff: 5s
  deadline: 10m
  max_replica_lag: 2s
  gating_replicas: [a1, b1]   # configured by hand; non-gating replicas are reported, not waited on
  require_gate: true          # human confirm or postpone flag
  on_exhausted: postpone      # postpone | abort | notify
  kill_blockers: false        # explicit opt-in only
```

- `RENAME TABLE` is atomic: a timed-out attempt changes nothing and is safe to retry.
- Before each attempt, inspect `INNODB_TRX` and `metadata_locks` (including FK children); skip and log if a long blocker exists.
- Each failed attempt still queues traffic for up to the bound, so keep it short and use backoff.
- Starvation is possible if a long transaction is always open, hence `max_attempts`, `deadline` and a defined end state.
- Done only when every **gating** replica has executed the swap GTID. Until then replicas may serve the old table, so the app needs a compatibility window.

## 5. Topology and crash safety (async, multi-region)

| Concern | Design |
|---|---|
| Failover can lose recent commits (async) | After any failover, re-verify shadow vs source before swap; never trust saved state |
| Replicas apply the swap later | Gating replicas must reach the swap GTID; non-gating (DR) replicas are reported only |
| Variable cross-region lag | Throttle on the worst gating replica; timeout for dead ones |
| File:position is server-local | All checkpoints use **GTID sets**; `gtid_mode=ON` is a hard requirement |
| Binlog retention may be short | Preflight: retention > estimated migration time x margin; abort early if capture falls behind the purge point |
| Every chunk re-applied on each replica | Estimate write amplification in `plan`; rate-limit |
| Provider maintenance restart | Treat as connection loss; own watchdog (see below) |

Topology fingerprint at plan time: `@@server_uuid`, primary host, `gtid_executed`. A change at any
later step pauses the run and requires a human decision.

Crash matrix:

| Failure | Behavior |
|---|---|
| Tool crash | Resume from checkpoint |
| Crash right after `RENAME` | Inspect the real schema first, then record success; never re-run a rename blindly |
| Primary failover mid-run | Pause, re-fingerprint, re-verify, human confirms |
| Region outage | Same as failover; non-gating replica loss never blocks |
| Binlog purged | Abort, clean up shadow, report retention shortfall |
| Disk filling | Abort, clean up; monitored throughout |
| Dead/lagging replica | Timeout; gating/non-gating config decides |

**Watchdog around any wrapped engine:** progress heartbeat + stall timeout. gh-ost issue #1169 shows
an engine can hang silently after a provider restart. `zdm` must not assume the engine notices.

## 6. Commands

```
zdm lint     <sql|->  [--facts f.json]     offline, CI-friendly
zdm check    --dsn …                       read-only live preflight + capability probe
zdm plan     --table T --alter "…"         writes plan.yaml
zdm run      --plan plan.yaml              phases 1-5, resumable
zdm cutover  --plan plan.yaml              phase 6
zdm verify | rollback | status --plan plan.yaml
zdm backfill plan|run …                    standalone batched data change (dry-run by default)
```

Input is any tool's SQL: `zdm lint -` reads from stdin (for example Alembic `--sql` output, Flyway,
plain `.sql`). No tool-specific adapters.

## 7. Linter rules (v1)

| Rule | Reason |
|---|---|
| Missing `ALGORITHM=INSTANT` pin on add-column | Silent fallback to COPY |
| `LOCK=` clause combined with INSTANT | Rejected by MySQL (error 1221); `LOCK=DEFAULT` is accepted. Verified on 8.4 and 9.7 |
| Two ALTERs on the same table in one migration | Two lock acquisitions |
| `NOT NULL` without default / unapproved default | Permanent default under strict mode |
| Pinned `ALGORITHM` in a downgrade | Can break rollback |
| Type change, PK change, DROP COLUMN, rename | Not INSTANT; expand/contract needed |
| Secondary index on a large table | INPLACE serializes on replicas (lag) |
| Instant-change count near 64 | Needs rebuild (from facts file) |
| FKs referencing a non-unique parent key | Invalid on 8.4+ |
| FKs or Galera present | Steers strategy to pt-osc |
| Giant UPDATE/DELETE without a key range | Belongs in `zdm backfill` |

Findings carry `severity`, `rule`, `statement`, `why`, `fix`. Tests are golden files
(`testdata/<case>.sql` + `<case>.expected.json`).

## 8. Backfill runner

- Keyset: `WHERE pk > :last ORDER BY pk LIMIT :n`, batch about 1k-10k.
- Checkpoint written in the same transaction as the batch.
- Throttles: sleep, gating-replica lag, optional `Threads_running`.
- Idempotent `WHERE` excluding already-migrated rows.
- Dry-run by default; `--execute` to write; hard caps on batch size and runtime; kill switch (flag file or signal).
- Verify report of mismatches.

## 9. Technology

| Area | Choice | Notes |
|---|---|---|
| Language | Go | single static binary, good DB and binlog libraries |
| SQL parsing | Vitess `sqlparser` (alt: TiDB parser) | try both on real migration files; keep it behind `internal/parse` |
| DB access | `database/sql` + `go-sql-driver/mysql` | `caching_sha2_password` support required |
| Binlog (late/optional) | `go-mysql-org/go-mysql` | only if own capture engine is built |
| Engines | gh-ost, pt-osc, native DDL | pinned versions; called as external processes |
| Tests | Go tests + golden files; Docker MySQL 8.4 and 9.7 | failure-injection suite |
| Release | goreleaser, checksums, container image, Homebrew tap | |
| License | Apache 2.0 or MIT | compatible with wrapping gh-ost (MIT) and Vitess (Apache) |

## 10. Layout

```
zdm/
  cmd/zdm/
  internal/
    parse/      Vitess sqlparser wrapper (only place touching the parser)
    classify/   statement + version → INSTANT | INPLACE | COPY
    rules/      lint rules (one file until it grows)
    advise/     strategy choice from facts + capabilities
    topo/       discovery, fingerprint, GTID, replica roles
    preflight/  live checks, capability detection
    engine/     gh-ost / pt-osc / native adapters
    state/      phase machine, control table/file
    backfill/ verify/ cutover/ report/
  testdata/     sql + golden JSON
  docker/       compose: primary + 2 replicas (+ delay injection for a "remote" replica)
  docs/
```

Do not add a `Dialect` interface until a second database is actually started.

## 11. Managed vs self-managed

Preflight discovers capabilities and reports what it cannot do rather than assuming:
privileges (`SHOW GRANTS`), `gtid_mode`, `binlog_format`, `binlog_row_image`, `log_replica_updates`,
binlog retention, and whether a replica exposes binlogs. Cache the result in the plan.
Never change flags automatically (some Cloud SQL flag changes restart the instance); print what is
needed and who must change it. Prefer reading binlog from a replica for gh-ost; make
`--allow-on-master` an explicit opt-in.
