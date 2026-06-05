#!/usr/bin/env bash
# =============================================================================
# bkt omnibus entrypoint
#
# Runs PostgreSQL and the bkt backend (UI + REST + S3 API) in a single
# container. On first boot it provisions everything into the /data volume:
#   - generates & persists secrets (DB_PASSWORD, JWT_SECRET, ENCRYPTION_KEY,
#     ADMIN_PASSWORD)         -> /data/secrets.env   (stable across restarts)
#   - generates a self-signed TLS cert (unless TLS disabled / certs provided)
#   - initialises Postgres    -> /data/pgdata
# Then it starts Postgres on loopback, waits until ready, and runs the backend.
# =============================================================================
set -euo pipefail

DATA_DIR=/data
PGDATA=${PGDATA:-$DATA_DIR/pgdata}
SECRETS_FILE=$DATA_DIR/secrets.env
CERT_DIR=$DATA_DIR/certs
STORAGE_ROOT=${STORAGE_ROOT:-$DATA_DIR/buckets}

log() { echo "[entrypoint] $*"; }

mkdir -p "$DATA_DIR"

# ── 1. Secrets: generate once, then the file is authoritative ────────────────
ADMIN_GENERATED=0
if [ ! -f "$SECRETS_FILE" ]; then
  log "First boot — generating secrets into $SECRETS_FILE"
  umask 077
  : > "$SECRETS_FILE"
  echo "DB_PASSWORD=${DB_PASSWORD:-$(openssl rand -hex 24)}"     >> "$SECRETS_FILE"
  echo "JWT_SECRET=${JWT_SECRET:-$(openssl rand -hex 32)}"       >> "$SECRETS_FILE"
  echo "ENCRYPTION_KEY=${ENCRYPTION_KEY:-$(openssl rand -hex 32)}" >> "$SECRETS_FILE"
  if [ -z "${ADMIN_PASSWORD:-}" ]; then
    ADMIN_PASSWORD="$(openssl rand -hex 12)"
    ADMIN_GENERATED=1
  fi
  echo "ADMIN_PASSWORD=${ADMIN_PASSWORD}"                        >> "$SECRETS_FILE"
fi
# shellcheck disable=SC1090
set -a; . "$SECRETS_FILE"; set +a

# ── 2. TLS: auto self-signed unless disabled or certs supplied ───────────────
if [ "${TLS_ENABLED:-true}" != "false" ]; then
  export TLS_ENABLED=true
  export TLS_CERT_FILE=${TLS_CERT_FILE:-$CERT_DIR/tls.crt}
  export TLS_KEY_FILE=${TLS_KEY_FILE:-$CERT_DIR/tls.key}
  if [ ! -f "$TLS_CERT_FILE" ] || [ ! -f "$TLS_KEY_FILE" ]; then
    log "Generating self-signed TLS certificate"
    mkdir -p "$CERT_DIR"
    openssl req -x509 -newkey rsa:2048 -nodes \
      -keyout "$TLS_KEY_FILE" -out "$TLS_CERT_FILE" -days 3650 \
      -subj "/CN=bkt" \
      -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" >/dev/null 2>&1
    chmod 600 "$TLS_KEY_FILE"
  fi
  # TLS is on, so production mode is safe by default.
  export GO_ENV=${GO_ENV:-production}
else
  log "TLS disabled — serving plain HTTP"
  export GO_ENV=${GO_ENV:-development}
fi

# ── 3. Postgres: init on first boot, then start on loopback ──────────────────
if [ ! -s "$PGDATA/PG_VERSION" ]; then
  log "Initialising Postgres data directory at $PGDATA"
  install -d -o postgres -g postgres -m 700 "$PGDATA"
  su-exec postgres initdb -D "$PGDATA" \
    --username=postgres --auth-local=trust --auth-host=scram-sha-256 \
    --encoding=UTF8 >/dev/null

  # Bring it up briefly on the unix socket to create the app role + database.
  su-exec postgres pg_ctl -D "$PGDATA" \
    -o "-c listen_addresses='' -p 5432" -w start >/dev/null
  su-exec postgres psql -v ON_ERROR_STOP=1 --username postgres --no-psqlrc <<SQL >/dev/null
    CREATE ROLE objectstore WITH LOGIN PASSWORD '${DB_PASSWORD}';
    CREATE DATABASE objectstore OWNER objectstore;
SQL
  su-exec postgres pg_ctl -D "$PGDATA" -m fast -w stop >/dev/null
  log "Postgres initialised (role/db: objectstore)"
fi

log "Starting Postgres on 127.0.0.1:5432"
su-exec postgres postgres -D "$PGDATA" -c listen_addresses='127.0.0.1' &
PG_PID=$!

until su-exec postgres pg_isready -h 127.0.0.1 -q 2>/dev/null; do
  sleep 1
done
log "Postgres ready"

# ── 4. Backend env (loopback DB, single data volume) ─────────────────────────
export DB_HOST=127.0.0.1 DB_PORT=5432 DB_USER=objectstore DB_NAME=objectstore DB_SSL_MODE=disable
export STORAGE_BACKEND=${STORAGE_BACKEND:-local}
export STORAGE_ROOT
export CONSOLE_PORT=${CONSOLE_PORT:-9443}
export S3_API_PORT=${S3_API_PORT:-9000}
export ADMIN_USERNAME=${ADMIN_USERNAME:-admin}
export AUTH_RATE_LIMIT=${AUTH_RATE_LIMIT:-60}
mkdir -p "$STORAGE_ROOT"

if [ "$ADMIN_GENERATED" = "1" ]; then
  echo "============================================================"
  echo "  bkt first-boot admin credentials"
  echo "    username: ${ADMIN_USERNAME}"
  echo "    password: ${ADMIN_PASSWORD}"
  echo "  (stored in ${SECRETS_FILE}; set ADMIN_PASSWORD to override)"
  echo "============================================================"
fi

# ── 5. Run backend; shut both down together ──────────────────────────────────
log "Starting bkt backend (console:${CONSOLE_PORT} s3:${S3_API_PORT})"
bkt &
APP_PID=$!

shutdown() {
  log "Shutting down…"
  kill -TERM "$APP_PID" 2>/dev/null || true
  wait "$APP_PID" 2>/dev/null || true
  su-exec postgres pg_ctl -D "$PGDATA" -m fast -w stop >/dev/null 2>&1 || true
  exit 0
}
trap shutdown TERM INT

# Exit (and tear down) as soon as either process stops.
wait -n "$APP_PID" "$PG_PID"
shutdown
