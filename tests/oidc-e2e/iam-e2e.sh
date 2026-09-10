#!/usr/bin/env bash
# IAM parity check for an SSO-provisioned user. Runs inside the tests/ image
# (curl + aws CLI + python3). Expects $WORK/shots/bob.json from oidc-e2e.js.
#
# Proves that a user who exists only because they signed in through OIDC gets
# policies, group membership and access keys exactly like a local user: the
# same steps are run for "bob" (OIDC) and a freshly created local user "dave",
# and the outcomes must be identical.
set -uo pipefail
BASE=$1; S3=$2; ADMIN_PASS=$3; WORK=$4
fails=0; pass() { echo "PASS $*"; }; fail() { echo "FAIL $*"; fails=$((fails+1)); }
api() { curl -sk --max-time 20 "$@"; }
code() { curl -sk --max-time 20 -o /dev/null -w "%{http_code}" "$@"; }
jget() { python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('$1','') if isinstance(d,dict) else '')" 2>/dev/null; }

ADMIN_RESP=$(api -X POST "$BASE/api/auth/login" -H "Content-Type: application/json" -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PASS\"}")
ADMIN=$(echo "$ADMIN_RESP" | jget token)
[[ -n "$ADMIN" ]] || { echo "admin login failed: [$ADMIN_RESP]"; echo "curl probe: $(code "$BASE/health")"; exit 1; }
A=(-H "Authorization: Bearer $ADMIN" -H "Content-Type: application/json")

BOB_TOKEN=$(jget token < "$WORK/shots/bob.json"); BOB_ID=$(jget id < "$WORK/shots/bob.json")
[[ -n "$BOB_TOKEN" && -n "$BOB_ID" ]] || { echo "bob session missing"; exit 1; }

# Local comparison user
DAVE_ID=$(api -X POST "$BASE/api/users" "${A[@]}" -d '{"username":"dave","email":"dave@example.com","password":"dave-pass-12345"}' | jget id)
[[ -n "$DAVE_ID" ]] || DAVE_ID=$(api "$BASE/api/users" "${A[@]}" | python3 -c "import sys,json; print([u['id'] for u in json.load(sys.stdin) if u['username']=='dave'][0])")
DAVE_TOKEN=$(api -X POST "$BASE/api/auth/login" -H "Content-Type: application/json" -d '{"username":"dave","password":"dave-pass-12345"}' | jget token)

# Buckets (admin-owned, local backend) and a bucket-scoped policy
for b in iam-allowed iam-forbidden; do api -X POST "$BASE/api/buckets" "${A[@]}" -d "{\"name\":\"$b\",\"storage_backend\":\"local\"}" >/dev/null; done
POL_DOC='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:*"],"Resource":["arn:aws:s3:::iam-allowed","arn:aws:s3:::iam-allowed/*"]}]}'
POL_ID=$(python3 -c "import json; print(json.dumps({'name':'e2e-allowed-bucket','description':'e2e','document': json.dumps(json.loads('$POL_DOC'))}))" | api -X POST "$BASE/api/policies" "${A[@]}" -d @- | jget id)
[[ -n "$POL_ID" ]] || { echo "policy create failed"; exit 1; }
GRP_DOC='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:PutObject","s3:ListBucket"],"Resource":["arn:aws:s3:::iam-forbidden","arn:aws:s3:::iam-forbidden/*"]}]}'
GRP_POL_ID=$(python3 -c "import json; print(json.dumps({'name':'e2e-group-bucket','description':'e2e','document': json.dumps(json.loads('$GRP_DOC'))}))" | api -X POST "$BASE/api/policies" "${A[@]}" -d @- | jget id)
GRP_ID=$(api -X POST "$BASE/api/groups" "${A[@]}" -d '{"name":"e2e-writers","description":"e2e"}' | jget id)
api -X POST "$BASE/api/groups/$GRP_ID/policies" "${A[@]}" -d "{\"policy_id\":\"$GRP_POL_ID\"}" >/dev/null

echo "hello from iam e2e" > /tmp/obj.txt

