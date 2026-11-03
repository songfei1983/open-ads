#!/usr/bin/env bash
# Filled-path integration smoke: publishes a DSP campaign and an SSP supply
# chain, then proves the request -> bid -> render -> impression -> click chain.
# It is the regression guard for every iteration that touches the ad path.
#
# Requires a running DSP (management API + config publisher + asset store) and a
# running SSP (management and internal APIs), plus MariaDB and Valkey.
set -euo pipefail

SSP_BASE="${SSP_BASE_URL:-http://127.0.0.1:8080}"
DSP_BASE="${DSP_BASE_URL:-http://127.0.0.1:8081}"
SSP_ADMIN_TOKEN="${SSP_ADMIN_TOKEN:?SSP_ADMIN_TOKEN is required}"
DSP_ADMIN_TOKEN="${DSP_ADMIN_TOKEN:?DSP_ADMIN_TOKEN is required}"
# A per-run supply credential keeps the manifest lookup bound to this run's
# endpoint even when the sandbox already holds earlier supply chains.
SUPPLY_KEY="${SSP_SUPPLY_KEY:-supply-secret-$(date +%s)-$$}"
DSP_BID_URL="${SSP_DSP_BID_URL:-$DSP_BASE/openrtb2/bid}"
CALLBACK_HOST="${SSP_DSP_CALLBACK_HOST:-127.0.0.1}"
BID_CPM_MICROS="${FILLED_PATH_BID_CPM_MICROS:-4200000}"
# Each run uses its own site domain so the script can be replayed against a
# long-lived sandbox without colliding with earlier resources.
SITE_DOMAIN="${FILLED_PATH_SITE_DOMAIN:-filled-$(date +%s)-$$.test}"
SITE_ORIGIN="https://$SITE_DOMAIN"

ssp_auth=(-H "Authorization: Bearer ${SSP_ADMIN_TOKEN}" -H "Content-Type: application/json")
dsp_auth=(-H "Authorization: Bearer ${DSP_ADMIN_TOKEN}" -H "Content-Type: application/json")
dsp_auth_form=(-H "Authorization: Bearer ${DSP_ADMIN_TOKEN}")
workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

json_field() {
  python3 -c 'import json,sys
value = json.load(sys.stdin)
if isinstance(value, dict):
    print(value.get(sys.argv[1], ""))
else:
    print("")' "$1"
}

wait_for() {
  local url="$1" expected="$2" attempts="${3:-40}"
  for _ in $(seq 1 "$attempts"); do
    if [ "$(curl -s -o /dev/null -w '%{http_code}' "$url" || true)" = "$expected" ]; then
      return 0
    fi
    sleep 1
  done
  echo "timeout waiting for $url to return $expected" >&2
  return 1
}

dsp_create() {
  local resource="$1" body="$2"
  curl --fail --silent -X POST "$DSP_BASE/admin/v1/$resource" "${dsp_auth[@]}" -d "$body" | json_field id
}

dsp_publish() {
  local resource="$1" id="$2" version="${3:-1}" code
  code="$(curl -s -o "$workdir/dsp-publish.json" -w '%{http_code}' -X POST \
    "$DSP_BASE/admin/v1/$resource/$id/publish" "${dsp_auth[@]}" -d "{\"version\":$version}")"
  if [ "$code" != "202" ]; then
    echo "DSP publish $resource/$id failed: $code $(cat "$workdir/dsp-publish.json")" >&2
    return 1
  fi
}

ssp_create() {
  local resource="$1" body="$2"
  curl --fail --silent -X POST "$SSP_BASE/admin/v1/$resource" "${ssp_auth[@]}" -d "$body" | json_field id
}

ssp_publish() {
  local resource="$1" id="$2" version="${3:-1}" code
  code="$(curl -s -o "$workdir/ssp-publish.json" -w '%{http_code}' -X POST \
    "$SSP_BASE/admin/v1/$resource/$id/publish" "${ssp_auth[@]}" -d "{\"version\":$version}")"
  if [ "$code" != "202" ]; then
    echo "SSP publish $resource/$id failed: $code $(cat "$workdir/ssp-publish.json")" >&2
    return 1
  fi
}

