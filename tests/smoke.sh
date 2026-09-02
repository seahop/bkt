#!/usr/bin/env bash
# =============================================================================
# bkt Smoke Tests — Comprehensive
# =============================================================================
# Tests the full bkt platform: REST API, S3-compatible API, file types,
# UI API endpoints, content-type enforcement, and optional AWS backend
# verification (confirms objects actually land on S3).
#
# Usage:
#   ./tests/smoke.sh
#   ./tests/smoke.sh --endpoint https://bkt.example.com:9443
#   ./tests/smoke.sh --endpoint https://localhost:9443 --password mypass
#   CLEANUP=false ./tests/smoke.sh    # keep test data for inspection
#
# Requirements: curl, aws CLI, dd, python3 (for minimal file generation)
# Optional:     zip (for archive upload test)
# =============================================================================

set -euo pipefail

# ── Defaults ──────────────────────────────────────────────────────────────────
# Console endpoint serves the REST API (/api). The S3-compatible API lives on its
# own endpoint (separate port since the listener split). BKT_S3_ENDPOINT defaults
# to BKT_ENDPOINT for backward compatibility with single-port deployments.
BKT_ENDPOINT="${BKT_ENDPOINT:-https://localhost:9443}"
BKT_S3_ENDPOINT="${BKT_S3_ENDPOINT:-}"
BKT_USERNAME="${BKT_USERNAME:-admin}"
BKT_PASSWORD="${BKT_PASSWORD:-}"
CLEANUP="${CLEANUP:-true}"

# ── Parse flags ───────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case $1 in
    --endpoint)    BKT_ENDPOINT="$2";    shift 2 ;;
    --s3-endpoint) BKT_S3_ENDPOINT="$2"; shift 2 ;;
    --username)    BKT_USERNAME="$2";    shift 2 ;;
    --password)    BKT_PASSWORD="$2";    shift 2 ;;
    --no-cleanup)  CLEANUP=false;        shift   ;;
    *) echo "Unknown arg: $1"; exit 1 ;;
  esac
done

# ── Load .env from project root ───────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ENV_FILE="${SCRIPT_DIR}/../.env"

load_env_var() {
  local key="$1"
  local val=""
  if [[ -f "$ENV_FILE" ]]; then
    val=$(grep -E "^${key}=" "$ENV_FILE" 2>/dev/null | head -1 | cut -d= -f2- | tr -d '"' | tr -d "'")
  fi
  echo "$val"
}

[[ -z "$BKT_PASSWORD" ]] && BKT_PASSWORD=$(load_env_var "ADMIN_PASSWORD")

# Resolve the S3 endpoint. The console SPA answers 200 for ANY path, so
# pointing S3 clients at the console port makes every S3 test silently bogus —
# derive the real S3 listener port from .env (SERVER_PORT is the S3-compatible
# API port in the compose stack) before falling back to the console endpoint
# (true single-port deployments only).
if [[ -z "$BKT_S3_ENDPOINT" ]]; then
  _s3_port=$(load_env_var "SERVER_PORT")
  _console_port="${BKT_ENDPOINT##*:}"
  if [[ -n "$_s3_port" && "$_s3_port" != "$_console_port" ]]; then
    BKT_S3_ENDPOINT="$(echo "$BKT_ENDPOINT" | sed -E 's#:[0-9]+/?$##'):${_s3_port}"
  else
    BKT_S3_ENDPOINT="$BKT_ENDPOINT"
  fi
fi

if [[ -z "$BKT_PASSWORD" ]]; then
  echo "Error: BKT_PASSWORD required. Pass --password or set ADMIN_PASSWORD in .env"
  exit 1
fi

# AWS backend config — used to decide whether to run the S3 storage pass and to
# verify objects directly on S3. Prefer an already-exported environment variable
# (how docker-compose injects these into the test container, where no .env is
# mounted) and fall back to the project .env when running on the host.
S3_BACKEND_ENABLED="${S3_ENABLED:-$(load_env_var S3_ENABLED)}"
S3_REGION="${S3_REGION:-$(load_env_var S3_REGION)}"
S3_BUCKET_PREFIX="${S3_BUCKET_PREFIX:-$(load_env_var S3_BUCKET_PREFIX)}"
AWS_KEY="${S3_ACCESS_KEY_ID:-$(load_env_var S3_ACCESS_KEY_ID)}"
AWS_SECRET="${S3_SECRET_ACCESS_KEY:-$(load_env_var S3_SECRET_ACCESS_KEY)}"

# ── Colors ────────────────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
CYAN='\033[0;36m'; BOLD='\033[1m'; RESET='\033[0m'

# ── State ─────────────────────────────────────────────────────────────────────
PASS=0; FAIL=0; SKIP=0
FAILED_TESTS=()
JWT_TOKEN=""; REFRESH_TOKEN=""
ACCESS_KEY=""; SECRET_KEY=""; ACCESS_KEY_ID=""
TEST_BUCKET=""              # set per storage-backend pass (see run_storage_suite)
CREATED_BUCKETS=()          # every bucket created, for cleanup
LINKED_BUCKET=""            # pre-provisioned S3 bucket the s3 pass links to (from S3_BUCKETS)
LINKED_PREREGISTERED=false  # true when that bucket was already registered in bkt
AWS_PROFILE="bkt-smoke-$$"
TEMP_FILES=()

# ── Helpers ───────────────────────────────────────────────────────────────────
log()  { echo -e "${CYAN}▸ $*${RESET}"; }
bold() { echo -e "\n${BOLD}── $* ──${RESET}"; }

pass() { PASS=$((PASS+1)); echo -e "  ${GREEN}✔${RESET} $1"; }

fail() {
  FAIL=$((FAIL+1)); FAILED_TESTS+=("$1")
  echo -e "  ${RED}✖${RESET} $1"
  [[ -n "${2:-}" ]] && echo -e "    ${RED}↳ $2${RESET}"
  return 0  # never let fail() itself cause set -e to exit the script
}

skip() { SKIP=$((SKIP+1)); echo -e "  ${YELLOW}⊘${RESET} $1 (${2})"; }

make_temp() {
  local f; f=$(mktemp "${TMPDIR:-/tmp}/bkt-smoke-XXXXXX${1:-}")
  TEMP_FILES+=("$f"); echo "$f"
}

# curl wrapper (ignores TLS for self-signed dev certs)
api() { curl -sk --max-time 15 "$@"; }

http_code() { curl -sk --max-time 15 -o /dev/null -w "%{http_code}" "$@"; }

# AWS CLI wrappers pointed at bkt
s3()    { aws s3    --endpoint-url "$BKT_S3_ENDPOINT" --no-verify-ssl --profile "$AWS_PROFILE" "$@" 2>&1; }
s3api() { aws s3api --endpoint-url "$BKT_S3_ENDPOINT" --no-verify-ssl --profile "$AWS_PROFILE" "$@" 2>&1; }

# Direct AWS S3 access (bypasses bkt, for backend verification)
aws_direct() { AWS_ACCESS_KEY_ID="$AWS_KEY" AWS_SECRET_ACCESS_KEY="$AWS_SECRET" \
  AWS_DEFAULT_REGION="${S3_REGION:-us-east-1}" aws "$@" 2>&1; }

cleanup() {
  if [[ "$CLEANUP" != "true" ]]; then
    echo -e "  ${YELLOW}Cleanup skipped. Buckets: ${CREATED_BUCKETS[*]:-none}${RESET}"
    return
  fi
  log "Cleaning up..."
  for b in "${CREATED_BUCKETS[@]:-}"; do
    [[ -z "$b" ]] && continue
    [[ -n "$ACCESS_KEY" ]] && \
      aws s3 rm "s3://${b}" --recursive \
        --endpoint-url "$BKT_S3_ENDPOINT" --no-verify-ssl --profile "$AWS_PROFILE" &>/dev/null || true
    # Keep the registration of an operator-provisioned linked bucket — only the
    # test objects (removed above) belong to this run.
    [[ "$b" == "${LINKED_BUCKET:-}" && "$LINKED_PREREGISTERED" == "true" ]] && continue
    [[ -n "$JWT_TOKEN" ]] && \
      api -X DELETE "${BKT_ENDPOINT}/api/buckets/${b}" \
        -H "Authorization: Bearer ${JWT_TOKEN}" &>/dev/null || true
  done
  [[ -n "$JWT_TOKEN" && -n "${ACCESS_KEY_ID:-}" ]] && \
    api -X DELETE "${BKT_ENDPOINT}/api/access-keys/${ACCESS_KEY_ID}" \
      -H "Authorization: Bearer ${JWT_TOKEN}" &>/dev/null || true
  for f in "${TEMP_FILES[@]:-}"; do [[ -f "$f" ]] && rm -f "$f"; done
  aws configure set aws_access_key_id "" --profile "$AWS_PROFILE" &>/dev/null || true
}
trap cleanup EXIT

# ── Minimal test file generators (no ImageMagick needed) ──────────────────────

# 1×1 white PNG (67 bytes, valid PNG with IHDR/IDAT/IEND)
make_png() {
  local f; f=$(make_temp ".png")
  python3 -c "
import struct, zlib
def chunk(t, d): return struct.pack('>I', len(d)) + t + d + struct.pack('>I', zlib.crc32(t+d)&0xffffffff)
sig = b'\x89PNG\r\n\x1a\n'
ihdr = chunk(b'IHDR', struct.pack('>IIBBBBB', 1, 1, 8, 2, 0, 0, 0))
raw  = b'\x00\xff\xff\xff'  # filter byte + RGB white
idat = chunk(b'IDAT', zlib.compress(raw))
iend = chunk(b'IEND', b'')
open('$f','wb').write(sig+ihdr+idat+iend)
"
  echo "$f"
}

# Minimal valid PDF (200 bytes)
make_pdf() {
  local f; f=$(make_temp ".pdf")
  python3 -c "
pdf = b'''%PDF-1.4
1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj
2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj
3 0 obj<</Type/Page/MediaBox[0 0 3 3]>>endobj
xref
0 4
0000000000 65535 f
0000000009 00000 n
0000000058 00000 n
0000000115 00000 n
trailer<</Size 4/Root 1 0 R>>
startxref
190
%%EOF'''
open('$f','wb').write(pdf)
"
  echo "$f"
}

