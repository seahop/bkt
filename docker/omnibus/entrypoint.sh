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
# Values are written so they are read back LITERALLY: generated hex is written
# bare (KEY=value, same as older releases), anything else single-quoted with
# embedded quotes escaped ('\''). The file is parsed below, never sourced, so a
# value containing spaces, $, backticks, quotes or # can't break or execute.
SECRET_KEYS="DB_PASSWORD JWT_SECRET ENCRYPTION_KEY ADMIN_PASSWORD"

write_secret() { # KEY VALUE -> appends one line to $SECRETS_FILE
  local key=$1 val=$2
  if [[ $val =~ ^[A-Za-z0-9._+/=:@%,-]+$ ]]; then
    printf '%s=%s\n' "$key" "$val" >> "$SECRETS_FILE"
  else
    printf "%s='%s'\n" "$key" "${val//\'/\'\\\'\'}" >> "$SECRETS_FILE"
  fi
}

load_secrets() { # parse KEY=VALUE / KEY='VALUE' lines; values taken literally
  local line key val sq="'" esc="'\\''"
  while IFS= read -r line || [ -n "$line" ]; do
    line=${line%$'\r'}
    case "$line" in ''|'#'*) continue ;; esac
    [[ $line == *=* ]] || continue
    key=${line%%=*}
    val=${line#*=}
    # Any valid variable name (operators may have added their own settings,
    # which older releases exported by sourcing the file).
    [[ $key =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || continue
    if [[ ${#val} -ge 2 && $val == "$sq"*"$sq" ]]; then
      val=${val:1:${#val}-2}
      val=${val//"$esc"/$sq}
    elif [[ ${#val} -ge 2 && $val == \"*\" ]]; then
      val=${val:1:${#val}-2}
    fi
    export "$key=$val"
  done < "$SECRETS_FILE"
}

ADMIN_GENERATED=0
if [ ! -f "$SECRETS_FILE" ]; then
  log "First boot — generating secrets into $SECRETS_FILE"
  (umask 077 && : > "$SECRETS_FILE")
  write_secret DB_PASSWORD    "${DB_PASSWORD:-$(openssl rand -hex 24)}"
  write_secret JWT_SECRET     "${JWT_SECRET:-$(openssl rand -hex 32)}"
  write_secret ENCRYPTION_KEY "${ENCRYPTION_KEY:-$(openssl rand -hex 32)}"
  if [ -z "${ADMIN_PASSWORD:-}" ]; then
    ADMIN_PASSWORD="$(openssl rand -hex 12)"
    ADMIN_GENERATED=1
  fi
  write_secret ADMIN_PASSWORD "$ADMIN_PASSWORD"
fi
chown root:root "$SECRETS_FILE"
chmod 600 "$SECRETS_FILE"
# Volumes from older releases may predate a key: add what's missing (never
# rotate what exists). A new ENCRYPTION_KEY is safe here — credentials stored
# under the old JWT_SECRET fallback stay decryptable — and DB_PASSWORD is
# re-applied to the Postgres role on every boot (step 3c).
for k in $SECRET_KEYS; do
  if ! grep -qE "^${k}=.+" "$SECRETS_FILE"; then
    log "Adding missing $k to $SECRETS_FILE"
    case "$k" in
      DB_PASSWORD) write_secret "$k" "${DB_PASSWORD:-$(openssl rand -hex 24)}" ;;
      ADMIN_PASSWORD) write_secret "$k" "${ADMIN_PASSWORD:-$(openssl rand -hex 12)}" ;;
      *) write_secret "$k" "${!k:-$(openssl rand -hex 32)}" ;;
    esac
  fi
done
load_secrets

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
fi

log "Starting Postgres on 127.0.0.1:5432"
su-exec postgres postgres -D "$PGDATA" -c listen_addresses='127.0.0.1' &
PG_PID=$!

until su-exec postgres pg_isready -h 127.0.0.1 -q 2>/dev/null; do
  if ! kill -0 "$PG_PID" 2>/dev/null; then
    log "ERROR: Postgres exited during startup"
    exit 1
  fi
  sleep 1
done
log "Postgres ready"
if [ "${PG_UPGRADED:-0}" = "1" ]; then
  # pg_upgrade does not carry over all planner statistics; rebuild them.
  # Unix socket (auth-local=trust); -w never prompts for a password.
  su-exec postgres vacuumdb -w --username postgres --all --analyze-in-stages >/dev/null 2>&1 || true
fi

# ── 3c. App role + database: create if missing, sync the password ───────────
# Idempotent on every boot, so an interrupted first boot heals itself and the
# role always matches DB_PASSWORD in the secrets file. The password travels via
# the environment (\getenv) and psql's :'var' quoting — never interpolated
# into SQL text or placed on a command line — so any character is safe.
su-exec postgres psql -v ON_ERROR_STOP=1 --username postgres --dbname postgres \
  --no-psqlrc --quiet >/dev/null <<'SQL'
\getenv pw DB_PASSWORD
SELECT 'CREATE ROLE objectstore WITH LOGIN'
  WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'objectstore')\gexec
ALTER ROLE objectstore WITH LOGIN PASSWORD :'pw';
SELECT 'CREATE DATABASE objectstore OWNER objectstore'
  WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'objectstore')\gexec
SQL
log "Postgres role/db ready (objectstore)"

# ── 4. Backend env (loopback DB, single data volume) ─────────────────────────
export DB_HOST=127.0.0.1 DB_PORT=5432 DB_USER=objectstore DB_NAME=objectstore DB_SSL_MODE=disable
export STORAGE_BACKEND=${STORAGE_BACKEND:-local}
export STORAGE_ROOT
export CONSOLE_PORT=${CONSOLE_PORT:-9443}
export S3_API_PORT=${S3_API_PORT:-9000}
export ADMIN_USERNAME=${ADMIN_USERNAME:-admin}
export AUTH_RATE_LIMIT=${AUTH_RATE_LIMIT:-60}
# SSO flows land back on the console itself (the UI is embedded there). Set
# FRONTEND_URL to your public console URL when published under another host/port.
if [ "${TLS_ENABLED:-false}" = "true" ]; then
  export FRONTEND_URL=${FRONTEND_URL:-https://localhost:${CONSOLE_PORT}}
else
  export FRONTEND_URL=${FRONTEND_URL:-http://localhost:${CONSOLE_PORT}}
fi

# ── 4b. Run the backend unprivileged ─────────────────────────────────────────
# bkt runs as the `bkt` user (uid 10001). It gets its secrets via the
# environment (secrets.env stays root-only) and needs write access only to the
# local bucket store and /tmp. Volumes from older releases (root-owned) are
# re-owned once; the check keeps later boots from walking the whole tree.
if [ ! -d "$STORAGE_ROOT" ]; then
  mkdir -p "$(dirname "$STORAGE_ROOT")"
  install -d -o bkt -g bkt -m 750 "$STORAGE_ROOT"
elif [ "$(stat -c %u "$STORAGE_ROOT")" != "$(id -u bkt)" ]; then
  log "Re-owning $STORAGE_ROOT to bkt (one-time migration from a root-run release)"
  chown -R bkt:bkt "$STORAGE_ROOT"
fi

# TLS key must be readable by bkt. Our generated pair is simply chowned; a
# user-supplied pair that bkt can't read (e.g. a 0600 bind mount) is copied
# into a private runtime dir instead of changing the operator's files.
if [ "${TLS_ENABLED:-false}" = "true" ]; then
  if [ "$TLS_CERT_FILE" = "$CERT_DIR/tls.crt" ] && [ "$TLS_KEY_FILE" = "$CERT_DIR/tls.key" ]; then
    # Older releases created $CERT_DIR as root 0700 (global umask 077), which
    # bkt can't traverse even when the files themselves are chowned.
    chown bkt:bkt "$CERT_DIR" "$TLS_CERT_FILE" "$TLS_KEY_FILE"
    chmod 750 "$CERT_DIR"
    chmod 600 "$TLS_KEY_FILE"
  fi
  if ! su-exec bkt test -r "$TLS_KEY_FILE" || ! su-exec bkt test -r "$TLS_CERT_FILE"; then
    RUN_TLS=/run/bkt-tls
    install -d -o bkt -g bkt -m 700 "$RUN_TLS"
    install -o bkt -g bkt -m 644 "$TLS_CERT_FILE" "$RUN_TLS/tls.crt"
    install -o bkt -g bkt -m 600 "$TLS_KEY_FILE" "$RUN_TLS/tls.key"
    export TLS_CERT_FILE=$RUN_TLS/tls.crt TLS_KEY_FILE=$RUN_TLS/tls.key
    log "TLS cert/key not readable by bkt — using a private copy in $RUN_TLS"
  fi
fi

if [ "$ADMIN_GENERATED" = "1" ]; then
  echo "============================================================"
  echo "  bkt first-boot admin credentials"
  echo "    username: ${ADMIN_USERNAME}"
  echo "    password: ${ADMIN_PASSWORD}"
  echo "  (stored in ${SECRETS_FILE}; set ADMIN_PASSWORD to override)"
  echo "============================================================"
fi

# ── 5. Run backend; shut both down together ──────────────────────────────────
log "Starting bkt backend as user bkt (console:${CONSOLE_PORT} s3:${S3_API_PORT})"
su-exec bkt:bkt bkt &
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
