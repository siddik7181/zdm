#!/usr/bin/env bash
# Measures insert latency on a live table before, during and after a zdm run, with writers running
# the whole time, so a cut-over pause shows up as a latency spike in the "run" phase.
# Needs the lab (make lab-up), ./bin/zdm, and gh-ost / pt-online-schema-change on PATH.
#
# Usage: docker/measure-pause.sh gh-ost|pt-osc
# Knobs (env): ROWS=1000000 WRITERS=4 WRITE_SLEEP=0.005 BASELINE_SECONDS=10
#   HOLD_MODE=txn|select  HOLD_SECONDS=N   a session holds a lock on the table from the start of the run
#   REPLICA_DELAY=N                        replica1 applies events N seconds late during the run
set -uo pipefail

ENGINE=${1:?engine: gh-ost or pt-osc}
ROWS=${ROWS:-1000000}
WRITERS=${WRITERS:-4}
WRITE_SLEEP=${WRITE_SLEEP:-0.005}
BASELINE_SECONDS=${BASELINE_SECONDS:-10}
HOLD_MODE=${HOLD_MODE:-txn}
HOLD_SECONDS=${HOLD_SECONDS:-0}
REPLICA_DELAY=${REPLICA_DELAY:-0}
M="docker exec -i docker-primary-1 mysql -uroot -proot --batch --skip-column-names"
R="docker exec -i docker-replica1-1 mysql -uroot -proot --batch --skip-column-names"
PRIMARY='root:root@tcp(127.0.0.1:33306)/'
REPLICA='root:root@tcp(127.0.0.1:33307)/'
DIR=$(mktemp -d)
PLAN=$DIR/plan.json
pids=()

cleanup() {
	$M 2>/dev/null -e "UPDATE latlog.ctl SET stop=1" || true
	((${#pids[@]})) && kill "${pids[@]}" 2>/dev/null
	[ "$REPLICA_DELAY" != 0 ] && $R 2>/dev/null -e "STOP REPLICA; CHANGE REPLICATION SOURCE TO SOURCE_DELAY=0; START REPLICA" || true
	$M 2>/dev/null -e "DROP DATABASE IF EXISTS lat; DROP DATABASE IF EXISTS latlog" || true
	rm -rf "$DIR" /tmp/gh-ost.lat.t.sock
}
rm -f /tmp/gh-ost.lat.t.sock
trap cleanup EXIT

$M 2>/dev/null <<EOF
DROP DATABASE IF EXISTS lat; DROP DATABASE IF EXISTS latlog;
CREATE DATABASE lat; CREATE DATABASE latlog;
CREATE TABLE lat.t (id BIGINT AUTO_INCREMENT PRIMARY KEY, ts DATETIME(6) NOT NULL, pad INT NOT NULL) ENGINE=InnoDB;
CREATE TABLE latlog.l (id BIGINT AUTO_INCREMENT PRIMARY KEY, phase VARCHAR(8), dur_us BIGINT);
CREATE TABLE latlog.ctl (stop INT, phase VARCHAR(8));
INSERT INTO latlog.ctl VALUES (0, 'base');
EOF
for _ in $(seq 1 $(((ROWS + 499999) / 500000))); do
	$M 2>/dev/null -e "SET SESSION cte_max_recursion_depth=1000000; INSERT INTO lat.t(ts,pad) WITH RECURSIVE s(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM s WHERE n<500000) SELECT NOW(6), n FROM s"
done
$M 2>/dev/null <<EOF
DROP PROCEDURE IF EXISTS latlog.w;
DELIMITER //
CREATE PROCEDURE latlog.w()
BEGIN
  DECLARE a DATETIME(6); DECLARE b DATETIME(6); DECLARE ph VARCHAR(8);
  WHILE (SELECT stop FROM latlog.ctl) = 0 DO
    SELECT phase INTO ph FROM latlog.ctl;
    SET a = NOW(6);
    INSERT INTO lat.t(ts, pad) VALUES (a, 1);
    SET b = NOW(6);
    INSERT INTO latlog.l(phase, dur_us) VALUES (ph, TIMESTAMPDIFF(MICROSECOND, a, b));
    DO SLEEP($WRITE_SLEEP);
  END WHILE;
END//
DELIMITER ;
EOF
echo "[$ENGINE rows=$($M 2>/dev/null -e 'SELECT COUNT(*) FROM lat.t') writers=$WRITERS sleep=${WRITE_SLEEP}s hold=$HOLD_MODE/${HOLD_SECONDS}s replica_delay=${REPLICA_DELAY}s]"

for _ in $(seq 1 "$WRITERS"); do
	$M 2>/dev/null -e "CALL latlog.w()" &
	pids+=($!)
done
sleep "$BASELINE_SECONDS"
$M 2>/dev/null -e "UPDATE latlog.ctl SET phase='run'"

if [ "$HOLD_SECONDS" != 0 ]; then
	if [ "$HOLD_MODE" = select ]; then
		$M 2>/dev/null -e "SELECT SLEEP($HOLD_SECONDS) FROM lat.t LIMIT 1" &
	else
		$M 2>/dev/null -e "START TRANSACTION; SELECT id FROM lat.t LIMIT 1; DO SLEEP($HOLD_SECONDS); COMMIT" &
	fi
	pids+=($!)
fi
if [ "$REPLICA_DELAY" != 0 ]; then
	$R 2>/dev/null -e "STOP REPLICA; CHANGE REPLICATION SOURCE TO SOURCE_DELAY=$REPLICA_DELAY; START REPLICA"
fi

./bin/zdm plan --dsn "$PRIMARY" --schema lat --table t --alter "ADD COLUMN c INT NULL" --engine "$ENGINE" --out "$PLAN" >/dev/null || exit 1
start=$(date +%s)
if ./bin/zdm run --dsn "$PRIMARY" --replica-dsn "$REPLICA" --behind-proxy --execute "$PLAN" >"$DIR/run.log" 2>&1; then
	outcome=ok
else
	outcome="FAILED: $(tail -2 "$DIR/run.log" | tr '\n' ' ' | cut -c1-200)"
fi
echo "zdm run: $outcome in $(($(date +%s) - start))s; $(grep -c 'cut-over held' "$DIR/run.log") gate polls"

$M 2>/dev/null -e "UPDATE latlog.ctl SET phase='after'"
sleep 5
$M 2>/dev/null -e "UPDATE latlog.ctl SET stop=1"
wait "${pids[@]}" 2>/dev/null
pids=()

for ph in base run after; do
	$M 2>/dev/null <<EOF | awk -v ph=$ph '{printf "  %-5s n=%-8s p50=%7.1fms p99=%7.1fms max=%8.1fms  >100ms:%-5s >500ms:%s\n", ph, $1, $2/1000, $3/1000, $4/1000, $5, $6}'
SELECT COUNT(*),
  COALESCE(MAX(IF(rn = FLOOR(c * 0.50) + 1, dur_us, NULL)), 0),
  COALESCE(MAX(IF(rn = FLOOR(c * 0.99) + 1, dur_us, NULL)), 0),
  COALESCE(MAX(dur_us), 0), COALESCE(SUM(dur_us > 100000), 0), COALESCE(SUM(dur_us > 500000), 0)
FROM (SELECT dur_us, ROW_NUMBER() OVER (ORDER BY dur_us) rn, COUNT(*) OVER () c FROM latlog.l WHERE phase='$ph') q;
EOF
done