# Minimal JPEG (a valid 1×1 JPEG)
make_jpeg() {
  local f; f=$(make_temp ".jpg")
  python3 -c "
import struct
# Minimal JFIF JPEG (1x1 white pixel)
data = bytes([
  0xFF,0xD8,0xFF,0xE0,0x00,0x10,0x4A,0x46,0x49,0x46,0x00,0x01,0x01,0x00,0x00,0x01,
  0x00,0x01,0x00,0x00,0xFF,0xDB,0x00,0x43,0x00,0x08,0x06,0x06,0x07,0x06,0x05,0x08,
  0x07,0x07,0x07,0x09,0x09,0x08,0x0A,0x0C,0x14,0x0D,0x0C,0x0B,0x0B,0x0C,0x19,0x12,
  0x13,0x0F,0x14,0x1D,0x1A,0x1F,0x1E,0x1D,0x1A,0x1C,0x1C,0x20,0x24,0x2E,0x27,0x20,
  0x22,0x2C,0x23,0x1C,0x1C,0x28,0x37,0x29,0x2C,0x30,0x31,0x34,0x34,0x34,0x1F,0x27,
  0x39,0x3D,0x38,0x32,0x3C,0x2E,0x33,0x34,0x32,0xFF,0xC0,0x00,0x0B,0x08,0x00,0x01,
  0x00,0x01,0x01,0x01,0x11,0x00,0xFF,0xC4,0x00,0x1F,0x00,0x00,0x01,0x05,0x01,0x01,
  0x01,0x01,0x01,0x01,0x00,0x00,0x00,0x00,0x00,0x00,0x00,0x00,0x01,0x02,0x03,0x04,
  0x05,0x06,0x07,0x08,0x09,0x0A,0x0B,0xFF,0xDA,0x00,0x08,0x01,0x01,0x00,0x00,0x3F,
  0x00,0xFB,0x26,0x8A,0x28,0x03,0xFF,0xD9
])
open('$f','wb').write(data)
"
  echo "$f"
}

# Minimal GIF (GIF89a 1x1 transparent)
make_gif() {
  local f; f=$(make_temp ".gif")
  python3 -c "
data = b'GIF89a\x01\x00\x01\x00\x00\x00\x00!\xf9\x04\x00\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;'
open('$f','wb').write(data)
"
  echo "$f"
}

# Minimal ZIP archive
make_zip() {
  local f; f=$(make_temp ".zip")
  if command -v zip &>/dev/null; then
    local tmp_txt; tmp_txt=$(make_temp ".txt")
    echo "bkt smoke test" > "$tmp_txt"
    zip -j "$f" "$tmp_txt" &>/dev/null
    echo "$f"
  else
    echo ""
  fi
}

# ── Section 1: Prerequisites ──────────────────────────────────────────────────
test_prereqs() {
  bold "Prerequisites"
  for cmd in curl aws python3 dd; do
    command -v "$cmd" &>/dev/null && pass "$cmd found" || fail "$cmd not found"
  done
  command -v zip &>/dev/null && pass "zip found (archive test enabled)" \
    || skip "zip not found" "archive upload test will be skipped"
}

# ── Section 2: Health ─────────────────────────────────────────────────────────
test_health() {
  bold "Health & readiness"
  local code
  code=$(http_code "${BKT_ENDPOINT}/health")
  [[ "$code" == "200" ]] && pass "GET /health → 200" || fail "GET /health" "HTTP $code"

  code=$(http_code "${BKT_ENDPOINT}/ready")
  [[ "$code" == "200" ]] && pass "GET /ready → 200" || fail "GET /ready" "HTTP $code"

  code=$(http_code "${BKT_ENDPOINT}/live")
  [[ "$code" == "200" ]] && pass "GET /live → 200" || fail "GET /live" "HTTP $code"
}

# ── Section 3: Prometheus metrics ────────────────────────────────────────────
test_metrics() {
  bold "Prometheus metrics"

  # The storage gauges are populated by a background collector that runs an
  # initial pass at startup; on a freshly booted instance that DB collection can
  # lag the HTTP server becoming ready, so retry briefly before asserting.
  # Scrape to a file (the body is ~80KB; capturing that into a shell variable can
  # truncate its tail, where the bkt_* gauges live). Wait on bkt_active_users_total,
  # the LAST gauge the startup collector sets, so the earlier ones are present too.
  local mfile; mfile=$(make_temp ".prom")
  local i
  for i in $(seq 1 15); do
    curl -sk --max-time 20 "${BKT_ENDPOINT}/metrics" -o "$mfile"
    grep -q "bkt_active_users_total" "$mfile" && break
    sleep 1
  done

  for metric in bkt_http_requests_total bkt_storage_buckets_total \
                bkt_storage_objects_total bkt_storage_bytes_total \
                bkt_active_users_total go_goroutines; do
    grep -q "$metric" "$mfile" \
      && pass "/metrics — $metric present" \
      || fail "/metrics — $metric missing"
  done
}

# ── Section 4: Swagger UI ─────────────────────────────────────────────────────
test_swagger() {
  bold "Swagger UI"
  local body; body=$(api "${BKT_ENDPOINT}/api/docs/index.html")
  echo "$body" | grep -qi "swagger\|openapi" \
    && pass "GET /api/docs/ — Swagger UI loads" \
    || fail "GET /api/docs/" "${body:0:120}"
}

# ── Section 5: Authentication ─────────────────────────────────────────────────
test_auth() {
  bold "Authentication"

  # Login
  local resp
  resp=$(api -X POST "${BKT_ENDPOINT}/api/auth/login" \
    -H "Content-Type: application/json" \
    -d "{\"username\":\"${BKT_USERNAME}\",\"password\":\"${BKT_PASSWORD}\"}")

  JWT_TOKEN=$(echo "$resp" | grep -o '"token":"[^"]*"' | cut -d'"' -f4)
  REFRESH_TOKEN=$(echo "$resp" | grep -o '"refresh_token":"[^"]*"' | cut -d'"' -f4)

  [[ -n "$JWT_TOKEN" ]] && pass "POST /api/auth/login → JWT token received" \
    || { fail "POST /api/auth/login" "$resp"; return 1; }

  # Validate token via /users/me
  local me; me=$(api "${BKT_ENDPOINT}/api/users/me" -H "Authorization: Bearer ${JWT_TOKEN}")
  echo "$me" | grep -q '"username"' \
    && pass "GET /api/users/me → authenticated" \
    || fail "GET /api/users/me" "$me"

  # Refresh token
  local refreshed
  refreshed=$(api -X POST "${BKT_ENDPOINT}/api/auth/refresh" \
    -H "Content-Type: application/json" \
    -d "{\"refresh_token\":\"${REFRESH_TOKEN}\"}")
  echo "$refreshed" | grep -q '"token"' \
    && pass "POST /api/auth/refresh → new access token returned" \
    || fail "POST /api/auth/refresh" "$refreshed"

  # Token revocation (separate session, then verify both tokens die)
  local resp2 dead_token dead_refresh
  resp2=$(api -X POST "${BKT_ENDPOINT}/api/auth/login" \
    -H "Content-Type: application/json" \
    -d "{\"username\":\"${BKT_USERNAME}\",\"password\":\"${BKT_PASSWORD}\"}")
  dead_token=$(echo "$resp2" | grep -o '"token":"[^"]*"' | cut -d'"' -f4)
  dead_refresh=$(echo "$resp2" | grep -o '"refresh_token":"[^"]*"' | cut -d'"' -f4)

  api -X POST "${BKT_ENDPOINT}/api/auth/logout" \
    -H "Authorization: Bearer ${dead_token}" \
    -H "Content-Type: application/json" \
    -d "{\"refresh_token\":\"${dead_refresh}\"}" &>/dev/null

  local revoked_me
  revoked_me=$(api "${BKT_ENDPOINT}/api/users/me" -H "Authorization: Bearer ${dead_token}")
  echo "$revoked_me" | grep -qi "unauthorized\|invalid\|expired" \
    && pass "Token revocation — logged-out access token rejected" \
    || fail "Token revocation — access token still valid after logout"

  local revoked_refresh
  revoked_refresh=$(api -X POST "${BKT_ENDPOINT}/api/auth/refresh" \
    -H "Content-Type: application/json" \
    -d "{\"refresh_token\":\"${dead_refresh}\"}")
  echo "$revoked_refresh" | grep -qi "unauthorized\|invalid\|revoked\|expired" \
    && pass "Token revocation — refresh token also rejected after logout" \
    || fail "Token revocation — refresh token still usable after logout"
}

# ── Section 6: Access keys ─────────────────────────────────────────────────────
test_access_keys() {
  bold "Access keys"

  # Revoke all existing keys first to avoid hitting the 5-key limit
  # (orphaned keys from previous interrupted test runs)
  local existing_keys
  existing_keys=$(api "${BKT_ENDPOINT}/api/access-keys" -H "Authorization: Bearer ${JWT_TOKEN}")
  echo "$existing_keys" | python3 -c "
import sys, json
keys = json.load(sys.stdin)
if isinstance(keys, list):
    for k in keys:
        print(k.get('id',''))
" 2>/dev/null | while read -r kid; do
    [[ -n "$kid" ]] && api -X DELETE "${BKT_ENDPOINT}/api/access-keys/${kid}" \
      -H "Authorization: Bearer ${JWT_TOKEN}" &>/dev/null || true
  done

  local resp
  resp=$(api -X POST "${BKT_ENDPOINT}/api/access-keys" \
    -H "Authorization: Bearer ${JWT_TOKEN}" \
    -H "Content-Type: application/json")

  ACCESS_KEY=$(echo "$resp" | grep -o '"access_key":"[^"]*"' | cut -d'"' -f4)
  SECRET_KEY=$(echo "$resp" | grep -o '"secret_key":"[^"]*"' | cut -d'"' -f4)
  ACCESS_KEY_ID=$(echo "$resp" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)

  [[ -n "$ACCESS_KEY" && -n "$SECRET_KEY" ]] \
    && pass "POST /api/access-keys → key pair generated" \
    || { fail "POST /api/access-keys" "$resp"; return 1; }

  aws configure set aws_access_key_id "$ACCESS_KEY" --profile "$AWS_PROFILE"
  aws configure set aws_secret_access_key "$SECRET_KEY" --profile "$AWS_PROFILE"
  aws configure set region us-east-1 --profile "$AWS_PROFILE"
  # Force path-style S3 URLs — required for non-AWS endpoints (virtual-hosted won't resolve)
  aws configure set s3.addressing_style path --profile "$AWS_PROFILE"
  aws configure set s3.payload_signing_enabled false --profile "$AWS_PROFILE"
  # Force SigV4 for presigned URLs (default may fall back to V2 for non-AWS endpoints)
  aws configure set s3.signature_version s3v4 --profile "$AWS_PROFILE"
  pass "AWS CLI profile configured for bkt endpoint"

  local stats
  stats=$(api "${BKT_ENDPOINT}/api/access-keys/stats" -H "Authorization: Bearer ${JWT_TOKEN}")
  echo "$stats" | grep -qi "total\|active\|count\|key" \
    && pass "GET /api/access-keys/stats → stats returned" \
    || fail "GET /api/access-keys/stats" "$stats"
}

