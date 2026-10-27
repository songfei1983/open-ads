#!/usr/bin/env bash
# DMP deny-gate recovery drills (I4-05).
#
# Requires the local compose stack with clickhouse, valkey and dmp running:
#   docker compose -f deployments/docker-compose.yml up -d clickhouse valkey dmp
# The drills use two subjects seeded into a published snapshot:
#   REVOKED  subject revoked before the drills start
#   ALLOWED  subject that stays resolvable throughout
set -uo pipefail

COMPOSE=(docker compose -f deployments/docker-compose.yml)
DMP=http://127.0.0.1:8082
INTERNAL_TOKEN=dmp-internal-token-0123456789abcdef
TENANT=00000000-0000-0000-0000-000000000001
NAMESPACE=publisher-account-v1
PROVIDER=dmp-provider-01
REVOKED=$(printf '9%.0s' $(seq 1 64))
ALLOWED=$(printf '8%.0s' $(seq 1 64))
FAILURES=0

pass() { echo "  PASS: $1"; }
fail() { echo "  FAIL: $1"; FAILURES=$((FAILURES + 1)); }

ready_code() { curl -s -o /dev/null -w '%{http_code}' "$DMP/readyz"; }

resolve_status() {
  curl -s -o /tmp/drill_resolve.json -w '%{http_code}' -X POST "$DMP/internal/v1/audiences:resolve" \
    -H 'Content-Type: application/json' \
    -d "{\"namespace\":\"$NAMESPACE\",\"provider_id\":\"$PROVIDER\",\"subject_token\":\"$1\"}"
}

resolve_segments() {
  python3 - <<'PY'
import json
try:
    with open("/tmp/drill_resolve.json") as handle:
        print(len(json.load(handle).get("segments", [])))
except Exception:
    print("-")
PY
}

expect_resolve() { # description subject want_status want_segments
  local description=$1 subject=$2 want_status=$3 want_segments=$4 status segments
  status=$(resolve_status "$subject")
  segments=$(resolve_segments)
  if [ "$status" = "$want_status" ] && [ "$segments" = "$want_segments" ]; then
    pass "$description (status=$status segments=$segments)"
  else
    fail "$description: got status=$status segments=$segments, want $want_status/$want_segments"
  fi
}

wait_ready() { # expected_code timeout_seconds
  local expected=$1 timeout=${2:-30} elapsed=0 code
  while [ "$elapsed" -lt "$timeout" ]; do
    code=$(ready_code)
    [ "$code" = "$expected" ] && return 0
    sleep 1
    elapsed=$((elapsed + 1))
  done
  return 1
}

valkey() { "${COMPOSE[@]}" exec -T valkey valkey-cli --user dmp --pass dmp_valkey_password "$@" 2>/dev/null; }

echo "== Seeding a dedicated snapshot for the drills"
wait_ready 200 60 || fail "readyz is not 200"
checksum=$(python3 -c "import hashlib;rows=[('$REVOKED',1,'seg-tech-enthusiasts'),('$ALLOWED',1,'seg-tech-enthusiasts')];print(hashlib.sha256(''.join(sorted(f'{t}\t{v}\t{s}\n' for t,v,s in rows)).encode()).hexdigest())")
seed_status=$(curl -s -o /tmp/drill_seed.json -w '%{http_code}' -X POST "$DMP/internal/v1/snapshots" \
  -H "Authorization: Bearer $INTERNAL_TOKEN" -H 'Content-Type: application/json' \
  -d "{\"provider_id\":\"$PROVIDER\",\"namespace\":\"$NAMESPACE\",\"schema_version\":1,\"source_version\":\"drill-$$\",\"expected_rows\":2,\"expected_checksum\":\"$checksum\"}")