run_user() {  # name id token
  local name=$1 id=$2 token=$3 U=(-H "Authorization: Bearer $3")
  # Before any policy: nothing on either bucket
  [[ "$(code "$BASE/api/buckets/iam-allowed/objects" "${U[@]}")" == "403" ]] && pass "$name: denied on iam-allowed before policy" || fail "$name: expected 403 before policy"
  # Admin attaches the bucket policy directly to the user
  api -X POST "$BASE/api/policies/users/$id/attach" "${A[@]}" -d "{\"policy_id\":\"$POL_ID\"}" >/dev/null
  # REST API as the user
  [[ "$(code "$BASE/api/buckets/iam-allowed/objects" "${U[@]}")" == "200" ]] && pass "$name: REST list iam-allowed → 200" || fail "$name: REST list iam-allowed"
  local up; up=$(code -X POST "$BASE/api/buckets/iam-allowed/objects" "${U[@]}" -F "file=@/tmp/obj.txt" -F "key=rest/$name.txt")
  [[ "$up" == "200" || "$up" == "201" ]] && pass "$name: REST upload to iam-allowed → $up" || fail "$name: REST upload to iam-allowed → $up"
  [[ "$(code "$BASE/api/buckets/iam-forbidden/objects" "${U[@]}")" == "403" ]] && pass "$name: REST list iam-forbidden → 403" || fail "$name: REST list iam-forbidden not denied"
  # Access key → S3 API with SigV4 (same policies apply)
  local keys ak sk
  keys=$(api -X POST "$BASE/api/access-keys" "${U[@]}" -H "Content-Type: application/json" -d "{\"name\":\"e2e-$name\"}")
  ak=$(echo "$keys" | jget access_key); sk=$(echo "$keys" | jget secret_key)
  [[ -n "$ak" && -n "$sk" ]] && pass "$name: access key created" || { fail "$name: access key creation: $keys"; return; }
  aws configure set aws_access_key_id "$ak" --profile "$name"; aws configure set aws_secret_access_key "$sk" --profile "$name"
  aws configure set region us-east-1 --profile "$name"; aws configure set s3.addressing_style path --profile "$name"; aws configure set s3.signature_version s3v4 --profile "$name"
  local s3=(--endpoint-url "$S3" --no-verify-ssl --profile "$name")
  aws s3 cp /tmp/obj.txt "s3://iam-allowed/s3/$name.txt" "${s3[@]}" >/dev/null 2>&1 && pass "$name: S3 PutObject iam-allowed" || fail "$name: S3 PutObject iam-allowed"
  aws s3 cp "s3://iam-allowed/s3/$name.txt" - "${s3[@]}" 2>/dev/null | grep -q "hello from iam e2e" && pass "$name: S3 GetObject iam-allowed content matches" || fail "$name: S3 GetObject iam-allowed"
  aws s3 ls "s3://iam-allowed/" "${s3[@]}" 2>/dev/null | grep -q "PRE\|s3/" && pass "$name: S3 ListObjects iam-allowed" || fail "$name: S3 ListObjects iam-allowed"
  local out; out=$(aws s3 cp /tmp/obj.txt "s3://iam-forbidden/$name.txt" "${s3[@]}" 2>&1 || true)
  echo "$out" | grep -qi "AccessDenied\|403" && pass "$name: S3 PutObject iam-forbidden → AccessDenied" || fail "$name: S3 PutObject iam-forbidden was not denied [$out]"
  # Group membership: effective policies = direct ∪ group
  api -X POST "$BASE/api/groups/$GRP_ID/members" "${A[@]}" -d "{\"user_id\":\"$id\"}" >/dev/null
  aws s3 cp /tmp/obj.txt "s3://iam-forbidden/$name.txt" "${s3[@]}" >/dev/null 2>&1 && pass "$name: S3 PutObject iam-forbidden allowed via group policy" || fail "$name: group policy not applied"
  out=$(aws s3 cp "s3://iam-forbidden/$name.txt" - "${s3[@]}" 2>&1 || true)
  echo "$out" | grep -qi "AccessDenied\|403" && pass "$name: S3 GetObject iam-forbidden still denied (group grants Put/List only)" || fail "$name: GetObject on iam-forbidden should be denied [$out]"
  api -X DELETE "$BASE/api/groups/$GRP_ID/members/$id" "${A[@]}" >/dev/null
  out=$(aws s3 cp /tmp/obj.txt "s3://iam-forbidden/$name-2.txt" "${s3[@]}" 2>&1 || true)
  echo "$out" | grep -qi "AccessDenied\|403" && pass "$name: denied again after leaving the group" || fail "$name: still allowed after leaving group [$out]"
}

echo "── OIDC user (bob)"; run_user bob "$BOB_ID" "$BOB_TOKEN"
echo "── local user (dave)"; run_user dave "$DAVE_ID" "$DAVE_TOKEN"
echo; [[ $fails -eq 0 ]] && echo "IAM E2E OK" || { echo "IAM E2E FAILED ($fails)"; exit 1; }
