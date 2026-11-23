# 05. Decisions, positioning, risks, open questions

## 1. Decisions made

| # | Decision | Notes |
|---|---|---|
| D1 | Language: Go; separate repo, not part of the application repo that motivated it | |
| D2 | Tool is generic MySQL (local + GCP), not tied to any particular application repo | |
| D3 | First version is a **linter/preflight** core; execution wraps existing engines | Backfill is a separate dry-run-default subcommand |
| D4 | Input is any tool's SQL via stdin/file (e.g. Alembic `--sql`) | No tool-specific adapters |
| D5 | Replication is **async**; multi-region replicas must be supported | Drives GTID, gating replicas, post-failover re-verify |
| D6 | Gating replicas are **configured by hand** | Others are reported, not waited on |
| D7 | Supported servers: **MySQL 8.4 LTS and 9.7 LTS only**; 8.0 unsupported (EOL) | Classifier keyed by exact version |
| D8 | Cut-over uses a short, configurable lock bound with bounded retries; config lives in the plan file | Atomic RENAME makes failed attempts harmless |
| D9 | Engine choice per table: triggers (pt-osc) as safe default; gh-ost where binlog works and no FKs/triggers | gh-ost is MIT-licensed |
| D10 | Own binlog capture engine is optional and late | Learning goal and open-source goal both want it eventually |
| D11 | Goals: learning **and** an open-source project | Changes priorities: adoption path, hygiene, trust |
| D12 | Plan only until the Docker lab; docs kept in `docs/` | |
| D13 | gh-ost cut-over is gated on `performance_schema.metadata_locks` holders (age above the lock timeout); defaults 1 s lock timeout, 10 attempts | gh-ost owns the cut-over, so zdm holds it with `--postpone-cut-over-flag-file` rather than building its own bounded cut-over (killing gh-ost's waiting session would break it). pt-osc has no gate. See `04-roadmap.md`, `08-cutover-pause.md` |

## 2. Positioning (honest)

- At the engine level this is not different from gh-ost, pt-osc or Vitess. Re-implementing capture is a learning exercise.
- The real value is the **orchestration and safety layer** these tools leave to humans:

| Capability | gh-ost / pt-osc today (our reading; not a full audit) | zdm (planned) |
|---|---|---|
| Lint a migration before running | No | Yes |
| Choose INSTANT / INPLACE / gh-ost / pt-osc per change | No | Yes |
| Preflight (GTID, binlog retention, disk, idle txns, MDL holders) | Partial | Yes, go/no-go report |
| Stall watchdog (gh-ost #1169) | No | Yes |
| External resumable state, inspect-before-act | Limited | Yes |
| Topology fingerprint, pause on failover | No | Yes |
| Swap completes only after gating replicas reach the swap GTID | No | Yes |
| Bounded-retry cut-over from one plan file | Partial | Yes |
| Verification before swap | Separate tool | Yes |
| Post-swap soak with reverse sync and rollback | No | Yes (hardest) |
| Batched, throttled, resumable backfill | No | Yes |

- Vitess bundles much of the orchestration but requires putting MySQL behind it. `zdm` is for people who do not want that platform change.
- Pitch: "Safety and orchestration for online MySQL schema changes. Works with gh-ost, pt-osc, or native DDL."
- Best adoption driver: the offline linter (v0.1).

## 3. Risks

| Risk | Mitigation |
|---|---|
| Trust: tools near production data | Read-only first; dry-run default; explicit destructive flags; publish test evidence |
| Wrong classification (says INSTANT when it is not) | Generate from the manual per version; test every rule on real MySQL in CI |
| Wrapped tools change flags/output | Pin versions; per-version integration tests |
| Async failover data loss | Mandatory post-failover re-verify |
| Cloud SQL restrictions may block gh-ost | Capability probe; pt-osc fallback; run spikes S1, S2, S7 first |
| Silent stalls after provider events | Watchdog and heartbeat |
| Liability if someone loses data | License disclaimer; docs state it is a safety aid, not a guarantee |
| Maintainer burnout | Small scope; wrapper before engine |
| `zdm` name collision | Check before publishing |

## 4. Open questions

1. Do gh-ost and pt-osc work on 8.4 and 9.7 (especially FK tables on 9.7)? (S8)
2. Which privileges does a Cloud SQL user actually have? (S7)
3. Full online-DDL operation tables for 8.4 and 9.7 (S9); do the 8.0-era rules (INSTANT drop needs 8.0.29, 64 row versions) still hold exactly?
4. Does `gtid_mode` default ON on 9.7 and OFF on 8.4? (single issue report)
5. Does `zdm lint` duplicate an existing tool (Skeema, Atlas, others)? Run the competitor survey.
6. Is there enough demand to justify the project beyond learning?
7. Is pt-osc's license compatible with how it will be bundled/called?
8. AWS RDS/Aurora support: in scope later?
9. Parser choice: Vitess `sqlparser` vs TiDB parser on real migration files.

## 5. Conversation timeline (summary)

1. Explained how migrations run from the app layer (Alembic/golang-migrate) to MySQL in a reference codebase, including lock-timeout handling and version-table/schema divergence.
2. Deep dive on production behavior with billions of rows: native online DDL, ghost-table tools, expand/contract, backfill rules.
3. Explained binlog, Galera, pt-osc, undo log, redo log, buffer pool; then with diagrams.
4. Decided to build a tool; clarified approach (linter vs engine), language, scope. Tensions resolved: linter core with opt-in backfill; Go binary with stdin/stdout contract.
5. Challenge: expand/contract alone is not a zero-downtime swap. Extended to shadow -> capture -> backfill -> verify -> swap -> soak -> contract, with honest limits ("zero" = no errors, bounded pause).
6. Short bounded lock for cut-over, configurable once in a plan file.
7. Added primary/secondary, async replication, crash and region-failure handling; GTID over file:position; post-failover re-verify.
8. Clarified who owns replication (DB layer / provider / platform team) and GCP/AWS managed options.
9. Generalized the plan (not tied to the original repo); async + multi-region confirmed.
10. Researched gh-ost (MIT), pt-osc trade-offs, Cloud SQL binlog and flags; decided triggers are a valid default; gating replicas manual; more spikes wanted.
11. Challenged "why build a new tool": settled on orchestration layer plus learning; open-source intent recorded.
12. Compared online vs offline schema change; checked Vitess license (Apache 2.0).
13. Researched newest MySQL: 8.0 EOL, 8.4 LTS, 9.7 LTS, Cloud SQL support; decided **target 8.4 and 9.7 only**.
14. Verified `mysql:8.4` and `mysql:9.7` Docker images exist; created this documentation. Docker 29.1.3, Compose v5.0.1 and Go 1.24.0 are available locally.