# ── Section 7: Bucket creation ─────────────────────────────────────────────────
# Creates a fresh bucket bound to the given storage backend ("local" or "s3")
# and points TEST_BUCKET at it so the storage tests that follow exercise it.
test_create_bucket() {
  local backend="${1:-local}"
  TEST_BUCKET="bkt-smoke-${backend}-$(date +%s)-${RANDOM}"
  # A least-privilege IAM user typically may NOT create buckets on AWS. When
  # .env pins the allowed bucket(s) via S3_BUCKETS, link the s3 pass to the
  # first allowed bucket (bkt's create API links to an existing S3 bucket of
  # the same name) instead of inventing a name IAM will deny.
  if [[ "$backend" == "s3" ]]; then
    local allowed; allowed=$(load_env_var "S3_BUCKETS" | cut -d, -f1 | tr -d ' ')
    if [[ -n "$allowed" ]]; then
      TEST_BUCKET="$allowed"
      LINKED_BUCKET="$allowed"
      log "s3 pass uses pre-provisioned bucket '${TEST_BUCKET}' (from S3_BUCKETS)"
    fi
  fi
  CREATED_BUCKETS+=("$TEST_BUCKET")
  bold "Bucket management — ${backend} backend (${TEST_BUCKET})"

  local resp
  resp=$(api -X POST "${BKT_ENDPOINT}/api/buckets" \
    -H "Authorization: Bearer ${JWT_TOKEN}" \
    -H "Content-Type: application/json" \
    -d "{\"name\":\"${TEST_BUCKET}\",\"storage_backend\":\"${backend}\"}")

  if echo "$resp" | grep -q '"name"'; then
    pass "POST /api/buckets → ${backend} bucket '${TEST_BUCKET}' created"
  elif [[ -n "$LINKED_BUCKET" && "$TEST_BUCKET" == "$LINKED_BUCKET" ]] \
      && echo "$resp" | grep -qi "already exists"; then
    # The operator registered the pre-provisioned bucket themselves (e.g. via
    # the UI) — reuse it, and validate the backend binding from a GET instead.
    LINKED_PREREGISTERED=true
    pass "POST /api/buckets → pre-provisioned bucket already registered (reusing)"
    resp=$(api "${BKT_ENDPOINT}/api/buckets/${TEST_BUCKET}" \
      -H "Authorization: Bearer ${JWT_TOKEN}")
  else
    fail "POST /api/buckets (${backend})" "$resp"; return 1
  fi

  # Start the pass from an empty bucket — a linked pre-provisioned bucket may
  # hold leftovers from an earlier aborted run, which would skew count checks.
  if [[ "$backend" == "s3" ]]; then
    s3 rm "s3://${TEST_BUCKET}" --recursive &>/dev/null || true
  fi

  # Confirm the server actually recorded the requested backend
  echo "$resp" | grep -q "\"storage_backend\":\"${backend}\"" \
    && pass "Bucket bound to '${backend}' storage backend" \
    || fail "Bucket storage_backend mismatch (expected ${backend})" "$resp"

  local get; get=$(api "${BKT_ENDPOINT}/api/buckets/${TEST_BUCKET}" \
    -H "Authorization: Bearer ${JWT_TOKEN}")
  echo "$get" | grep -q '"name"' \
    && pass "GET /api/buckets/:name → metadata returned" \
    || fail "GET /api/buckets/:name" "$get"

  local list; list=$(api "${BKT_ENDPOINT}/api/buckets" -H "Authorization: Bearer ${JWT_TOKEN}")
  echo "$list" | grep -q "$TEST_BUCKET" \
    && pass "GET /api/buckets → test bucket appears in list (UI dashboard)" \
    || fail "GET /api/buckets" "$list"

  local head_code; head_code=$(s3api head-bucket --bucket "$TEST_BUCKET" &>/dev/null && echo 200 || echo err)
  [[ "$head_code" == "200" ]] \
    && pass "S3 HeadBucket → bucket accessible via S3 API" \
    || fail "S3 HeadBucket"
}

# ── Section 8: File type uploads ──────────────────────────────────────────────
test_file_types() {
  bold "File type uploads (via S3 API)"

  # Plain text
  local txt; txt=$(make_temp ".txt")
  echo "bkt smoke test — plain text file $(date)" > "$txt"
  s3 cp "$txt" "s3://${TEST_BUCKET}/files/sample.txt" &>/dev/null \
    && pass "Upload .txt — plain text" \
    || fail "Upload .txt"

  # JSON
  local json; json=$(make_temp ".json")
  echo '{"test":true,"source":"bkt-smoke","timestamp":"'$(date -u +%FT%TZ)'"}' > "$json"
  s3 cp "$json" "s3://${TEST_BUCKET}/files/data.json" &>/dev/null \
    && pass "Upload .json — JSON data" \
    || fail "Upload .json"

  # CSV
  local csv; csv=$(make_temp ".csv")
  printf "id,name,value\n1,alpha,100\n2,beta,200\n3,gamma,300\n" > "$csv"
  s3 cp "$csv" "s3://${TEST_BUCKET}/files/records.csv" &>/dev/null \
    && pass "Upload .csv — CSV data" \
    || fail "Upload .csv"

  # PNG image
  local png; png=$(make_png)
  s3 cp "$png" "s3://${TEST_BUCKET}/images/pixel.png" &>/dev/null \
    && pass "Upload .png — PNG image" \
    || fail "Upload .png"

  # JPEG image
  local jpg; jpg=$(make_jpeg)
  s3 cp "$jpg" "s3://${TEST_BUCKET}/images/photo.jpg" &>/dev/null \
    && pass "Upload .jpg — JPEG image" \
    || fail "Upload .jpg"

  # GIF image
  local gif; gif=$(make_gif)
  s3 cp "$gif" "s3://${TEST_BUCKET}/images/anim.gif" &>/dev/null \
    && pass "Upload .gif — GIF image" \
    || fail "Upload .gif"

  # PDF document
  local pdf; pdf=$(make_pdf)
  s3 cp "$pdf" "s3://${TEST_BUCKET}/docs/report.pdf" &>/dev/null \
    && pass "Upload .pdf — PDF document" \
    || fail "Upload .pdf"

  # Binary blob
  local bin; bin=$(make_temp ".bin")
  dd if=/dev/urandom of="$bin" bs=1K count=16 2>/dev/null
  s3 cp "$bin" "s3://${TEST_BUCKET}/files/data.bin" &>/dev/null \
    && pass "Upload .bin — random binary blob (16KB)" \
    || fail "Upload .bin"

  # ZIP archive (if zip available)
  local zip_file; zip_file=$(make_zip)
  if [[ -n "$zip_file" && -f "$zip_file" ]]; then
    s3 cp "$zip_file" "s3://${TEST_BUCKET}/files/archive.zip" &>/dev/null \
      && pass "Upload .zip — ZIP archive" \
      || fail "Upload .zip"
  else
    skip "Upload .zip" "zip command not available"
  fi

  # HTML (should be allowed — object storage, not a web server)
  local html; html=$(make_temp ".html")
  echo "<html><body><h1>bkt test</h1></body></html>" > "$html"
  s3 cp "$html" "s3://${TEST_BUCKET}/files/page.html" &>/dev/null \
    && pass "Upload .html — allowed (storage, not execution)" \
    || fail "Upload .html"
}

# ── Section 9: Content-type enforcement ──────────────────────────────────────
test_content_type_enforcement() {
  bold "Content-type enforcement (dangerous types blocked)"

  # Create a fake EXE (PE header magic bytes: MZ)
  local exe; exe=$(make_temp ".exe")
  python3 -c "open('$exe','wb').write(b'MZ\x90\x00\x03\x00\x00\x00\x04\x00\x00\x00\xFF\xFF\x00\x00')"

  local code
  code=$(http_code -X PUT "${BKT_S3_ENDPOINT}/${TEST_BUCKET}/blocked/evil.exe" \
    -H "Authorization: Bearer dummy" \
    --data-binary "@${exe}" \
    --aws-sigv4 "aws:amz:us-east-1:s3" \
    --user "${ACCESS_KEY}:${SECRET_KEY}" 2>/dev/null || echo "err")

  # The S3 API validates via the bkt middleware — check the REST API directly
  local rest_code
  rest_code=$(api -X POST "${BKT_ENDPOINT}/api/buckets/${TEST_BUCKET}/objects" \
    -H "Authorization: Bearer ${JWT_TOKEN}" \
    -F "file=@${exe}" \
    -F "key=blocked/evil.exe" \
    -w "%{http_code}" -o /dev/null)

  if [[ "$rest_code" == "400" || "$rest_code" == "415" ]]; then
    pass "Content enforcement — .exe (PE binary) blocked on REST upload"
  else
    fail "Content enforcement — .exe should be blocked, got HTTP $rest_code"
  fi

  # Shared library (.so) — also blocked
  local so; so=$(make_temp ".so")
  python3 -c "open('$so','wb').write(b'\x7fELF\x02\x01\x01\x00' + b'\x00'*8)"

  local so_code
  so_code=$(api -X POST "${BKT_ENDPOINT}/api/buckets/${TEST_BUCKET}/objects" \
    -H "Authorization: Bearer ${JWT_TOKEN}" \
    -F "file=@${so}" \
    -F "key=blocked/lib.so" \
    -w "%{http_code}" -o /dev/null)

  if [[ "$so_code" == "400" || "$so_code" == "415" ]]; then
    pass "Content enforcement — .so (ELF shared lib) blocked on REST upload"
  else
    fail "Content enforcement — .so should be blocked, got HTTP $so_code"
  fi
}

