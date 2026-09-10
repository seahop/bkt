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

# ── 3a. Postgres major-version upgrade (existing data dir from an older PG) ──
# The image bundles the previous major's binaries (apk postgresql<N>) so a
# volume created by an older omnibus release is upgraded in place with
# pg_upgrade on first start. The old cluster is kept beside the new one as
# $PGDATA.pg<N> until the operator deletes it.
PG_NEW_MAJOR=$(postgres --version | sed -E 's/.* ([0-9]+)[.0-9]*.*/\1/')
if [ -s "$PGDATA/PG_VERSION" ]; then
  PG_OLD_MAJOR=$(cat "$PGDATA/PG_VERSION")
  if [ "$PG_OLD_MAJOR" != "$PG_NEW_MAJOR" ]; then
    OLD_BIN=/usr/libexec/postgresql${PG_OLD_MAJOR}
    if [ ! -x "$OLD_BIN/pg_ctl" ]; then
      log "ERROR: $PGDATA is PostgreSQL ${PG_OLD_MAJOR} but this image (PostgreSQL ${PG_NEW_MAJOR}) has no ${PG_OLD_MAJOR} binaries to upgrade from."
      log "       Dump with the previous image release and restore, or open an issue."
      exit 1
    fi
    log "Upgrading Postgres data directory: ${PG_OLD_MAJOR} -> ${PG_NEW_MAJOR} (pg_upgrade)"
    NEW_DATA="$PGDATA.new"
    OLD_KEEP="$PGDATA.pg${PG_OLD_MAJOR}"
    UPG_LOG="$DATA_DIR/pg_upgrade_logs"
    rm -rf "$NEW_DATA" "$UPG_LOG"
    install -d -o postgres -g postgres -m 700 "$NEW_DATA" "$UPG_LOG"

    # pg_upgrade refuses a cluster that was not shut down cleanly (e.g. the
    # container was killed). Start/stop it once with the old binaries.
    su-exec postgres "$OLD_BIN/pg_ctl" -D "$PGDATA" \
      -o "-c listen_addresses='' -c unix_socket_directories='$UPG_LOG'" -w start >/dev/null
    su-exec postgres "$OLD_BIN/pg_ctl" -D "$PGDATA" -m fast -w stop >/dev/null

    # New cluster must match the old one's checksum setting (PG18 initdb turns
    # checksums on by default; older releases did not).
    CHECKSUM_FLAG=--no-data-checksums
    if "$OLD_BIN/pg_controldata" "$PGDATA" | grep -qE 'Data page checksum version:\s+[1-9]'; then
      CHECKSUM_FLAG=--data-checksums
    fi
    su-exec postgres initdb -D "$NEW_DATA" \
      --username=postgres --auth-local=trust --auth-host=scram-sha-256 \
      --encoding=UTF8 $CHECKSUM_FLAG >/dev/null

    if ! (cd "$UPG_LOG" && su-exec postgres pg_upgrade \
          -b "$OLD_BIN" -B /usr/local/bin -d "$PGDATA" -D "$NEW_DATA" \
          --username=postgres >"$UPG_LOG/pg_upgrade.out" 2>&1); then
      log "ERROR: pg_upgrade failed; the original ${PG_OLD_MAJOR} cluster at $PGDATA is untouched."
      log "       See $UPG_LOG/pg_upgrade.out"
      tail -20 "$UPG_LOG/pg_upgrade.out" || true
      exit 1
    fi
    rm -rf "$OLD_KEEP"
    mv "$PGDATA" "$OLD_KEEP"
    mv "$NEW_DATA" "$PGDATA"
    PG_UPGRADED=1
    log "Postgres upgraded to ${PG_NEW_MAJOR}. Previous cluster kept at $OLD_KEEP — delete it once satisfied."
  fi
fi

# ── 3b. Postgres: init on first boot, then start on loopback ─────────────────
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
if [ "${PG_UPGRADED:-0}" = "1" ]; then
  # pg_upgrade does not carry over all planner statistics; rebuild them.
  su-exec postgres vacuumdb -h 127.0.0.1 --username postgres --all --analyze-in-stages >/dev/null 2>&1 || true
fi

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
