#!/usr/bin/env bash
# Loads ROWS narrow rows (default 100M) into big.t on the lab primary, 1M rows per transaction.
set -euo pipefail
ROWS=${ROWS:-100000000}
M="docker exec -i docker-primary-1 mysql -uroot -proot"
$M -e "DROP DATABASE IF EXISTS big; CREATE DATABASE big;
CREATE TABLE big.seed (id INT NOT NULL PRIMARY KEY);
CREATE TABLE big.t (id INT NOT NULL PRIMARY KEY, v INT NOT NULL DEFAULT 0);
SET SESSION cte_max_recursion_depth=2000000;
INSERT INTO big.seed WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i<999999) SELECT i FROM n" 2>&1 | grep -v Warn || true
for ((k = 0; k < ROWS / 1000000; k++)); do
  $M -e "INSERT INTO big.t (id, v) SELECT id + $((k * 1000000)), id % 100 FROM big.seed" 2>&1 | grep -v Warn || true
  ((k % 10 == 9)) && echo "loaded $(((k + 1) * 1000000)) rows"
done
$M -e "DROP TABLE big.seed" 2>&1 | grep -v Warn || true