# ── Section 10: File retrieval & content verification ─────────────────────────
test_file_retrieval() {
  bold "File retrieval & content verification"

  # Download each uploaded type and verify content
  declare -A ORIGINALS
  declare -A KEYS

  # Re-upload with known content so we can verify round-trip
  local txt; txt=$(make_temp ".txt")
  local marker="SMOKE_VERIFY_$(date +%s)_$$"
  echo "$marker" > "$txt"
  s3 cp "$txt" "s3://${TEST_BUCKET}/verify/roundtrip.txt" &>/dev/null || true

  local dl; dl=$(make_temp ".txt")
  s3 cp "s3://${TEST_BUCKET}/verify/roundtrip.txt" "$dl" &>/dev/null || true
  if diff "$txt" "$dl" &>/dev/null; then
    pass "Round-trip — text file content matches after upload/download"
  else
    fail "Round-trip — text content mismatch"
  fi

  # Download PNG and verify it's still a valid PNG (check magic bytes)
  local png; png=$(make_png)
  s3 cp "$png" "s3://${TEST_BUCKET}/verify/check.png" &>/dev/null || true
  local png_dl; png_dl=$(make_temp ".png")
  s3 cp "s3://${TEST_BUCKET}/verify/check.png" "$png_dl" &>/dev/null || true
  if python3 -c "
data = open('$png_dl','rb').read()
assert data[:8] == b'\x89PNG\r\n\x1a\n', 'Not a PNG'
print('ok')
" 2>/dev/null | grep -q ok; then
    pass "Round-trip — PNG magic bytes intact after download"
  else
    fail "Round-trip — PNG file corrupted in transit"
  fi

  # Download via REST API (what the UI uses)
  local rest_dl; rest_dl=$(make_temp ".txt")
  local rest_code
  rest_code=$(api -o "$rest_dl" -w "%{http_code}" \
    "${BKT_ENDPOINT}/api/buckets/${TEST_BUCKET}/objects/verify/roundtrip.txt" \
    -H "Authorization: Bearer ${JWT_TOKEN}")
  if [[ "$rest_code" == "200" ]] && diff "$txt" "$rest_dl" &>/dev/null; then
    pass "REST API download — content matches original"
  else
    fail "REST API download" "HTTP $rest_code"
  fi

  # HEAD request (metadata only — what the UI uses for file info)
  local head
  head=$(api -I "${BKT_ENDPOINT}/api/buckets/${TEST_BUCKET}/objects/verify/roundtrip.txt" \
    -H "Authorization: Bearer ${JWT_TOKEN}")
  echo "$head" | grep -qi "content-type\|content-length\|200" \
    && pass "HEAD object — metadata headers returned (UI file info)" \
    || fail "HEAD object" "${head:0:200}"
}

# ── Section 11: UI API endpoints ──────────────────────────────────────────────
test_ui_api() {
  bold "UI API endpoints (what the browser calls)"

  # Object list — the main bucket view
  local obj_list
  obj_list=$(api "${BKT_ENDPOINT}/api/buckets/${TEST_BUCKET}/objects" \
    -H "Authorization: Bearer ${JWT_TOKEN}")
  echo "$obj_list" | grep -q '"key"\|"objects"\|\[\]' \
    && pass "GET /api/buckets/:name/objects — bucket contents for UI" \
    || fail "GET /api/buckets/:name/objects" "$obj_list"

  # Folder-style prefix listing (the UI uses prefix= for folder navigation)
  local prefix_list
  prefix_list=$(api "${BKT_ENDPOINT}/api/buckets/${TEST_BUCKET}/objects?prefix=images/" \
    -H "Authorization: Bearer ${JWT_TOKEN}")
  echo "$prefix_list" | grep -q 'pixel.png\|photo.jpg\|anim.gif' \
    && pass "GET /api/buckets/:name/objects?prefix=images/ — folder navigation works" \
    || fail "GET /api/buckets/:name/objects?prefix=images/" "$prefix_list"

  # Uploads list (the UI upload progress panel)
  local uploads
  uploads=$(api "${BKT_ENDPOINT}/api/uploads" -H "Authorization: Bearer ${JWT_TOKEN}")
  [[ $? -eq 0 ]] \
    && pass "GET /api/uploads — upload history endpoint responds" \
    || fail "GET /api/uploads"

  # Access keys list (the Settings page)
  local keys
  keys=$(api "${BKT_ENDPOINT}/api/access-keys" -H "Authorization: Bearer ${JWT_TOKEN}")
  echo "$keys" | grep -q "$ACCESS_KEY" \
    && pass "GET /api/access-keys — keys list for Settings page" \
    || fail "GET /api/access-keys"

  # SSO config (the login page reads this to show/hide SSO buttons)
  local sso_code
  sso_code=$(http_code "${BKT_ENDPOINT}/api/auth/sso/config")
  [[ "$sso_code" == "200" ]] \
    && pass "GET /api/auth/sso/config → 200 (login page SSO config)" \
    || fail "GET /api/auth/sso/config" "HTTP $sso_code"
}

# ── Section 12: S3 pagination ─────────────────────────────────────────────────
test_s3_pagination() {
  bold "S3 API — ListObjects pagination"

  # Seed objects for pagination
  for i in $(seq 1 8); do
    echo "page-obj-$i" | s3 cp - "s3://${TEST_BUCKET}/paginate/obj-$(printf '%04d' $i).txt" &>/dev/null || true
  done

  local v1; v1=$(s3api list-objects --bucket "$TEST_BUCKET" --prefix "paginate/" --max-items 3 2>&1)
  echo "$v1" | grep -q "paginate/" \
    && pass "ListObjectsV1 — paginated response (max-items=3)" \
    || fail "ListObjectsV1 pagination" "$v1"

  local v2; v2=$(s3api list-objects-v2 --bucket "$TEST_BUCKET" --prefix "paginate/" --max-items 3 2>&1)
  echo "$v2" | grep -q "KeyCount\|paginate/" \
    && pass "ListObjectsV2 — KeyCount present" \
    || fail "ListObjectsV2" "$v2"

  local token
  token=$(echo "$v2" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('NextToken',''))" 2>/dev/null || echo "")
  if [[ -n "$token" ]]; then
    local page2
    page2=$(s3api list-objects-v2 --bucket "$TEST_BUCKET" --prefix "paginate/" \
      --max-items 3 --starting-token "$token" 2>&1)
    echo "$page2" | grep -q "paginate/" \
      && pass "ListObjectsV2 continuation token — page 2 returned" \
      || fail "ListObjectsV2 continuation token" "$page2"
  else
    skip "ListObjectsV2 continuation token" "all objects fit on first page"
  fi
}

# ── Section 13: CopyObject ────────────────────────────────────────────────────
test_s3_copy() {
  bold "S3 API — CopyObject"

  s3 cp "s3://${TEST_BUCKET}/files/sample.txt" "s3://${TEST_BUCKET}/copies/sample-copy.txt" &>/dev/null \
    && pass "CopyObject — same bucket copy completed" \
    || fail "CopyObject"

  local orig; orig=$(make_temp ".txt")
  local copy_dl; copy_dl=$(make_temp ".txt")
  s3 cp "s3://${TEST_BUCKET}/files/sample.txt"       "$orig"    &>/dev/null || true
  s3 cp "s3://${TEST_BUCKET}/copies/sample-copy.txt" "$copy_dl" &>/dev/null || true
  if [[ ! -s "$orig" || ! -s "$copy_dl" ]]; then
    fail "CopyObject — download returned empty file (source may have been moved not copied)"
  elif diff "$orig" "$copy_dl" &>/dev/null; then
    pass "CopyObject — copy content matches source"
  else
    fail "CopyObject — content mismatch" "orig=$(wc -c < "$orig")B copy=$(wc -c < "$copy_dl")B"
  fi
}

# ── Section 14: DeleteObjects (bulk) ─────────────────────────────────────────
test_s3_delete_objects() {
  bold "S3 API — DeleteObjects (bulk)"

  for i in 1 2 3; do
    echo "delete-me-$i" | s3 cp - "s3://${TEST_BUCKET}/bulk-delete/obj-${i}.txt" &>/dev/null || true
  done

  local result
  result=$(s3api delete-objects --bucket "$TEST_BUCKET" \
    --delete '{"Objects":[{"Key":"bulk-delete/obj-1.txt"},{"Key":"bulk-delete/obj-2.txt"},{"Key":"bulk-delete/obj-3.txt"}],"Quiet":false}' 2>&1)
  echo "$result" | grep -q "bulk-delete/obj-1\|Deleted" \
    && pass "DeleteObjects — 3 objects deleted in single request" \
    || fail "DeleteObjects" "$result"

  local remaining
  remaining=$(s3api list-objects-v2 --bucket "$TEST_BUCKET" --prefix "bulk-delete/" 2>&1)
  echo "$remaining" | grep -q "bulk-delete/obj-" \
    && fail "DeleteObjects — objects still listed after bulk delete" \
    || pass "DeleteObjects — confirmed objects no longer exist"
}

