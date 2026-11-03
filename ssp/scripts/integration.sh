#!/usr/bin/env bash
# SSP control-plane integration smoke. Requires a running SSP (with the
# management API configured) and MariaDB/Valkey. It exercises the publish
# pipeline, readiness gating, supply authentication and both ingress paths.
set -euo pipefail

BASE="${SSP_BASE_URL:-http://127.0.0.1:8080}"
ADMIN_TOKEN="${SSP_ADMIN_TOKEN:?SSP_ADMIN_TOKEN is required}"
SUPPLY_KEY="${SSP_SUPPLY_KEY:-supply-secret-value-0123456789}"
DSP_URL="${SSP_DSP_BID_URL:-http://127.0.0.1:8081/openrtb2/bid}"

auth=(-H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json")

json_field() {
  python3 -c 'import json,sys; print(json.load(sys.stdin).get(sys.argv[1],""))' "$1"
}

wait_for() {
  local url="$1" expected="$2" attempts="${3:-30}"
  for _ in $(seq 1 "$attempts"); do
    code="$(curl -s -o /dev/null -w '%{http_code}' "$url" || true)"
    if [ "$code" = "$expected" ]; then return 0; fi
    sleep 1
  done
  echo "timeout waiting for $url to return $expected" >&2
  return 1
}

create() {
  local resource="$1" body="$2"
  curl --fail --silent -X POST "$BASE/admin/v1/$resource" "${auth[@]}" -d "$body" | json_field id
}

publish() {
  local resource="$1" id="$2" version="${3:-1}"
  local code
  code="$(curl -s -o /tmp/ssp-publish.json -w '%{http_code}' -X POST \
    "$BASE/admin/v1/$resource/$id/publish" "${auth[@]}" -d "{\"version\":$version}")"
  if [ "$code" != "202" ]; then
    echo "publish $resource/$id failed: $code $(cat /tmp/ssp-publish.json)" >&2
    return 1
  fi
}

echo "==> waiting for SSP health"
wait_for "$BASE/healthz" 200

echo "==> management API authentication"
test "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/admin/v1/publishers")" = "401"
test "$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $ADMIN_TOKEN" "$BASE/admin/v1/publishers")" = "200"

echo "==> creating and publishing the sandbox supply chain"
publisher="$(create publishers '{"name":"CI Publisher"}')"
property="$(create properties "{\"publisher_id\":\"$publisher\",\"name\":\"CI Site\",\"type\":\"site\",\"external_id\":\"publisher.test\"}")"
policy="$(create auction-policies '{"name":"CI first price"}')"
placement="$(create placements "{\"property_id\":\"$property\",\"auction_policy_id\":\"$policy\",\"name\":\"CI Leaderboard\",\"media_type\":\"banner\",\"width\":728,\"height\":90,\"floor_cpm_micros\":1200000,\"currency\":\"USD\",\"privacy_policy\":\"consent_required\",\"allowed_origins\":[\"https://publisher.test\"]}")"
supply="$(create supply-endpoints "{\"property_id\":\"$property\",\"name\":\"CI Supply\",\"protocol_version\":\"openrtb2.6\",\"credential\":\"$SUPPLY_KEY\",\"allowed_origins\":[\"https://publisher.test\"],\"rate_limit_per_second\":100}")"
dsp="$(create dsp-endpoints "{\"name\":\"CI DSP\",\"bid_url\":\"$DSP_URL\",\"openrtb_profile\":\"openrtb2.6\",\"callback_hosts\":[\"127.0.0.1\"],\"priority\":10,\"timeout_ms\":50}")"

publish publishers "$publisher"
publish properties "$property"
publish auction-policies "$policy"
publish placements "$placement"
publish supply-endpoints "$supply"
publish dsp-endpoints "$dsp"

echo "==> waiting for the published manifest"
wait_for "$BASE/readyz" 200 30
ready="$(curl --fail --silent "$BASE/readyz")"
echo "$ready"
echo "$ready" | grep -q '"status":"ready"'

echo "==> supply ingress"
supply_body="{\"id\":\"ci-supply-1\",\"imp\":[{\"id\":\"1\",\"tagid\":\"$placement\",\"banner\":{\"w\":728,\"h\":90}}],\"site\":{\"domain\":\"publisher.test\",\"page\":\"https://publisher.test/demo\"},\"tmax\":120,\"cur\":[\"USD\"]}"
test "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/openrtb2/auction" -H 'Content-Type: application/json' -H "X-Supply-Key: wrong-credential-0123456789" -d "$supply_body")" = "401"
response="$(curl --fail --silent -X POST "$BASE/openrtb2/auction" -H 'Content-Type: application/json' -H "X-Supply-Key: $SUPPLY_KEY" -d "$supply_body")"
echo "$response"
echo "$response" | grep -q '"id":"ci-supply-1"'
echo "$response" | grep -q '"nbr":2'

echo "==> SDK ingress"
sdk_body="{\"placement_id\":\"$placement\",\"slot\":{\"id\":\"leaderboard\",\"width\":728,\"height\":90},\"page\":{\"origin\":\"https://publisher.test\",\"path\":\"/demo\"}}"
sdk="$(curl --fail --silent -X POST "$BASE/sdk/v1/ad-requests" -H 'Content-Type: application/json' -H 'Origin: https://publisher.test' -d "$sdk_body")"
echo "$sdk"
echo "$sdk" | grep -q '"status":"no_fill"'
test "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/sdk/v1/ad-requests" -H 'Content-Type: application/json' -H 'Origin: https://evil.test' -d "$sdk_body")" = "403"

echo "==> SSP integration smoke passed"