echo "==> waiting for DSP and SSP health"
wait_for "$DSP_BASE/healthz" 200
wait_for "$SSP_BASE/healthz" 200

echo "==> publishing the DSP campaign"
starts_at="$(python3 -c 'import datetime; print((datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(hours=1)).strftime("%Y-%m-%dT%H:%M:%SZ"))')"
advertiser="$(dsp_create advertisers '{"status":"active","default_currency":"USD"}')"
campaign="$(dsp_create campaigns "{\"advertiser_id\":\"$advertiser\",\"status\":\"active\",\"starts_at\":\"$starts_at\"}")"
dsp_publish campaigns "$campaign"

# A 1x1 PNG is enough to exercise content addressing, MIME validation and the
# server-side hash recomputation on render.
python3 - "$workdir/tiny.png" <<'PY'
import base64, sys
data = base64.b64decode(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
)
open(sys.argv[1], "wb").write(data)
PY
asset_sha="$(shasum -a 256 "$workdir/tiny.png" | awk '{print $1}')"
asset="$(curl --fail --silent -X POST "$DSP_BASE/admin/v1/creative-assets" "${dsp_auth_form[@]}" \
  -F "file=@$workdir/tiny.png;type=image/png" -F "expected_sha256=$asset_sha")"
asset_ref="$(echo "$asset" | json_field asset_ref)"
if [ -z "$asset_ref" ]; then
  echo "asset upload did not return an asset_ref: $asset" >&2
  exit 1
fi
creative="$(dsp_create creatives "{\"status\":\"active\",\"asset_ref\":\"$asset_ref\",\"landing_url\":\"https://example.com/landing\"}")"
dsp_publish creatives "$creative"
line_item="$(dsp_create line-items "{\"campaign_id\":\"$campaign\",\"status\":\"active\",\"bid_cpm_micros\":$BID_CPM_MICROS,\"currency\":\"USD\",\"daily_budget_micros\":100000000,\"lifetime_budget_micros\":1000000000,\"targeting\":{\"site_domains\":[\"$SITE_DOMAIN\"],\"banner_sizes\":[{\"w\":728,\"h\":90}]},\"creative_ids\":[\"$creative\"]}")"
dsp_publish line-items "$line_item"

echo "==> publishing the SSP supply chain"
publisher="$(ssp_create publishers '{"name":"Filled Path Publisher"}')"
property="$(ssp_create properties "{\"publisher_id\":\"$publisher\",\"name\":\"Filled Path Site\",\"type\":\"site\",\"external_id\":\"$SITE_DOMAIN\"}")"
policy="$(ssp_create auction-policies '{"name":"Filled Path first price"}')"
placement="$(ssp_create placements "{\"property_id\":\"$property\",\"auction_policy_id\":\"$policy\",\"name\":\"Filled Path Leaderboard\",\"media_type\":\"banner\",\"width\":728,\"height\":90,\"floor_cpm_micros\":1200000,\"currency\":\"USD\",\"privacy_policy\":\"consent_required\",\"allowed_origins\":[\"$SITE_ORIGIN\"]}")"
supply="$(ssp_create supply-endpoints "{\"property_id\":\"$property\",\"name\":\"Filled Path Supply\",\"protocol_version\":\"openrtb2.6\",\"credential\":\"$SUPPLY_KEY\",\"allowed_origins\":[\"$SITE_ORIGIN\"],\"rate_limit_per_second\":100}")"
dsp_endpoint="$(ssp_create dsp-endpoints "{\"name\":\"Filled Path DSP\",\"bid_url\":\"$DSP_BID_URL\",\"openrtb_profile\":\"openrtb2.6\",\"callback_hosts\":[\"$CALLBACK_HOST\"],\"priority\":10,\"timeout_ms\":50}")"

ssp_publish publishers "$publisher"
ssp_publish properties "$property"
ssp_publish auction-policies "$policy"
ssp_publish placements "$placement"
ssp_publish supply-endpoints "$supply"
# The endpoint must be published once before its creative registry can be
# synced; the sync itself republishes the endpoint with the whitelist.
ssp_publish dsp-endpoints "$dsp_endpoint"