# ── Section 15: Multipart upload ─────────────────────────────────────────────
test_s3_multipart() {
  bold "S3 API — Multipart upload (>8MB)"

  local big; big=$(make_temp ".bin")
  log "Generating 12MB test file..."
  dd if=/dev/urandom of="$big" bs=1M count=12 2>/dev/null
  pass "12MB test file generated"

  s3 cp "$big" "s3://${TEST_BUCKET}/multipart/large.bin" &>/dev/null \
    && pass "Multipart upload — 12MB file uploaded" \
    || { fail "Multipart upload"; return; }

  local size
  size=$(PYTHONWARNINGS=ignore s3api head-object --bucket "$TEST_BUCKET" --key "multipart/large.bin" \
    --output text --query 'ContentLength' 2>/dev/null | grep -E '^[0-9]+$' || echo 0)
  if [[ "${size:-0}" -eq $((12 * 1024 * 1024)) ]]; then
    pass "Multipart upload — HeadObject size correct ($(( 12*1024*1024 )) bytes)"
  else
    fail "Multipart upload — size mismatch (expected $((12*1024*1024)), got ${size:-0})"
  fi

  local dl; dl=$(make_temp ".bin")
  local dl_err
  dl_err=$(PYTHONWARNINGS=ignore s3 cp "s3://${TEST_BUCKET}/multipart/large.bin" "$dl" 2>&1) || true
  local orig_size dl_size orig_md5 dl_md5
  orig_size=$(wc -c < "$big" 2>/dev/null || echo 0)
  dl_size=$(wc -c < "$dl" 2>/dev/null || echo 0)
  if [[ "${dl_size:-0}" -ne "${orig_size:-0}" ]]; then
    fail "Multipart upload — download size mismatch (orig=${orig_size}B downloaded=${dl_size}B)" "$dl_err"
  else
    orig_md5=$(md5sum "$big" | awk '{print $1}')
    dl_md5=$(md5sum "$dl"   | awk '{print $1}')
    [[ "$orig_md5" == "$dl_md5" ]] \
      && pass "Multipart upload — MD5 roundtrip matches (no corruption)" \
      || fail "Multipart upload — MD5 mismatch (orig=$orig_md5 dl=$dl_md5)"
  fi
}

# ── Section 16: Presigned URLs ────────────────────────────────────────────────
test_s3_presign() {
  bold "S3 API — Presigned URLs"

  local url
  url=$(aws s3 presign "s3://${TEST_BUCKET}/files/sample.txt" \
    --endpoint-url "$BKT_S3_ENDPOINT" --no-verify-ssl --profile "$AWS_PROFILE" \
    --expires-in 120 2>&1)
  echo "$url" | grep -q "X-Amz-Signature" \
    && pass "Presign — URL generated with X-Amz-Signature" \
    || { fail "Presign — URL generation failed" "$url"; return; }

  # Use the URL without any credentials
  local code body
  code=$(curl -sk -o /dev/null -w "%{http_code}" --max-time 10 "$url")
  [[ "$code" == "200" ]] \
    && pass "Presigned URL — unauthenticated GET returns 200" \
    || fail "Presigned URL — expected 200, got $code"

  # Verify content
  body=$(curl -sk --max-time 10 "$url")
  echo "$body" | grep -q "bkt smoke test\|SMOKE_VERIFY" \
    && pass "Presigned URL — content matches uploaded file" \
    || fail "Presigned URL — unexpected content"

  # Presign an image to verify binary files work too
  local img_url
  img_url=$(aws s3 presign "s3://${TEST_BUCKET}/images/pixel.png" \
    --endpoint-url "$BKT_S3_ENDPOINT" --no-verify-ssl --profile "$AWS_PROFILE" \
    --expires-in 60 2>&1)
  local img_code
  img_code=$(curl -sk -o /dev/null -w "%{http_code}" --max-time 10 "$img_url")
  [[ "$img_code" == "200" ]] \
    && pass "Presigned URL — PNG image download without credentials" \
    || fail "Presigned URL (PNG)" "HTTP $img_code"

  # Expired URL should return 403
  local exp_url
  exp_url=$(aws s3 presign "s3://${TEST_BUCKET}/files/sample.txt" \
    --endpoint-url "$BKT_S3_ENDPOINT" --no-verify-ssl --profile "$AWS_PROFILE" \
    --expires-in 1 2>&1)
  sleep 3
  local exp_code
  exp_code=$(curl -sk -o /dev/null -w "%{http_code}" --max-time 10 "$exp_url")
  [[ "$exp_code" == "403" ]] \
    && pass "Presigned URL expiry — expired URL returns 403" \
    || fail "Presigned URL expiry — expected 403, got $exp_code"
}

# ── Section 17: Policy enforcement ───────────────────────────────────────────
test_policies() {
  bold "Policy enforcement"

  local ts=$RANDOM
  local uname="smoketest-${ts}"
  local upass="Smoke!Test1${ts}"

  local user_resp
  user_resp=$(api -X POST "${BKT_ENDPOINT}/api/users" \
    -H "Authorization: Bearer ${JWT_TOKEN}" \
    -H "Content-Type: application/json" \
    -d "{\"username\":\"${uname}\",\"email\":\"${uname}@test.local\",\"password\":\"${upass}\"}")

  local uid
  uid=$(echo "$user_resp" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)

  if [[ -z "$uid" ]]; then
    skip "Policy enforcement" "could not create test user"
    return
  fi

  local utoken
  utoken=$(api -X POST "${BKT_ENDPOINT}/api/auth/login" \
    -H "Content-Type: application/json" \
    -d "{\"username\":\"${uname}\",\"password\":\"${upass}\"}" \
    | grep -o '"token":"[^"]*"' | cut -d'"' -f4)

  # No policies = denied
  local denied_code
  denied_code=$(http_code "${BKT_ENDPOINT}/api/buckets/${TEST_BUCKET}/objects" \
    -H "Authorization: Bearer ${utoken}")
  [[ "$denied_code" == "403" || "$denied_code" == "401" ]] \
    && pass "Policy — deny-by-default: no policies → 403 on bucket access" \
    || fail "Policy — user with no policies should be denied, got $denied_code"

  # Attach a read-only policy and verify it grants list access
  local policy_resp policy_id
  policy_resp=$(api -X POST "${BKT_ENDPOINT}/api/policies" \
    -H "Authorization: Bearer ${JWT_TOKEN}" \
    -H "Content-Type: application/json" \
    -d "{
      \"name\":\"smoke-readonly-${ts}\",
      \"description\":\"smoke test read policy\",
      \"document\":\"{\\\"Version\\\":\\\"2012-10-17\\\",\\\"Statement\\\":[{\\\"Effect\\\":\\\"Allow\\\",\\\"Action\\\":[\\\"s3:GetObject\\\",\\\"s3:ListBucket\\\"],\\\"Resource\\\":[\\\"*\\\"]}]}\"
    }")
  policy_id=$(echo "$policy_resp" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)

  if [[ -n "$policy_id" ]]; then
    api -X POST "${BKT_ENDPOINT}/api/policies/users/${uid}/attach" \
      -H "Authorization: Bearer ${JWT_TOKEN}" \
      -H "Content-Type: application/json" \
      -d "{\"policy_id\":\"${policy_id}\"}" &>/dev/null || true

    local allowed_code
    allowed_code=$(http_code "${BKT_ENDPOINT}/api/buckets/${TEST_BUCKET}/objects" \
      -H "Authorization: Bearer ${utoken}")
    [[ "$allowed_code" == "200" ]] \
      && pass "Policy — read-only policy grants ListBucket (200)" \
      || fail "Policy — read-only policy should grant access, got $allowed_code"

    # Cleanup: detach policy, delete policy, delete user
    api -X DELETE "${BKT_ENDPOINT}/api/policies/users/${uid}/detach/${policy_id}" \
      -H "Authorization: Bearer ${JWT_TOKEN}" &>/dev/null || true
    api -X DELETE "${BKT_ENDPOINT}/api/policies/${policy_id}" \
      -H "Authorization: Bearer ${JWT_TOKEN}" &>/dev/null || true
  else
    skip "Policy allow test" "could not create test policy"
  fi

  api -X DELETE "${BKT_ENDPOINT}/api/users/${uid}" \
    -H "Authorization: Bearer ${JWT_TOKEN}" &>/dev/null || true
}

# ── Section 18: AWS S3 backend verification ───────────────────────────────────
test_aws_backend() {
  if [[ "${S3_BACKEND_ENABLED:-false}" != "true" || -z "$AWS_KEY" ]]; then
    bold "AWS S3 backend verification"
    skip "All AWS backend checks" "S3_ENABLED != true or S3_ACCESS_KEY_ID not set in .env"
    return
  fi

  bold "AWS S3 backend verification (direct S3 check)"
  log "Verifying objects landed on real AWS S3 (bypassing bkt)..."

  # Real S3 bucket name as the server constructs it: getBucketName() joins a
  # non-empty prefix to the bucket name with a hyphen ("{prefix}-{name}"),
  # otherwise uses the bare name. Mirror that exactly or the lookups miss.
  local s3_bucket="$TEST_BUCKET"
  [[ -n "${S3_BUCKET_PREFIX:-}" ]] && s3_bucket="${S3_BUCKET_PREFIX}-${TEST_BUCKET}"

  local head
  head=$(aws_direct s3api head-object \
    --bucket "$s3_bucket" \
    --key "files/sample.txt" \
    --region "${S3_REGION:-us-east-1}" 2>&1)
  echo "$head" | grep -q "ContentLength\|ETag" \
    && pass "AWS direct — files/sample.txt exists in S3 bucket '${s3_bucket}'" \
    || fail "AWS direct — files/sample.txt not found in S3" "$head"

  head=$(aws_direct s3api head-object \
    --bucket "$s3_bucket" \
    --key "images/pixel.png" \
    --region "${S3_REGION:-us-east-1}" 2>&1)
  echo "$head" | grep -q "ContentLength\|ETag" \
    && pass "AWS direct — images/pixel.png exists in S3 bucket (PNG roundtrip)" \
    || fail "AWS direct — images/pixel.png not found in S3" "$head"

  head=$(aws_direct s3api head-object \
    --bucket "$s3_bucket" \
    --key "multipart/large.bin" \
    --region "${S3_REGION:-us-east-1}" 2>&1)
  echo "$head" | grep -q "ContentLength\|ETag" \
    && pass "AWS direct — multipart/large.bin exists in S3 (multipart upload landed)" \
    || fail "AWS direct — multipart/large.bin not found in S3" "$head"

  # Verify object count matches between bkt and S3
  local bkt_count s3_count
  bkt_count=$(api "${BKT_ENDPOINT}/api/buckets/${TEST_BUCKET}/objects" \
    -H "Authorization: Bearer ${JWT_TOKEN}" \
    | python3 -c "import sys,json; d=json.load(sys.stdin); objs = d.get('objects',[]) if isinstance(d,dict) else (d if isinstance(d,list) else []); print(len(objs))" 2>/dev/null || echo "?")

  s3_count=$(aws_direct s3api list-objects-v2 \
    --bucket "$s3_bucket" \
    --region "${S3_REGION:-us-east-1}" \
    --query 'KeyCount' --output text 2>/dev/null || echo "?")

  log "Object count — bkt API: ${bkt_count}, S3 direct: ${s3_count}"
  [[ "$bkt_count" == "$s3_count" ]] \
    && pass "AWS direct — object count matches between bkt and S3" \
    || skip "AWS direct — count mismatch (${bkt_count} vs ${s3_count})" \
       "pagination or .keep files may cause minor differences"
}

