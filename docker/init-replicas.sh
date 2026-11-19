#!/usr/bin/env bash
# Wire replica1/replica2 to primary with GTID auto-positioning. Run after `make lab-up`.
set -euo pipefail
cd "$(dirname "$0")"
C="docker compose -f compose.yml"

# ping also succeeds on the entrypoint's temporary server; an authenticated query does not.
for r in replica1 replica2; do
  until $C exec -T "$r" mysql -uroot -proot -e 'SELECT 1' >/dev/null 2>&1; do sleep 2; done
done

for r in replica1 replica2; do
  delay=0; [ "$r" = replica2 ] && delay=5
  $C exec -T "$r" mysql -uroot -proot -e "
    STOP REPLICA;
    CHANGE REPLICATION SOURCE TO SOURCE_HOST='primary', SOURCE_USER='root',
      SOURCE_PASSWORD='root', SOURCE_AUTO_POSITION=1, GET_SOURCE_PUBLIC_KEY=1,
      SOURCE_DELAY=${delay};
    START REPLICA; SET PERSIST super_read_only=ON;" 2>&1 | grep -v 'Using a password' || true
done

sleep 3
for r in replica1 replica2; do
  echo "== $r"
  $C exec -T "$r" mysql -uroot -proot -E -e "SHOW REPLICA STATUS" 2>/dev/null |
    grep -E 'Replica_(IO|SQL)_Running:|Seconds_Behind_Source|Last_(IO|SQL)_Error:'
done
