#!/usr/bin/env bash
# End-to-end OIDC login test: real Keycloak + real browser.
#
# Boots Keycloak (realm imported from bkt-realm.json: PKCE-enforced confidential
# client "bkt", a groups mapper, users alice/bob/carol), starts the bkt omnibus
# image configured against it, and drives the login through Chromium
# (Playwright) asserting PKCE on the wire, admin/user/denied outcomes, subject
# matching on repeat login, and audit entries.
#
#   ./tests/oidc-e2e/run.sh                 # builds the omnibus image from the working tree
#   BKT_IMAGE=ghcr.io/seahop/bkt:1.4.0 ./tests/oidc-e2e/run.sh   # test a published image
#
# Requires Docker. Takes ~3 minutes; everything runs on a private network.
set -euo pipefail
cd "$(dirname "$0")/../.."

NET=bkt-oidc-e2e
KC_IMAGE=${KC_IMAGE:-quay.io/keycloak/keycloak:26.3}
PW_IMAGE=${PW_IMAGE:-mcr.microsoft.com/playwright:v1.58.2-noble}
BKT_IMAGE=${BKT_IMAGE:-bkt:oidc-e2e}
# Work dir lives inside the repo: on VM-backed Docker (Rancher Desktop, Docker
# Desktop) /tmp is not shared with the daemon and a bind mount there is empty.
WORK="$PWD/tests/oidc-e2e/.work"; rm -rf "$WORK"; mkdir -p "$WORK"

cleanup() { docker rm -f oidc-e2e-keycloak oidc-e2e-bkt >/dev/null 2>&1 || true; docker network rm $NET >/dev/null 2>&1 || true; }
[[ "${NOCLEANUP:-0}" == "1" ]] || trap cleanup EXIT

if [[ "$BKT_IMAGE" == "bkt:oidc-e2e" ]]; then
  echo "▸ Building omnibus image from working tree"
  docker build -q --target omnibus -t "$BKT_IMAGE" . >/dev/null
fi

docker network create $NET >/dev/null
echo "▸ Starting Keycloak ($KC_IMAGE)"
docker run -d --name oidc-e2e-keycloak --network $NET \
  -e KC_BOOTSTRAP_ADMIN_USERNAME=admin -e KC_BOOTSTRAP_ADMIN_PASSWORD=admin \
  -e KC_HTTP_ENABLED=true -e KC_HOSTNAME_STRICT=false \
  -v "$PWD/tests/oidc-e2e":/opt/keycloak/data/import:ro "$KC_IMAGE" start-dev --import-realm >/dev/null
for i in $(seq 1 120); do
  docker run --rm --network $NET alpine wget -q -O- http://oidc-e2e-keycloak:8080/realms/bkt/.well-known/openid-configuration 2>/dev/null | grep -q '"issuer"' && break
  sleep 2
done

echo "▸ Starting bkt ($BKT_IMAGE) with OIDC_* pointed at Keycloak"
docker run -d --name oidc-e2e-bkt --network $NET -e ADMIN_PASSWORD=E2E-Admin-Pass-1 \
  -e OIDC_ISSUER_URL=http://oidc-e2e-keycloak:8080/realms/bkt -e OIDC_CLIENT_ID=bkt \
  -e OIDC_CLIENT_SECRET=bkt-client-secret-for-tests \
  -e OIDC_REDIRECT_URL=https://oidc-e2e-bkt:9443/api/auth/oidc/callback -e FRONTEND_URL=https://oidc-e2e-bkt:9443 \
  -e OIDC_PROVIDER_NAME=Keycloak -e OIDC_ADMIN_GROUP=bkt-admins -e OIDC_USER_GROUP=bkt-users \
  "$BKT_IMAGE" >/dev/null
for i in $(seq 1 90); do
  docker exec oidc-e2e-bkt wget -q --no-check-certificate -O /dev/null https://localhost:9443/health 2>/dev/null && break
  sleep 1
done

echo "▸ Driving the browser (login, roles, denial, audit)"
cp tests/oidc-e2e/oidc-e2e.js tests/oidc-e2e/iam-e2e.sh "$WORK/"
docker run --rm --network $NET -v "$WORK":/work -w /work "$PW_IMAGE" bash -c \
  'npm init -y >/dev/null 2>&1; npm install --no-audit --no-fund playwright@1.58.2 >/dev/null 2>&1; node oidc-e2e.js https://oidc-e2e-bkt:9443 shots'

echo "▸ IAM parity: policies, groups and access keys for the OIDC user vs a local user"
docker build -q -t bkt-tests:oidc-e2e tests/ >/dev/null
docker run --rm --network $NET -v "$WORK":/work --entrypoint bash bkt-tests:oidc-e2e \
  /work/iam-e2e.sh https://oidc-e2e-bkt:9443 https://oidc-e2e-bkt:9000 E2E-Admin-Pass-1 /work

echo "▸ Re-login: assigned policy survives a fresh SSO login"
docker run --rm --network $NET -v "$WORK":/work -w /work "$PW_IMAGE" node oidc-e2e.js https://oidc-e2e-bkt:9443 shots --relogin-check
echo "Screenshots: $WORK/shots"