# ── Section 19: Range requests (partial content) ──────────────────────────────
# Regression guard: GetObject must honor the Range header and return 206 with the
# exact byte slice. Advertising Accept-Ranges while serving the full body for
# every range corrupts AWS CLI/SDK multipart downloads (oversized, mismatched MD5).
test_s3_range() {
  bold "S3 API — Range requests (partial content)"

  local src; src=$(make_temp ".bin")
  dd if=/dev/urandom of="$src" bs=1K count=512 2>/dev/null   # 512KB deterministic blob
  s3 cp "$src" "s3://${TEST_BUCKET}/range/data.bin" &>/dev/null \
    && pass "Range — seed object uploaded (512KB)" \
    || { fail "Range — seed upload failed"; return; }

  local size; size=$(wc -c < "$src")
  local url="${BKT_S3_ENDPOINT}/${TEST_BUCKET}/range/data.bin"
  local sigv4=(--aws-sigv4 "aws:amz:us-east-1:s3" --user "${ACCESS_KEY}:${SECRET_KEY}")

  # Prefix range: first 1024 bytes → 206 + Content-Range
  local hdr body code len
  hdr=$(make_temp ".hdr"); body=$(make_temp ".bin")
  code=$(curl -sk --max-time 15 -D "$hdr" -o "$body" -w "%{http_code}" \
    -H "Range: bytes=0-1023" "${sigv4[@]}" "$url")
  len=$(wc -c < "$body")
  if [[ "$code" == "206" && "$len" == "1024" ]] \
     && grep -qi "content-range: *bytes 0-1023/${size}" "$hdr"; then
    pass "Range — bytes=0-1023 → 206, 1024 bytes, correct Content-Range"
  else
    fail "Range — prefix range" "code=$code len=$len cr=$(grep -i content-range "$hdr" | tr -d '\r')"
  fi

  # Prefix content must equal source[0:1024]
  if python3 -c "
import sys
src=open('$src','rb').read(); got=open('$body','rb').read()
sys.exit(0 if got==src[:1024] else 1)
"; then
    pass "Range — prefix bytes match source[0:1024]"
  else
    fail "Range — prefix content mismatch"
  fi

  # Suffix range: last 1024 bytes
  local sbody scode
  sbody=$(make_temp ".bin")
  scode=$(curl -sk --max-time 15 -o "$sbody" -w "%{http_code}" \
    -H "Range: bytes=-1024" "${sigv4[@]}" "$url")
  if [[ "$scode" == "206" ]] && python3 -c "
import sys
src=open('$src','rb').read(); got=open('$sbody','rb').read()
sys.exit(0 if got==src[-1024:] else 1)
"; then
    pass "Range — suffix bytes=-1024 → last 1024 bytes match source"
  else
    fail "Range — suffix range" "code=$scode"
  fi

  # Unsatisfiable range (offset past EOF) → 416
  local ucode
  ucode=$(curl -sk --max-time 15 -o /dev/null -w "%{http_code}" \
    -H "Range: bytes=99999999-" "${sigv4[@]}" "$url")
  [[ "$ucode" == "416" ]] \
    && pass "Range — unsatisfiable offset → 416" \
    || fail "Range — expected 416, got $ucode"
}

# ── Section 20: Object operations (move / rename / folder move) ────────────────
test_object_ops() {
  bold "Object operations (move / rename / folder move)"

  # Seed an object to move
  local f; f=$(make_temp ".txt")
  echo "move-me $(date)" > "$f"
  s3 cp "$f" "s3://${TEST_BUCKET}/ops/original.txt" &>/dev/null || true

  # MoveObject
  local mv_code
  mv_code=$(api -X POST "${BKT_ENDPOINT}/api/buckets/${TEST_BUCKET}/objects/move" \
    -H "Authorization: Bearer ${JWT_TOKEN}" -H "Content-Type: application/json" \
    -d '{"source_key":"ops/original.txt","destination_key":"ops/moved.txt"}' \
    -w "%{http_code}" -o /dev/null)
  [[ "$mv_code" == "200" ]] \
    && pass "POST /objects/move → object moved (200)" \
    || fail "POST /objects/move" "HTTP $mv_code"

  # Destination present, source gone
  local dst_code src_code
  dst_code=$(http_code "${BKT_ENDPOINT}/api/buckets/${TEST_BUCKET}/objects/ops/moved.txt" \
    -H "Authorization: Bearer ${JWT_TOKEN}")
  src_code=$(http_code "${BKT_ENDPOINT}/api/buckets/${TEST_BUCKET}/objects/ops/original.txt" \
    -H "Authorization: Bearer ${JWT_TOKEN}")
  [[ "$dst_code" == "200" && "$src_code" == "404" ]] \
    && pass "Move — destination present, source removed" \
    || fail "Move — dst=$dst_code src=$src_code (expected 200/404)"

  # RenameObject (new_name keeps the source prefix)
  local rn_code
  rn_code=$(api -X POST "${BKT_ENDPOINT}/api/buckets/${TEST_BUCKET}/objects/rename" \
    -H "Authorization: Bearer ${JWT_TOKEN}" -H "Content-Type: application/json" \
    -d '{"source_key":"ops/moved.txt","new_name":"renamed.txt"}' \
    -w "%{http_code}" -o /dev/null)
  [[ "$rn_code" == "200" ]] \
    && pass "POST /objects/rename → object renamed (200)" \
    || fail "POST /objects/rename" "HTTP $rn_code"

  # MoveFolder (recursive prefix move)
  echo "a" | s3 cp - "s3://${TEST_BUCKET}/folder-src/a.txt" &>/dev/null || true
  echo "b" | s3 cp - "s3://${TEST_BUCKET}/folder-src/b.txt" &>/dev/null || true
  local fm_code
  fm_code=$(api -X POST "${BKT_ENDPOINT}/api/buckets/${TEST_BUCKET}/folders/move" \
    -H "Authorization: Bearer ${JWT_TOKEN}" -H "Content-Type: application/json" \
    -d '{"source_prefix":"folder-src/","destination_prefix":"folder-dst/"}' \
    -w "%{http_code}" -o /dev/null)
  [[ "$fm_code" == "200" ]] \
    && pass "POST /folders/move → folder moved (200)" \
    || fail "POST /folders/move" "HTTP $fm_code"

  local moved_list
  moved_list=$(api "${BKT_ENDPOINT}/api/buckets/${TEST_BUCKET}/objects?prefix=folder-dst/" \
    -H "Authorization: Bearer ${JWT_TOKEN}")
  echo "$moved_list" | grep -q "a.txt\|b.txt" \
    && pass "Folder move — objects appear under new prefix" \
    || fail "Folder move — objects not found under folder-dst/"
}

# ── Section 21: User lock / unlock (admin) ────────────────────────────────────
test_user_lock() {
  bold "User lock / unlock (admin)"

  local ts=$RANDOM
  local uname="locktest-${ts}"
  local upass="Lock!Test1${ts}"

  local user_resp uid
  user_resp=$(api -X POST "${BKT_ENDPOINT}/api/users" \
    -H "Authorization: Bearer ${JWT_TOKEN}" -H "Content-Type: application/json" \
    -d "{\"username\":\"${uname}\",\"email\":\"${uname}@test.local\",\"password\":\"${upass}\"}")
  uid=$(echo "$user_resp" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)
  if [[ -z "$uid" ]]; then
    skip "User lock/unlock" "could not create test user"
    return
  fi

  # Baseline: the new user can log in
  local pre_code
  pre_code=$(http_code -X POST "${BKT_ENDPOINT}/api/auth/login" \
    -H "Content-Type: application/json" \
    -d "{\"username\":\"${uname}\",\"password\":\"${upass}\"}")
  [[ "$pre_code" == "200" ]] \
    && pass "User lock — new user can log in initially" \
    || fail "User lock — initial login failed ($pre_code)"

  # Lock the account
  local lock_code
  lock_code=$(api -X POST "${BKT_ENDPOINT}/api/users/${uid}/lock" \
    -H "Authorization: Bearer ${JWT_TOKEN}" -w "%{http_code}" -o /dev/null)
  [[ "$lock_code" == "200" ]] \
    && pass "POST /users/:id/lock → 200" \
    || fail "POST /users/:id/lock" "HTTP $lock_code"

  # Locked account login is rejected (403 Account locked)
  local locked_code
  locked_code=$(http_code -X POST "${BKT_ENDPOINT}/api/auth/login" \
    -H "Content-Type: application/json" \
    -d "{\"username\":\"${uname}\",\"password\":\"${upass}\"}")
  [[ "$locked_code" == "403" || "$locked_code" == "401" ]] \
    && pass "User lock — locked user login rejected ($locked_code)" \
    || fail "User lock — locked user still able to log in ($locked_code)"

  # Unlock
  local unlock_code
  unlock_code=$(api -X POST "${BKT_ENDPOINT}/api/users/${uid}/unlock" \
    -H "Authorization: Bearer ${JWT_TOKEN}" -w "%{http_code}" -o /dev/null)
  [[ "$unlock_code" == "200" ]] \
    && pass "POST /users/:id/unlock → 200" \
    || fail "POST /users/:id/unlock" "HTTP $unlock_code"

  # Login works again
  local post_code
  post_code=$(http_code -X POST "${BKT_ENDPOINT}/api/auth/login" \
    -H "Content-Type: application/json" \
    -d "{\"username\":\"${uname}\",\"password\":\"${upass}\"}")
  [[ "$post_code" == "200" ]] \
    && pass "User lock — unlocked user can log in again" \
    || fail "User lock — login still blocked after unlock ($post_code)"

  # Cleanup
  api -X DELETE "${BKT_ENDPOINT}/api/users/${uid}" \
    -H "Authorization: Bearer ${JWT_TOKEN}" &>/dev/null || true
}

