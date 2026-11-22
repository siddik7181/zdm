# 7. Large-table run: 100M rows, gh-ost through zdm

Run by hand on 2026-11-22 on a laptop (12 CPUs, 32 GB RAM for Docker). **[spike]** One run, one table shape,
one machine. It shows the logic holds at 100M rows; it does **not** show behaviour on billions of rows,
production hardware, real network latency, or Cloud SQL.

## Setup

| | |
|---|---|
| Server | MySQL 9.7, Docker lab: 1 primary, 2 replicas (`docker/compose.yml`), GTID, ROW binlog |
| Lab tuning | `innodb_buffer_pool_size=3G`, `innodb_flush_log_at_trx_commit=2`, `sync_binlog=0` (set at runtime for the load; **not** production-safe settings) |
| Table | `big.t (id INT PK, v INT)`, **100,000,000 rows**, 2.4 GB (data + index) |
| Load | `docker/load-big.sh`: 1M rows per transaction, 6m52s |
| Change | `ADD COLUMN c INT NULL` (planned by `zdm plan`, engine chosen: gh-ost) |
| Command | `zdm run --execute --behind-proxy --replica-dsn <replica1> plan.json` |
| Concurrent load | A stored procedure inserting one row and updating one hot row every ~10 ms, 60,000 iterations, running through the copy |

## Results

| Measure | Result |
|---|---|
| Total time | **22m53s** (copy 22m27s, about 74k rows/s) |
| Rows copied | 100,000,506 of 100,000,506 (gh-ost's count includes concurrent inserts) |
| Binlog events applied during copy | 118,988 |
| Replica lag during copy | 0.04 to 0.4 s once running; **19.9 s at start**, see below |
| Cut-over | About 1 s (magic-table cut-over, 6 s idle timeout, no retries) |
| zdm phases | preflight, migrate, verify (post-check): all passed |
| Disk | MySQL data dir 7.6 GB on the primary after the run (new table, kept old table, binlogs) |

### Correctness checks (all passed)

- Primary `big.t`: **100,060,000** rows = 100,000,000 + 60,000 concurrent inserts. None lost.
- Replica 1 `big.t`: 100,060,000 rows. Matches the primary.
- Old table `big._t_del` (kept, as the plan says): 100,060,000 rows, identical to the new table.
- Column `c` exists on the new table.
- Checksum over the 99,999,999 original rows (excluding the hot updated row), `BIT_XOR(CRC32(CONCAT_WS(',',id,v)))`, on the new and the old table: **identical** (`1682671320`). The query took 54 s for both tables.
- No helper tables left except the deliberately kept `_t_del`.

### The 19.9 s lag at start

The replica was still applying the load's binlog when the run began, so gh-ost reported 19.9 s lag and
**throttled itself** (8 throttled samples) until the replica caught up. Then it ran unthrottled. So the
throttle works, and a run started while a replica is behind is slow to start, not unsafe. Our preflight
passed because the lag check looks at `Seconds_Behind_Source` at that moment and the load had just ended.

## Same table with pt-online-schema-change 3.7.2

Same lab and table (100M rows, now with column `c`), `ADD COLUMN d INT NULL`, engine forced to pt-osc in the
plan, `--replica-dsn` replica 1 for lag checks. The concurrent writer ran for the whole copy
(one insert and one hot-row update every ~10 ms).

| Measure | Result |
|---|---|
| Total time | **11m43s**, about twice as fast as gh-ost's 22m53s on the same table |
| Cut-over | Swap finished within 1 s of "Copied rows OK" |
| Concurrent writes | 42,978 inserts into the new table (ids contiguous 200000000..200042977, none lost); 38,935 reached the old table before the swap, the rest after |
| Replica 1 | 100,042,978 rows, matches the primary |
| Checksum | Same `BIT_XOR(CRC32(...))` over the 99,999,999 original rows on `t` and `_t_old`: identical (`1682671320`) |
| Leftovers | No triggers left; only the kept `_t_old` remains |
| Disk | 21 GB free at the end (from 56 GB at the start, with both runs, the kept old tables and 3 servers) |

Why gh-ost took longer is not established here. One likely factor is that gh-ost applies the concurrent
binlog events through the same applier while copying, and throttled at the start. Do not read this as
"pt-osc is faster": its triggers add cost to every write on the live table, which this run did not measure
(the writer's own latency was not recorded). **Open question:** measure write latency on the live table
during each engine.

## Not shown by this run

- Billions of rows, or rows much wider than 8 bytes of payload (a wide table copies much more slowly).
- A replica in another region, or any real network latency.
- A write load heavier than about 100 writes a second.
- Cloud SQL, its privileges, or its binlog retention.
- Write latency on the live table during either engine (not recorded).
- `zdm verify` at this size. The chunked checksum was not run here; the SQL checksum above stands in for it.
- A failure at 100M rows (kill, replica stop, primary restart). Those were tested at about 1M rows only.

## Reproduce

```
make lab-up MYSQL_VERSION=9.7
# raise the buffer pool etc. as in the table above, on all three servers
docker/load-big.sh                      # ROWS=100000000 by default; needs about 25 GB free disk
zdm plan --dsn ... --schema big --table t --alter "ADD COLUMN c INT NULL" --out plan.json
zdm run --execute --behind-proxy --dsn ... --replica-dsn ... plan.json
```
