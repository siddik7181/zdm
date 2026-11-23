#!/usr/bin/env bash
# Runs docker/measure-pause.sh through the worst cases for both engines and prints one report.
# Needs the lab, ./bin/zdm and both engines on PATH. Takes about 30 minutes. ENGINES=pt-osc limits it to one engine.
set -uo pipefail
cd "$(dirname "$0")/.."

scenarios=(
	"normal|"
	"heavy writes|WRITERS=8 WRITE_SLEEP=0.005"
	"long transaction (60s)|HOLD_MODE=txn HOLD_SECONDS=60"
	"long SELECT (60s)|HOLD_MODE=select HOLD_SECONDS=60"
	"all of the above|WRITERS=8 WRITE_SLEEP=0.005 HOLD_MODE=txn HOLD_SECONDS=60"
)
# SOURCE_DELAY has 1 s granularity and gh-ost throttles at 2 s lag, so a delayed replica (REPLICA_DELAY=1 in
# measure-pause.sh) crawls for tens of minutes in this lab; it is not a default scenario.
for engine in ${ENGINES:-gh-ost pt-osc}; do
	for s in "${scenarios[@]}"; do
		echo "=== $engine: ${s%%|*}"
		# shellcheck disable=SC2086
		env ${s#*|} docker/measure-pause.sh "$engine" 2>&1
	done
done
