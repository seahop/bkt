#!/usr/bin/env bash
# IdP group → bkt group mapping, end to end. Runs inside the tests/ image
# (curl + python3). Two stages around bob's SSO re-login:
#
#   setup   admin creates bucket "sso-eng", a policy for it, and
#           - group e2e-sso-eng linked to "eng-team" (Keycloak group "Eng-Team":
#             matching is case-insensitive) with that policy attached,
#           - group e2e-sso-ops linked to "ops-team" (bob is not in it) and adds
#             bob to it by hand (must be removed at his next sign-in),
#           - group e2e-manual without links, with bob added by hand (must be kept),
#           - local user dave added by hand to e2e-sso-eng (never touched by SSO).
#   verify  after bob signed in again: memberships as expected, and the
#           auth.login audit entry records sso_groups_added/removed.
#
# Bob's bucket access through the synced group is asserted by oidc-e2e.js
# (--relogin-check) between the two stages.
set -uo pipefail
STAGE=$1; BASE=$2; ADMIN_PASS=$3; WORK=$4
fails=0; pass() { echo "PASS $*"; }; fail() { echo "FAIL $*"; fails=$((fails+1)); }
api() { curl -sk --max-time 20 "$@"; }
jget() { python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('$1','') if isinstance(d,dict) else '')" 2>/dev/null; }

ADMIN=$(api -X POST "$BASE/api/auth/login" -H "Content-Type: application/json" -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PASS\"}" | jget token)
[[ -n "$ADMIN" ]] || { echo "admin login failed"; exit 1; }
A=(-H "Authorization: Bearer $ADMIN" -H "Content-Type: application/json")
BOB_ID=$(jget id < "$WORK/shots/bob.json")
user_id() { api "$BASE/api/users" "${A[@]}" | python3 -c "import sys,json; print(next((u['id'] for u in json.load(sys.stdin) if u['username']=='$1'), ''))"; }
DAVE_ID=$(user_id dave)
[[ -n "$BOB_ID" && -n "$DAVE_ID" ]] || { echo "bob/dave missing (run the IAM stage first)"; exit 1; }

# group_field NAME EXPR: evaluate EXPR (python, variable g = the group) for group NAME.
group_field() { api "$BASE/api/groups" "${A[@]}" | python3 -c "
import sys, json
g = next((g for g in json.load(sys.stdin) if g['name'] == '$1'), None)
ids = [u['id'] for u in (g or {}).get('users') or []]
print($2)"; }

case "$STAGE" in
setup)
  api -X POST "$BASE/api/buckets" "${A[@]}" -d '{"name":"sso-eng","storage_backend":"local"}' >/dev/null
  DOC='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:ListBucket","s3:GetObject","s3:PutObject"],"Resource":["arn:aws:s3:::sso-eng","arn:aws:s3:::sso-eng/*"]}]}'
  POL=$(python3 -c "import json; print(json.dumps({'name':'e2e-sso-eng','description':'e2e','document':'$DOC'}))" | api -X POST "$BASE/api/policies" "${A[@]}" -d @- | jget id)
  [[ -n "$POL" ]] || { echo "policy create failed"; exit 1; }
  ENG=$(api -X POST "$BASE/api/groups" "${A[@]}" -d '{"name":"e2e-sso-eng","sso_groups":["eng-team"]}' | jget id)
  OPS=$(api -X POST "$BASE/api/groups" "${A[@]}" -d '{"name":"e2e-sso-ops","sso_groups":["ops-team"]}' | jget id)
  MAN=$(api -X POST "$BASE/api/groups" "${A[@]}" -d '{"name":"e2e-manual"}' | jget id)
  [[ -n "$ENG" && -n "$OPS" && -n "$MAN" ]] || { echo "group create failed"; exit 1; }
  api -X POST "$BASE/api/groups/$ENG/policies" "${A[@]}" -d "{\"policy_id\":\"$POL\"}" >/dev/null
  api -X POST "$BASE/api/groups/$OPS/members" "${A[@]}" -d "{\"user_id\":\"$BOB_ID\"}" >/dev/null
  api -X POST "$BASE/api/groups/$MAN/members" "${A[@]}" -d "{\"user_id\":\"$BOB_ID\"}" >/dev/null
  api -X POST "$BASE/api/groups/$ENG/members" "${A[@]}" -d "{\"user_id\":\"$DAVE_ID\"}" >/dev/null
  [[ "$(group_field e2e-sso-eng "','.join(g['sso_groups'])")" == "eng-team" ]] && pass "setup: group JSON carries sso_groups" || fail "setup: sso_groups missing from group JSON"
  [[ "$(group_field e2e-sso-eng "'$BOB_ID' in ids")" == "False" ]] && pass "setup: bob not yet in e2e-sso-eng (sync happens at sign-in)" || fail "setup: bob already in e2e-sso-eng"
  ;;
verify)
  [[ "$(group_field e2e-sso-eng "'$BOB_ID' in ids")" == "True" ]] && pass "bob added to e2e-sso-eng via Keycloak group Eng-Team (case-insensitive link eng-team)" || fail "bob not synced into e2e-sso-eng"
  [[ "$(group_field e2e-sso-ops "'$BOB_ID' in ids")" == "False" ]] && pass "bob's manual membership in linked e2e-sso-ops replaced at sign-in" || fail "bob still in e2e-sso-ops"
  [[ "$(group_field e2e-manual "'$BOB_ID' in ids")" == "True" ]] && pass "bob's membership in unlinked e2e-manual untouched" || fail "bob lost e2e-manual membership"
  [[ "$(group_field e2e-sso-eng "'$DAVE_ID' in ids")" == "True" ]] && pass "local user dave's manual membership in a linked group untouched" || fail "dave removed from e2e-sso-eng"
  AUDIT=$(api "$BASE/api/audit?limit=100" "${A[@]}" | python3 -c "
import sys, json
logs = json.load(sys.stdin).get('logs') or []
e = next((l for l in logs if l['action'] == 'auth.login' and l['username'] == 'bob' and l['status'] == 'success'), None)
m = e.get('metadata') if e else None
m = json.loads(m) if isinstance(m, str) else (m or {})
print(','.join(m.get('sso_groups_added') or []) + '|' + ','.join(m.get('sso_groups_removed') or []))")
  [[ "$AUDIT" == "e2e-sso-eng|e2e-sso-ops" ]] && pass "audit auth.login: sso_groups_added=e2e-sso-eng sso_groups_removed=e2e-sso-ops" || fail "audit sso_groups metadata: [$AUDIT]"
  ;;
*) echo "usage: $0 setup|verify BASE ADMIN_PASS WORK"; exit 2 ;;
esac
echo; [[ $fails -eq 0 ]] && echo "SSO GROUPS E2E ($STAGE) OK" || { echo "SSO GROUPS E2E ($STAGE) FAILED ($fails)"; exit 1; }
