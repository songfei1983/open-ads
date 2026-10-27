#!/usr/bin/env bash
# DMP standalone integration: the full lifecycle against only clickhouse,
# valkey and dmp (no SSP/DSP/console involved).
#
#   docker compose -f deployments/docker-compose.yml up -d clickhouse valkey dmp
#   ./dmp/scripts/integration.sh
set -uo pipefail

DMP=http://127.0.0.1:8082
INTERNAL_TOKEN=dmp-internal-token-0123456789abcdef
ADMIN_TOKEN=dmp-admin-token-0123456789abcdef
NAMESPACE=publisher-account-v1
PROVIDER=dmp-provider-01
FAILURES=0

pass() { echo "  PASS: $1"; }
fail() { echo "  FAIL: $1"; FAILURES=$((FAILURES + 1)); }

internal() { curl -s -o /tmp/integration.json -w '%{http_code}' -X "$1" "$DMP/internal/v1$2" \
  -H "Authorization: Bearer $INTERNAL_TOKEN" -H 'Content-Type: application/json' ${3:+-d "$3"}; }

admin() { curl -s -o /tmp/integration.json -w '%{http_code}' -X "$1" "$DMP/admin/v1$2" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' ${3:+-d "$3"}; }

json() { python3 -c "import json;print(json.load(open('/tmp/integration.json'))$1)"; }
segments_count() { python3 -c 'import json;print(len(json.load(open("/tmp/integration.json")).get("segments") or []))'; }

# Fresh subjects per run so earlier revocations cannot interfere.
RUN_ID=$(python3 -c 'import os;print(os.urandom(16).hex())')
TOKEN_A=$(python3 -c "print(('$RUN_ID' + 'a' * 64)[:64])")
TOKEN_B=$(python3 -c "print(('$RUN_ID' + 'b' * 64)[:64])")
TOKEN_RAW="alice@example.com"

echo "== probes"
[ "$(curl -s -o /dev/null -w '%{http_code}' "$DMP/healthz")" = "200" ] && pass "healthz" || fail "healthz"
[ "$(curl -s -o /dev/null -w '%{http_code}' "$DMP/readyz")" = "200" ] && pass "readyz" || fail "readyz"

echo "== control plane refuses unauthenticated writes"
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$DMP/internal/v1/snapshots" -H 'Content-Type: application/json' -d '{}')
[ "$code" = "401" ] && pass "internal API requires the adapter token" || fail "internal API answered $code without a token"

echo "== import"
checksum=$(python3 - <<PY
import hashlib
rows = [("$TOKEN_A", 1, "seg-tech-enthusiasts"), ("$TOKEN_B", 1, "seg-sports-fans")]
print(hashlib.sha256("".join(sorted(f"{t}\t{v}\t{s}\n" for t, v, s in rows)).encode()).hexdigest())
PY
)
code=$(internal POST /snapshots "{\"provider_id\":\"$PROVIDER\",\"namespace\":\"$NAMESPACE\",\"schema_version\":1,\"source_version\":\"integration-$$\",\"expected_rows\":2,\"expected_checksum\":\"$checksum\"}")
[ "$code" = "201" ] && pass "snapshot registered" || fail "register snapshot: $code $(cat /tmp/integration.json)"
SNAPSHOT=$(json '["snapshot_id"]')

code=$(internal POST "/snapshots/$SNAPSHOT/rows" "{\"rows\":[{\"subject_token\":\"$TOKEN_RAW\",\"subject_key_version\":1,\"segment_ids\":[\"seg-tech-enthusiasts\"]}]}")
[ "$code" = "400" ] && pass "raw identity rejected" || fail "raw identity accepted: $code"

code=$(internal POST "/snapshots/$SNAPSHOT/rows" "{\"rows\":[{\"subject_token\":\"$TOKEN_A\",\"subject_key_version\":1,\"segment_ids\":[\"seg-tech-enthusiasts\"]},{\"subject_token\":\"$TOKEN_B\",\"subject_key_version\":1,\"segment_ids\":[\"seg-sports-fans\"]}]}")
[ "$code" = "200" ] && [ "$(json '["stored_rows"]')" = "2" ] && pass "membership stored" || fail "membership append: $code $(cat /tmp/integration.json)"

code=$(internal POST "/snapshots/$SNAPSHOT/verify")
[ "$code" = "200" ] && [ "$(json '["status"]')" = "verified" ] && pass "snapshot verified" || fail "verify: $code $(cat /tmp/integration.json)"

echo "== publication"
code=$(internal POST "/snapshots/$SNAPSHOT/publish")
[ "$code" = "200" ] && [ "$(json '["status"]')" = "published" ] && pass "snapshot published" || fail "publish: $code $(cat /tmp/integration.json)"

code=$(curl -s -o /tmp/integration.json -w '%{http_code}' -X POST "$DMP/internal/v1/audiences:resolve" \
  -H 'Content-Type: application/json' -d "{\"namespace\":\"$NAMESPACE\",\"provider_id\":\"$PROVIDER\",\"subject_token\":\"$TOKEN_A\"}")
[ "$code" = "200" ] && [ "$(segments_count)" = "1" ] && pass "audience resolved from the published snapshot" || fail "resolve: $code $(cat /tmp/integration.json)"
python3 - "$TOKEN_A" <<'PY' && pass "resolve response carries no token" || fail "resolve response echoed the token"
import json, sys
body = open("/tmp/integration.json").read()
sys.exit(1 if sys.argv[1] in body else 0)
PY

echo "== revocation"
code=$(internal POST /identity:revoke "{\"namespace\":\"$NAMESPACE\",\"subject_tokens\":[{\"token\":\"$TOKEN_A\",\"key_version\":1}]}")
[ "$code" = "202" ] && pass "revocation accepted" || fail "revoke: $code $(cat /tmp/integration.json)"
REVOCATION=$(json '["revocation_id"]')
status=$(json '["status"]')
[ "$status" = "completed" ] || [ "$status" = "pending" ] && pass "revocation status reported ($status)" || fail "unexpected revocation status: $status"

code=$(curl -s -o /tmp/integration.json -w '%{http_code}' -X POST "$DMP/internal/v1/audiences:resolve" \
  -H 'Content-Type: application/json' -d "{\"namespace\":\"$NAMESPACE\",\"provider_id\":\"$PROVIDER\",\"subject_token\":\"$TOKEN_A\"}")
[ "$code" = "200" ] && [ "$(segments_count)" = "0" ] && pass "revoked subject no longer resolves" || fail "revoked subject still resolved: $code $(cat /tmp/integration.json)"

code=$(internal GET "/identity-revocations/$REVOCATION")
[ "$code" = "200" ] && pass "revocation status queryable" || fail "status query: $code"

echo "== retention and physical deletion"
code=$(internal POST /retention:run)
[ "$code" = "200" ] && pass "retention sweep ran" || fail "retention sweep: $code $(cat /tmp/integration.json)"
code=$(internal GET "/identity-revocations/$REVOCATION")
[ "$(json '["physical_delete_status"]')" = "completed" ] && pass "physical deletion completed" || fail "physical deletion: $(cat /tmp/integration.json)"

code=$(internal POST "/snapshots/$SNAPSHOT/delete")
[ "$code" = "409" ] && pass "the serving snapshot is protected from deletion" || fail "serving snapshot deletion: $code $(cat /tmp/integration.json)"

echo
if [ "$FAILURES" -eq 0 ]; then echo "DMP STANDALONE INTEGRATION PASSED"; else echo "$FAILURES CHECK(S) FAILED"; fi
exit "$FAILURES"
