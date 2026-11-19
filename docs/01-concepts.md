# 01. Concepts

Background that shaped the design. Written from general MySQL/InnoDB knowledge unless marked.
Verify exact variable names and defaults against the manual for your version. **[verify]**

## 1. How a migration runs, application to database

Typical Python flow (Alembic), as discussed from a reference codebase:

```
Model (SQLAlchemy Base.metadata)        desired schema
   │ autogenerate or hand-written revision
Revision file                            upgrade()/downgrade(), revision + down_revision (linked list)
   │ `alembic upgrade head`
env.py                                   builds engine, sets lock timeout, imports models
   │ PyMySQL (sync driver; app itself may use an async driver)
MySQL: ALTER TABLE ... ; UPDATE alembic_version
```

Go flow (golang-migrate): paired `NNN_name.up.sql` / `.down.sql` files, version in
`schema_migrations(version, dirty)`. It pins one connection, sets
`SET SESSION lock_wait_timeout`, takes an advisory lock, marks `dirty=true`, runs the SQL,
then `dirty=false`. A failure leaves `dirty=true`; recovery is a manual `force` after
checking which version the schema really matches.

Key point: **MySQL DDL auto-commits**. It cannot be rolled back and it ends any open
transaction. So the version-table update is a separate step and the two can disagree.
Always verify the version table against `information_schema` after a run.

A lock-timeout set via a statement that opens a transaction first can leave the schema
changed while the version-table `UPDATE` rolls back. Setting it as a driver-level
connect-time `init_command` avoids that.

## 2. The four structures behind a write

```
UPDATE t SET x=1 WHERE id=7; COMMIT;

 ① BUFFER POOL (RAM)   page modified in memory → "dirty page"
 ② UNDO LOG            old row version saved (rollback + MVCC)
 ③ REDO LOG            "page P changed bytes B", fsync at commit (durability)
 ④ BINLOG              logical row event, fsync (replication, PITR, CDC)
 ⑤ later: dirty page flushed to the .ibd file in the background
```

| Structure | Level | Purpose | Migration relevance |
|---|---|---|---|
| Buffer pool | InnoDB | RAM page cache, LRU with young/old sublists | A rebuild or index build streams the whole table through it and evicts hot pages; unrelated queries slow down |
| Redo log | InnoDB | Write-ahead log for crash recovery; ring buffer with a checkpoint | A giant UPDATE or backfill can outrun flushing and fill the log, stalling all writes |
| Undo log | InnoDB | Rollback and MVCC old versions; purge removes them | An idle open transaction pins a snapshot, purge stalls, history list grows, reads slow. A failed giant UPDATE takes as long to roll back as it did to run |
| Binlog | Server | Ordered logical changes: replication, PITR, change capture | gh-ost reads it. An INPLACE ALTER is one event, so replicas lag for its whole duration |

**Two-phase commit** keeps redo and binlog consistent: redo PREPARE → binlog write+fsync → redo COMMIT.
After a crash, a transaction commits iff the binlog has its event.

**GTID** gives each transaction a global id, so positions survive failover. File:position is
server-local and meaningless after promoting a replica.

## 3. Metadata locks (the real outage mechanism)

Every statement on a table holds a metadata lock (MDL). `ALTER` needs an **exclusive** MDL
at the start and end.

```
 idle txn T0 holds S lock on t ──────────────────────────────►
 ALTER wants X lock ── WAITING ───────────────────────────────►
 SELECT/INSERT on t ── queue behind the ALTER ────────────────►
```

While the ALTER waits, **every new query on that table queues behind it**, so one idle
transaction turns a harmless ALTER into a table outage. MySQL's default
`lock_wait_timeout` is one year. Bound it (180 s was used in the reference codebase;
for a cut-over use 1-3 s). MySQL 8 also extends the DDL's MDLs to tables whose foreign
keys reference the altered table, so check FK children too.

## 4. Online vs offline DDL

| | Offline | Online |
|---|---|---|
| Table during change | Reads and/or writes blocked | Reads and writes continue (brief locks at start and end) |
| Mechanism | `ALGORITHM=COPY`, `LOCK=SHARED/EXCLUSIVE` | INSTANT, INPLACE `LOCK=NONE`, or shadow-table tools |

Native algorithms:

| Algorithm | Rebuilds data? | Concurrent DML | Example |
|---|---|---|---|
| INSTANT | No (dictionary only) | Yes | Add column |
| INPLACE + LOCK=NONE | Index only, or in-place rebuild | Yes (changes logged and replayed) | Add secondary index |
| COPY | Yes | **No writes** | Many type changes, PK changes |

Facts **[sourced]** (MySQL 8.0 manual, 9.7 manual snippet, MonPG):
- INSTANT is metadata-only; old rows are interpreted through row versions.
- **64 row versions** limit: after that, INSTANT ADD/DROP COLUMN is rejected and a rebuild is needed (still stated in the 9.7 manual).
- INSTANT accepts only `LOCK=DEFAULT`; `LOCK=NONE` is rejected.
- Pin `ALGORITHM=INSTANT` so MySQL fails instead of silently falling back to COPY.
- INPLACE is non-blocking on the primary but **serialized on replicas**, causing replica lag (Vitess docs).
- Adding an auto-increment column blocks concurrent DML.

Even online, you pay in: metadata-lock risk, replica lag, disk and I/O, extra moving parts.

## 5. Ghost-table recipe (gh-ost, pt-osc, Vitess)

```
1 create _t_gho LIKE t; ALTER it (empty, so instant)
2 start capturing changes to t   (triggers or binlog)
3 copy rows in PK-range chunks, throttled
4 apply captured changes in order
5 cut-over: atomic RENAME TABLE swap
6 drop old table
```

| | pt-osc | gh-ost | Vitess VReplication |
|---|---|---|---|
| Capture | Triggers (same transaction) | Binlog (async) | Binlog |
| Overhead on app writes | Yes | None synchronous | None synchronous |
| Real pause | No (triggers keep firing) | Yes | Yes |
| Foreign keys | Supported (strategies) | Not supported | Limited |
| Existing triggers on table | Refuses by default (`--preserve-triggers`) | Not supported | n/a |

## 6. Application protocol: expand, migrate, contract

Tools solve the lock. They do not solve code/schema compatibility. Rule: at every moment
the running code must work with both the current and the previous schema.

```
Expand    add nullable column / new table / new index        old code ignores it
Migrate   dual-write or capture → backfill → verify → switch reads (flag)
Contract  stop writing old → drop old                        point of no return
```

Backfill rules: keyset batches by PK (about 1k-10k rows), sleep between batches, watch
replica lag, resumable via stored last id, never one giant UPDATE, verify mismatches before
switching reads.

App-level dual-write is the weakest capture option: not atomic across two writes, misses
non-app writers (cron, other services, ad hoc SQL, old app versions). Prefer log-based capture.

## 7. Replication, Galera, topology

- Async primary/replica: the DB layer replicates; a provider or DBA team owns failover; the app
  only picks endpoints/pools and handles reconnects and stale reads.
- Async failover can **lose recent commits**. After any failover, re-verify before a swap.
- Replicas apply the swap `RENAME` later than the primary; a lagging replica serves the old table.
- **Galera** is synchronous multi-primary with certification. DDL runs in TOI mode by default and
  blocks the whole cluster's writes; RSU is node-by-node. gh-ost's single-binlog-stream assumption
  does not fit multi-writer clusters, so pt-osc (triggers) is the safe choice there.
- On GCP/AWS the provider runs replication and failover (Cloud SQL HA, RDS Multi-AZ/Aurora).
  Your tooling sees "connection lost, new primary behind the same address".