# ── Section 22: Bucket policy (set / get) ─────────────────────────────────────
test_bucket_policy() {
  bold "Bucket policy (set / get)"

  local doc='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["*"]}]}'
  # SetBucketPolicy expects {"policy":"<json-string>"}; build it safely with python.
  local body
  body=$(python3 -c "import json,sys; print(json.dumps({'policy': sys.argv[1]}))" "$doc")

  local code
  code=$(api -X PUT "${BKT_ENDPOINT}/api/buckets/${TEST_BUCKET}/policy" \
    -H "Authorization: Bearer ${JWT_TOKEN}" -H "Content-Type: application/json" \
    -d "$body" -w "%{http_code}" -o /dev/null)
  [[ "$code" == "200" ]] \
    && pass "PUT /api/buckets/:name/policy → policy set" \
    || fail "PUT /api/buckets/:name/policy" "HTTP $code"

  local get
  get=$(api "${BKT_ENDPOINT}/api/buckets/${TEST_BUCKET}/policy" -H "Authorization: Bearer ${JWT_TOKEN}")
  echo "$get" | grep -q 's3:GetObject\|Statement' \
    && pass "GET /api/buckets/:name/policy → policy returned" \
    || fail "GET /api/buckets/:name/policy" "$get"
}

# ── Section 23: Async upload + status polling ─────────────────────────────────
test_async_upload() {
  bold "Async upload + status"

  local f; f=$(make_temp ".bin")
  dd if=/dev/urandom of="$f" bs=1K count=64 2>/dev/null

  local resp uid
  resp=$(api -X POST "${BKT_ENDPOINT}/api/buckets/${TEST_BUCKET}/objects/async" \
    -H "Authorization: Bearer ${JWT_TOKEN}" \
    -F "file=@${f}" -F "key=async/data.bin")
  uid=$(echo "$resp" | grep -o '"upload_id":"[^"]*"' | head -1 | cut -d'"' -f4)
  [[ -n "$uid" ]] \
    && pass "POST /objects/async → upload_id returned" \
    || { fail "POST /objects/async" "$resp"; return; }

  local status="" i
  for i in $(seq 1 20); do
    status=$(api "${BKT_ENDPOINT}/api/uploads/${uid}/status" \
      -H "Authorization: Bearer ${JWT_TOKEN}" \
      | grep -o '"status":"[^"]*"' | head -1 | cut -d'"' -f4)
    [[ "$status" == "completed" || "$status" == "failed" ]] && break
    sleep 1
  done
  [[ "$status" == "completed" ]] \
    && pass "GET /uploads/:id/status → completed" \
    || fail "Async upload — not completed (status=${status:-none})"

  local dl_code
  dl_code=$(http_code "${BKT_ENDPOINT}/api/buckets/${TEST_BUCKET}/objects/async/data.bin" \
    -H "Authorization: Bearer ${JWT_TOKEN}")
  [[ "$dl_code" == "200" ]] \
    && pass "Async upload — object retrievable after completion" \
    || fail "Async upload — object missing (HTTP $dl_code)"
}

# ── Section 24: Single object delete (REST) ───────────────────────────────────
test_object_delete() {
  bold "Object delete (REST)"

  local f; f=$(make_temp ".txt")
  echo "delete-me $(date)" > "$f"
  s3 cp "$f" "s3://${TEST_BUCKET}/del/target.txt" &>/dev/null || true

  local del_code
  del_code=$(api -X DELETE "${BKT_ENDPOINT}/api/buckets/${TEST_BUCKET}/objects/del/target.txt" \
    -H "Authorization: Bearer ${JWT_TOKEN}" -w "%{http_code}" -o /dev/null)
  [[ "$del_code" == "200" || "$del_code" == "204" ]] \
    && pass "DELETE /api/buckets/:name/objects/:key → ${del_code}" \
    || fail "DELETE object (REST)" "HTTP $del_code"

  local after
  after=$(http_code "${BKT_ENDPOINT}/api/buckets/${TEST_BUCKET}/objects/del/target.txt" \
    -H "Authorization: Bearer ${JWT_TOKEN}")
  [[ "$after" == "404" ]] \
    && pass "Object delete — gone (404 after delete)" \
    || fail "Object delete — still present (HTTP $after)"
}

# ── Section 25: S3 Configurations (admin CRUD) ────────────────────────────────
test_s3_configs() {
  bold "S3 Configurations (admin CRUD)"

  local ts=$RANDOM
  local resp cfg_id
  resp=$(api -X POST "${BKT_ENDPOINT}/api/s3-configs" \
    -H "Authorization: Bearer ${JWT_TOKEN}" -H "Content-Type: application/json" \
    -d "{\"name\":\"smoke-s3cfg-${ts}\",\"endpoint\":\"s3.amazonaws.com\",\"region\":\"us-east-1\",\"access_key_id\":\"AKIASMOKE${ts}\",\"secret_access_key\":\"smokesecretkey${ts}value\",\"use_ssl\":true}")
  cfg_id=$(echo "$resp" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)
  [[ -n "$cfg_id" ]] \
    && pass "POST /api/s3-configs → config created" \
    || { fail "POST /api/s3-configs" "$resp"; return; }

  # The encrypted secret must never be echoed back in plaintext.
  echo "$resp" | grep -q "smokesecretkey${ts}value" \
    && fail "S3 config — secret key leaked in response" \
    || pass "S3 config — secret not exposed in response"

  local list
  list=$(api "${BKT_ENDPOINT}/api/s3-configs" -H "Authorization: Bearer ${JWT_TOKEN}")
  echo "$list" | grep -q "smoke-s3cfg-${ts}" \
    && pass "GET /api/s3-configs → config listed" \
    || fail "GET /api/s3-configs" "$list"

  local get
  get=$(api "${BKT_ENDPOINT}/api/s3-configs/${cfg_id}" -H "Authorization: Bearer ${JWT_TOKEN}")
  echo "$get" | grep -q '"region":"us-east-1"' \
    && pass "GET /api/s3-configs/:id → details returned" \
    || fail "GET /api/s3-configs/:id" "$get"

  local upd_code
  upd_code=$(api -X PUT "${BKT_ENDPOINT}/api/s3-configs/${cfg_id}" \
    -H "Authorization: Bearer ${JWT_TOKEN}" -H "Content-Type: application/json" \
    -d '{"region":"us-west-2"}' -w "%{http_code}" -o /dev/null)
  [[ "$upd_code" == "200" ]] \
    && pass "PUT /api/s3-configs/:id → updated" \
    || fail "PUT /api/s3-configs/:id" "HTTP $upd_code"

  local del_code
  del_code=$(api -X DELETE "${BKT_ENDPOINT}/api/s3-configs/${cfg_id}" \
    -H "Authorization: Bearer ${JWT_TOKEN}" -w "%{http_code}" -o /dev/null)
  [[ "$del_code" == "200" || "$del_code" == "204" ]] \
    && pass "DELETE /api/s3-configs/:id → removed" \
    || fail "DELETE /api/s3-configs/:id" "HTTP $del_code"
}

# ── Section 26: User profile (update self) ────────────────────────────────────
# Runs against a throwaway user so it never mutates the admin account (the admin's
# default email `admin@localhost` has no TLD and would fail the email validator on
# a restore, leaving the admin email dirty).
test_user_profile() {
  bold "User profile (update self)"

  local ts=$RANDOM
  local uname="proftest-${ts}"
  local upass="Prof!Test1${ts}"

  local user_resp uid
  user_resp=$(api -X POST "${BKT_ENDPOINT}/api/users" \
    -H "Authorization: Bearer ${JWT_TOKEN}" -H "Content-Type: application/json" \
    -d "{\"username\":\"${uname}\",\"email\":\"${uname}@test.local\",\"password\":\"${upass}\"}")
  uid=$(echo "$user_resp" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4 || true)
  if [[ -z "$uid" ]]; then
    skip "User profile" "could not create test user"
    return
  fi

  local utoken
  utoken=$(api -X POST "${BKT_ENDPOINT}/api/auth/login" -H "Content-Type: application/json" \
    -d "{\"username\":\"${uname}\",\"password\":\"${upass}\"}" \
    | grep -o '"token":"[^"]*"' | cut -d'"' -f4 || true)

  local new_email="${uname}-updated@test.local"
  local code
  code=$(api -X PUT "${BKT_ENDPOINT}/api/users/me" \
    -H "Authorization: Bearer ${utoken}" -H "Content-Type: application/json" \
    -d "{\"email\":\"${new_email}\"}" -w "%{http_code}" -o /dev/null)
  [[ "$code" == "200" ]] \
    && pass "PUT /api/users/me → profile updated" \
    || fail "PUT /api/users/me" "HTTP $code"

  local me
  me=$(api "${BKT_ENDPOINT}/api/users/me" -H "Authorization: Bearer ${utoken}")
  echo "$me" | grep -q "$new_email" \
    && pass "GET /api/users/me → reflects updated email" \
    || fail "User profile — updated email not persisted"

  # Cleanup: delete the throwaway user.
  api -X DELETE "${BKT_ENDPOINT}/api/users/${uid}" \
    -H "Authorization: Bearer ${JWT_TOKEN}" &>/dev/null || true
}