echo "==> syncing the DSP creative registry into SSP"
sync="$(curl --fail --silent -X POST "$DSP_BASE/admin/v1/ssp/creative-registry/sync" \
  "${dsp_auth[@]}" -d "{\"dsp_endpoint_id\":\"$dsp_endpoint\"}")"
echo "$sync"
echo "$sync" | grep -qE '"accepted_count":[1-9]'

echo "==> waiting for the filled manifest"
wait_for "$SSP_BASE/readyz" 200 60

echo "==> SDK ingress returns a filled ad"
sdk_body="{\"placement_id\":\"$placement\",\"slot\":{\"id\":\"leaderboard\",\"width\":728,\"height\":90},\"page\":{\"origin\":\"$SITE_ORIGIN\",\"path\":\"/demo\"}}"
# Readiness flips on the first manifest load, so poll until this run's
# placement is served and the DSP has installed its budget lease.
for _ in $(seq 1 30); do
  sdk_response="$(curl -s -w '\n%{http_code}' -X POST "$SSP_BASE/sdk/v1/ad-requests" -H 'Content-Type: application/json' -H "Origin: $SITE_ORIGIN" -d "$sdk_body")"
  sdk_code="${sdk_response##*$'\n'}"
  sdk="${sdk_response%$'\n'*}"
  if echo "$sdk" | grep -q '"status":"filled"'; then
    break
  fi
  sleep 1
done
echo "$sdk"
test "$sdk_code" = "200"
echo "$sdk" | grep -q '"status":"filled"'
render_url="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["ad"]["render_url"])' <<<"$sdk")"
click_url="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["ad"]["click_url"])' <<<"$sdk")"
event_token="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["ad"]["event_token"])' <<<"$sdk")"

echo "==> render shell and asset bytes"
render_html="$(curl --fail --silent "$render_url")"
echo "$render_html" | grep -q '/render/v1/assets/'
asset_path="$(echo "$render_html" | python3 -c 'import re,sys; match=re.search(r"/render/v1/assets/([A-Za-z0-9_-]+)", sys.stdin.read()); print(match.group(1) if match else "")')"
test -n "$asset_path"
asset_code="$(curl -s -o "$workdir/asset.png" -w '%{http_code}' "$SSP_BASE/render/v1/assets/$asset_path")"
test "$asset_code" = "200"
test "$(shasum -a 256 "$workdir/asset.png" | awk '{print $1}')" = "$asset_sha"

echo "==> impression is accepted once and deduplicated afterwards"
event_id="$(python3 -c 'import hashlib; print(hashlib.sha256(b"filled-path").hexdigest()[:32])')"
impression="{\"event_token\":\"$event_token\",\"event_id\":\"$event_id\",\"occurred_at\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"}"
first="$(curl --fail --silent -X POST "$SSP_BASE/sdk/v1/impressions" -H 'Content-Type: application/json' -d "$impression")"
echo "$first"
echo "$first" | grep -q '"status":"accepted"'
second="$(curl --fail --silent -X POST "$SSP_BASE/sdk/v1/impressions" -H 'Content-Type: application/json' -d "$impression")"
echo "$second"
echo "$second" | grep -q '"status":"duplicate"'

echo "==> publisher payable is booked exactly once"
auction_id="$(echo "$sdk" | python3 -c 'import json,sys; print(json.load(sys.stdin)["diagnostics"]["auction_id"])')"
expected_amount="$((BID_CPM_MICROS / 1000))"
curl --fail --silent -H "Authorization: Bearer ${SSP_ADMIN_TOKEN}" \
  "$SSP_BASE/admin/v1/publisher-payable?limit=50" | python3 -c '
import json, sys
auction, expected = sys.argv[1], int(sys.argv[2])
rows = json.load(sys.stdin)
matches = [row for row in rows if row["auction_id"] == auction and row["entry_type"] == "billable"]
if len(matches) != 1:
    raise SystemExit(f"expected one billable payable entry for {auction}, got {len(matches)}")
