# Cut-over pause with live writers (lab)

`docker/measure-pause.sh <engine>` runs writers that insert into the table and log each insert's latency,
then runs `zdm run --execute` against the same table. Phases: `base` (before), `run` (during the whole
migration, copy plus swap) and `after`. `docker/worst-case.sh` runs the scenarios below.

Lab: MySQL 8.4, one primary and one replica in Docker on a laptop, 1M-row table, `ADD COLUMN`.
Defaults under test: `lock_timeout_seconds: 1`, `max_attempts: 10`.

## Results

Insert latency, `run` phase. "Gate polls" is how often the metadata-lock gate found a holder and waited.

| Engine | Scenario | zdm run | Gate polls | p50 | p99 | max | >100 ms | >500 ms |
|---|---|---|---|---|---|---|---|---|
| gh-ost | normal (4 writers) | ok, 21 s | 0 | 1.7 ms | 10.6 ms | 137 ms | 5 | 0 |
| gh-ost | heavy (8 writers) | ok, 179 s | 3 | 2.3 ms | 13.4 ms | 862 ms | 54 | 8 |
| gh-ost | 60 s open transaction | ok, 62 s | 8 | 1.0 ms | 16.8 ms | 804 ms | 41 | 8 |
| gh-ost | 60 s long SELECT | ok, 62 s | 9 | 0.8 ms | 10.6 ms | 1073 ms | 18 | 8 |
| pt-osc | normal | ok, 41 s | 0 | 4.2 ms | 39.3 ms | 177 ms | 10 | 0 |
| pt-osc | 60 s open transaction | **failed**, 23 s | n/a (no gate) | 0.9 ms | 51.2 ms | **1879 ms** | 44 | 40 |

- With a long holder, the gate kept gh-ost from asking for the swap until the holder ended (8 to 9
  polls, run time about the holder length). Both migrations completed.
- pt-osc had no gate when these rows were measured. With a 60 s open transaction it hit `lock_wait_timeout`, stalled writes (40 inserts
  over 500 ms), and aborted with "`lat`.`t` was not altered". The table was left unchanged, but the attempt failed.
- Writes were never blocked for more than about 1 s in these runs.

## Limits (read before quoting these numbers)

- Single local run per scenario, no repeats. The `base` phase of some scenarios also shows
  spikes of 500 ms to 1.1 s (laptop noise), so a `max` in the `run` phase is not necessarily the swap.
- The `run` phase covers the whole copy, not only the swap. The swap pause was not isolated, and the 500 ms+ spikes
  were not attributed to the swap, row copy or lab noise.
- Not run: pt-osc heavy writes (over 20 minutes, the replica could not keep up), and every
  delayed-replica scenario (`REPLICA_DELAY`; `SOURCE_DELAY` is in whole seconds and gh-ost throttles at 2 s lag,
  so it crawls). Heavy writes at 16 writers with no sleep made gh-ost throttle forever for the same reason.
- Small table, Docker on a laptop, MySQL 8.4 only, `ADD COLUMN` only. Nothing here predicts production
  or Cloud SQL numbers.