[ "$seed_status" = "201" ] || fail "drill snapshot registration: $seed_status $(cat /tmp/drill_seed.json)"
SEED=$(python3 -c 'import json;print(json.load(open("/tmp/drill_seed.json"))["snapshot_id"])')
curl -s -o /dev/null -X POST "$DMP/internal/v1/snapshots/$SEED/rows" -H "Authorization: Bearer $INTERNAL_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"rows\":[{\"subject_token\":\"$REVOKED\",\"subject_key_version\":1,\"segment_ids\":[\"seg-tech-enthusiasts\"]},{\"subject_token\":\"$ALLOWED\",\"subject_key_version\":1,\"segment_ids\":[\"seg-tech-enthusiasts\"]}]}"
curl -s -o /dev/null -X POST "$DMP/internal/v1/snapshots/$SEED/verify" -H "Authorization: Bearer $INTERNAL_TOKEN"
publish_status=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$DMP/internal/v1/snapshots/$SEED/publish" -H "Authorization: Bearer $INTERNAL_TOKEN")
[ "$publish_status" = "200" ] && pass "drill snapshot published" || fail "drill snapshot publish: $publish_status"
revoke_status=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$DMP/internal/v1/identity:revoke" \
  -H "Authorization: Bearer $INTERNAL_TOKEN" -H 'Content-Type: application/json' \
  -d "{\"namespace\":\"$NAMESPACE\",\"subject_tokens\":[{\"token\":\"$REVOKED\",\"key_version\":1}]}")
[ "$revoke_status" = "202" ] && pass "drill subject revoked" || fail "drill subject revoke: $revoke_status"
sleep 1

echo "== Drill 0: baseline"
expect_resolve "revoked subject resolves empty" "$REVOKED" 200 0
expect_resolve "allowed subject resolves one segment" "$ALLOWED" 200 1

echo "== Drill 1: Valkey epoch loss without restart"
valkey DEL "dmp:$TENANT:gate:epoch" >/dev/null
status=$(resolve_status "$REVOKED")
segments=$(resolve_segments)
if [ "$segments" = "0" ]; then
  pass "no audience released while the gate is rebuilding (status=$status segments=$segments)"
else
  fail "revoked subject received an audience during epoch loss (status=$status segments=$segments)"
fi
wait_ready 200 20 && pass "readiness recovered automatically (gate rebuilt from tombstones)" \
  || fail "readiness did not recover"
# The readiness loop ticks every 2s; give it one full pass to publish the epoch.
sleep 3
wait_ready 200 10 || fail "readiness did not stay at 200"
expect_resolve "revoked subject still blocked after rebuild" "$REVOKED" 200 0
expect_resolve "allowed subject still served after rebuild" "$ALLOWED" 200 1

echo "== Drill 2: ClickHouse outage keeps the deny gate"
"${COMPOSE[@]}" stop clickhouse >/dev/null
wait_ready 503 20 && pass "readiness fails while ClickHouse is down" || fail "readiness stayed $(ready_code) during the outage"
status=$(resolve_status "$ALLOWED")
if [ "$status" = "503" ]; then pass "resolve fails closed during ClickHouse outage"; else fail "resolve returned $status during the outage"; fi
[ "$(valkey EXISTS "dmp:$TENANT:gate:deny:$NAMESPACE:$REVOKED")" = "1" ] \
  && pass "deny marker survives the outage" || fail "deny marker disappeared"
"${COMPOSE[@]}" up -d --wait clickhouse >/dev/null
wait_ready 200 60 && pass "readiness returns after ClickHouse recovery" || fail "readiness after recovery: $(ready_code)"
expect_resolve "revoked subject still blocked after the outage" "$REVOKED" 200 0

echo "== Drill 3: node restart"
"${COMPOSE[@]}" restart dmp >/dev/null
wait_ready 200 90 && pass "restarted node becomes ready" || fail "restarted node readiness: $(ready_code)"
expect_resolve "revocation survives the restart" "$REVOKED" 200 0

echo "== Drill 4: in-flight revocation drains before completion"
SUBJECT=$(printf '7%.0s' $(seq 1 64))
now_ms=$(python3 -c 'import time;print(int(time.time()*1000))')
valkey ZADD "dmp:$TENANT:gate:inflight:$NAMESPACE:$SUBJECT" $((now_ms + 400)) drill-lease >/dev/null
revoke=$(curl -s -X POST "$DMP/internal/v1/identity:revoke" -H "Authorization: Bearer $INTERNAL_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"namespace\":\"$NAMESPACE\",\"subject_tokens\":[{\"token\":\"$SUBJECT\",\"key_version\":1}]}")
echo "  revoke: $revoke"
echo "$revoke" | grep -q '"drained_lookup_count":1' && pass "in-flight lease was observed" || fail "in-flight lease not reported"
expect_resolve "revoked subject is blocked immediately" "$SUBJECT" 200 0

echo
if [ "$FAILURES" -eq 0 ]; then echo "ALL DRILLS PASSED"; else echo "$FAILURES DRILL(S) FAILED"; fi
exit "$FAILURES"