if matches[0]["amount_micros"] != expected:
    raise SystemExit("payable amount %d != %d" % (matches[0]["amount_micros"], expected))
print("payable", matches[0]["entry_id"], matches[0]["amount_micros"])
' "$auction_id" "$expected_amount"

echo "==> billable outbox delivers the impression to the DSP"
outbox_status=""
for _ in $(seq 1 30); do
  outbox_status="$(curl --fail --silent -H "Authorization: Bearer ${SSP_ADMIN_TOKEN}" \
    "$SSP_BASE/admin/v1/billable-outbox?limit=50" | python3 -c '
import json, sys
auction = sys.argv[1]
rows = [row for row in json.load(sys.stdin) if row["auction_id"] == auction]
print(rows[0]["status"] if rows else "missing")
' "$auction_id")"
  case "$outbox_status" in
    delivered | quarantined | rejected) break ;;
  esac
  sleep 1
done
echo "outbox $outbox_status"
test "$outbox_status" = "delivered"

echo "==> DSP recorded the advertiser spend"
curl --fail --silent -H "Authorization: Bearer ${DSP_ADMIN_TOKEN}" \
  "$DSP_BASE/admin/v1/spend-ledger?limit=50" | python3 -c '
import json, sys
line_item, expected = sys.argv[1], int(sys.argv[2])
rows = [row for row in json.load(sys.stdin)
        if row["line_item_id"] == line_item and row["entry_type"] == "billable"]
if len(rows) != 1:
    raise SystemExit(f"expected one billable spend entry for {line_item}, got {len(rows)}")
if rows[0]["amount_micros"] != expected:
    raise SystemExit("spend amount %d != %d" % (rows[0]["amount_micros"], expected))
print("spend", rows[0]["entry_id"], rows[0]["amount_micros"])
' "$line_item" "$expected_amount"

echo "==> click redirects to the allowlisted landing page"
location="$(curl -s -o /dev/null -w '%{redirect_url}' "$click_url")"
test "$location" = "https://example.com/landing"

echo "==> supply ingress returns a seat bid"
supply_body="{\"id\":\"filled-path-supply-1\",\"imp\":[{\"id\":\"1\",\"tagid\":\"$placement\",\"banner\":{\"w\":728,\"h\":90}}],\"site\":{\"domain\":\"$SITE_DOMAIN\",\"page\":\"$SITE_ORIGIN/demo\"},\"tmax\":120,\"cur\":[\"USD\"]}"
supply_response="$(curl -s -w '\n%{http_code}' -X POST "$SSP_BASE/openrtb2/auction" -H 'Content-Type: application/json' -H "X-Supply-Key: $SUPPLY_KEY" -d "$supply_body")"
supply_code="${supply_response##*$'\n'}"
response="${supply_response%$'\n'*}"
echo "$response"
echo "$response" | grep -q '"seatbid"'

echo "==> Supply reports the impression through the shared ledger"
supply_token="$(echo "$response" | python3 -c '
import json, sys
print(json.load(sys.stdin)["seatbid"][0]["bid"][0]["ext"]["openads"]["event_token"])
')"
supply_impression="{\"event_token\":\"$supply_token\"}"
first_supply="$(curl --fail --silent -X POST "$SSP_BASE/internal/v1/supply/impressions" \
  -H 'Content-Type: application/json' -H "X-Supply-Key: $SUPPLY_KEY" -d "$supply_impression")"
echo "$first_supply"
echo "$first_supply" | grep -q '"status":"accepted"'
second_supply="$(curl --fail --silent -X POST "$SSP_BASE/internal/v1/supply/impressions" \
  -H 'Content-Type: application/json' -H "X-Supply-Key: $SUPPLY_KEY" -d "$supply_impression")"
echo "$second_supply"
echo "$second_supply" | grep -q '"status":"duplicate"'
test "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$SSP_BASE/internal/v1/supply/impressions" \
  -H 'Content-Type: application/json' -H 'X-Supply-Key: wrong-credential' -d "$supply_impression")" = "401"

echo "==> filled-path integration smoke passed"