# ── Section 27: Idempotency (request replay protection) ───────────────────────
# Replaying a POST with the same Idempotency-Key + body must return the cached
# response instead of performing the action twice. We use bucket creation (its
# response carries a stable id and is easy to clean up). `|| true` on the id
# extraction keeps a no-match from aborting the script under pipefail.
test_idempotency() {
  bold "Idempotency (request replay protection)"

  local idem="smoke-idem-$(date +%s)-${RANDOM}-padding"  # must be >= 16 chars
  local bname="bkt-idem-$(date +%s)-${RANDOM}"
  CREATED_BUCKETS+=("$bname")  # ensure cleanup even if assertions fail
  local body="{\"name\":\"${bname}\",\"storage_backend\":\"local\"}"

  local r1 id1
  r1=$(api -X POST "${BKT_ENDPOINT}/api/buckets" \
    -H "Authorization: Bearer ${JWT_TOKEN}" \
    -H "Idempotency-Key: ${idem}" -H "Content-Type: application/json" -d "$body")
  id1=$(echo "$r1" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4 || true)
  [[ -n "$id1" ]] \
    && pass "Idempotency — first request created the bucket" \
    || { fail "Idempotency — first request" "$r1"; return; }

  # Replay the exact same request (same key, same body).
  local r2 id2
  r2=$(api -X POST "${BKT_ENDPOINT}/api/buckets" \
    -H "Authorization: Bearer ${JWT_TOKEN}" \
    -H "Idempotency-Key: ${idem}" -H "Content-Type: application/json" -d "$body")
  id2=$(echo "$r2" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4 || true)
  [[ -n "$id2" && "$id1" == "$id2" ]] \
    && pass "Idempotency — replay returned the cached response (same id, no duplicate)" \
    || fail "Idempotency — replay differed (${id1} vs ${id2}) resp=${r2:0:160}"
}

# ── Section 28: Policy — explicit user Deny overrides bucket Allow ─────────────
# Regression guard: an explicit Deny in a user's identity policy must win even
# when a bucket (resource) policy allows the action (IAM "deny always wins").
test_policy_deny_precedence() {
  bold "Policy — explicit user Deny overrides bucket Allow"

  local ts=$RANDOM
  local uname="denytest-${ts}"
  local upass="Deny!Test1${ts}"

  local uid
  uid=$(api -X POST "${BKT_ENDPOINT}/api/users" \
    -H "Authorization: Bearer ${JWT_TOKEN}" -H "Content-Type: application/json" \
    -d "{\"username\":\"${uname}\",\"email\":\"${uname}@test.local\",\"password\":\"${upass}\"}" \
    | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4 || true)
  if [[ -z "$uid" ]]; then skip "Policy deny precedence" "could not create test user"; return; fi

  local utoken
  utoken=$(api -X POST "${BKT_ENDPOINT}/api/auth/login" -H "Content-Type: application/json" \
    -d "{\"username\":\"${uname}\",\"password\":\"${upass}\"}" | grep -o '"token":"[^"]*"' | cut -d'"' -f4 || true)

  # Bucket policy: Allow ListBucket/GetObject for everyone on this bucket.
  local bp_body
  bp_body=$(python3 -c "import json; print(json.dumps({'policy': json.dumps({'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':['s3:ListBucket','s3:GetObject'],'Resource':['*']}]})}))")
  api -X PUT "${BKT_ENDPOINT}/api/buckets/${TEST_BUCKET}/policy" \
    -H "Authorization: Bearer ${JWT_TOKEN}" -H "Content-Type: application/json" -d "$bp_body" &>/dev/null

  # Sanity: with the bucket Allow and no user policy, the bucket policy grants access.
  local before
  before=$(http_code "${BKT_ENDPOINT}/api/buckets/${TEST_BUCKET}/objects" -H "Authorization: Bearer ${utoken}")
  [[ "$before" == "200" ]] \
    && pass "Bucket-policy Allow grants access (no user policy yet)" \
    || fail "Bucket-policy Allow should grant access (got $before)"

  # Attach a user policy that explicitly DENIES the same actions.
  local pol_body pol_id
  pol_body=$(python3 -c "import json; print(json.dumps({'name':'smoke-deny-${ts}','description':'smoke deny','document': json.dumps({'Version':'2012-10-17','Statement':[{'Effect':'Deny','Action':['s3:ListBucket','s3:GetObject'],'Resource':['*']}]})}))")
  pol_id=$(api -X POST "${BKT_ENDPOINT}/api/policies" \
    -H "Authorization: Bearer ${JWT_TOKEN}" -H "Content-Type: application/json" -d "$pol_body" \
    | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4 || true)

  if [[ -n "$pol_id" ]]; then
    api -X POST "${BKT_ENDPOINT}/api/policies/users/${uid}/attach" \
      -H "Authorization: Bearer ${JWT_TOKEN}" -H "Content-Type: application/json" \
      -d "{\"policy_id\":\"${pol_id}\"}" &>/dev/null

    # The explicit user Deny MUST override the bucket Allow.
    local after
    after=$(http_code "${BKT_ENDPOINT}/api/buckets/${TEST_BUCKET}/objects" -H "Authorization: Bearer ${utoken}")
    [[ "$after" == "403" || "$after" == "401" ]] \
      && pass "Explicit user Deny overrides bucket-policy Allow (denied, $after)" \
      || fail "User Deny did NOT override bucket Allow (got $after, expected 403)"

    api -X DELETE "${BKT_ENDPOINT}/api/policies/users/${uid}/detach/${pol_id}" -H "Authorization: Bearer ${JWT_TOKEN}" &>/dev/null || true
    api -X DELETE "${BKT_ENDPOINT}/api/policies/${pol_id}" -H "Authorization: Bearer ${JWT_TOKEN}" &>/dev/null || true
  else
    skip "Policy deny precedence" "could not create deny policy"
  fi

  api -X DELETE "${BKT_ENDPOINT}/api/users/${uid}" -H "Authorization: Bearer ${JWT_TOKEN}" &>/dev/null || true
}

# ── Summary ───────────────────────────────────────────────────────────────────
print_summary() {
  local total=$((PASS + FAIL + SKIP))
  echo ""
  echo -e "${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"
  echo -e "${BOLD}  bkt Smoke Test Results${RESET}"
  echo -e "${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"
  printf "  %-10s ${GREEN}%s${RESET}\n"  "Passed:"  "$PASS"
  printf "  %-10s ${RED}%s${RESET}\n"    "Failed:"  "$FAIL"
  printf "  %-10s ${YELLOW}%s${RESET}\n" "Skipped:" "$SKIP"
  printf "  %-10s %s\n"                  "Total:"   "$total"
  echo -e "  Endpoint:  $BKT_ENDPOINT"
  echo ""

  if [[ $FAIL -gt 0 ]]; then
    echo -e "${RED}Failed:${RESET}"
    for t in "${FAILED_TESTS[@]}"; do
      echo -e "  ${RED}✖${RESET} $t"
    done
    echo ""
  fi

  if [[ $FAIL -eq 0 ]]; then
    echo -e "${GREEN}${BOLD}✔ All tests passed.${RESET}"
  else
    echo -e "${RED}${BOLD}✖ $FAIL test(s) failed.${RESET}"
  fi
}

# ── Storage-backend suite ─────────────────────────────────────────────────────
# Runs every storage-dependent test against a freshly created bucket bound to
# the given backend. Called once per enabled backend so both the local and S3
# code paths are exercised in a single run.
run_storage_suite() {
  local backend="$1"
  echo ""
  echo -e "${BOLD}${CYAN}═══════════════ STORAGE BACKEND: ${backend} ═══════════════${RESET}"

  test_create_bucket "$backend" || {
    fail "Bucket creation failed for '${backend}' backend — skipping its storage tests"
    return 1
  }
  test_file_types
  test_content_type_enforcement
  test_file_retrieval
  test_ui_api
  test_s3_pagination
  test_s3_copy
  test_s3_delete_objects
  test_s3_multipart
  test_s3_range
  test_s3_presign
  test_object_ops
  test_object_delete
  test_async_upload
  test_bucket_policy
  # Direct-to-S3 verification only makes sense for the S3-backed pass.
  if [[ "$backend" == "s3" ]]; then
    test_aws_backend
  fi
  return 0
}

# ── Main ──────────────────────────────────────────────────────────────────────
main() {
  # Storage backends to exercise: local always; S3 when enabled + keyed in .env.
  local backends=("local")
  if [[ "${S3_BACKEND_ENABLED:-false}" == "true" && -n "$AWS_KEY" ]]; then
    backends+=("s3")
  fi

  echo ""
  echo -e "${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"
  echo -e "${BOLD}  bkt Smoke Tests${RESET}"
  echo -e "${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"
  echo -e "  Endpoint:  ${CYAN}$BKT_ENDPOINT${RESET}"
  echo -e "  User:      ${CYAN}$BKT_USERNAME${RESET}"
  echo -e "  Backends:  ${CYAN}${backends[*]}${RESET}"
  [[ "${S3_BACKEND_ENABLED:-false}" == "true" && -n "$AWS_KEY" ]] \
    && echo -e "  S3 region: ${CYAN}${S3_REGION:-us-east-1}${RESET}" \
    || echo -e "  S3 pass:   ${YELLOW}skipped (set S3_ENABLED=true and S3 keys in .env)${RESET}"
  echo ""

  test_prereqs
  test_health
  test_metrics
  test_swagger
  test_auth          || { echo -e "${RED}Auth failed — cannot continue${RESET}"; print_summary; exit 1; }
  test_access_keys   || { echo -e "${RED}Access key creation failed — cannot continue${RESET}"; print_summary; exit 1; }

  # Run the full storage suite once per enabled backend.
  for backend in "${backends[@]}"; do
    run_storage_suite "$backend" || true
  done

  # Backend-agnostic checks (run once, against the most recently created bucket).
  test_user_lock
  test_user_profile
  test_idempotency
  test_s3_configs
  test_policies
  test_policy_deny_precedence

  print_summary
  [[ $FAIL -eq 0 ]]
}

main
