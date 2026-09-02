#!/usr/bin/env bash
# =============================================================================
# bkt restore — restore a backup produced by scripts/backup.sh
# =============================================================================
# Restores, into a target bkt deployment, IN THIS ORDER:
#
#   0. safety snapshot — a timestamped copy of the target's current secrets and
#      object bytes, kept next to the originals for manual roll-back
#   1. the Postgres database (metadata) — restored FIRST, authenticating with
#      the TARGET's CURRENT credentials (never the backup's: replaying a SQL
#      dump does not change the running Postgres role's password, so the
#      backup's DB_PASSWORD may simply not work on this target)
#   2. the object bytes for local-backend buckets
#   3. the secrets (JWT_SECRET, ENCRYPTION_KEY, ADMIN_PASSWORD, …) — LAST, so
#      a failed database restore aborts before secrets or bytes are touched
#      and the target keeps running on what it had
#
# ⚠️  THE ENCRYPTION KEY MUST MATCH ⚠️
#
#   The stored external-S3 credentials in the database are encrypted with
#   ENCRYPTION_KEY. Restoring the database WITHOUT restoring the matching
#   ENCRYPTION_KEY leaves those credentials as undecryptable ciphertext — the
#   S3 configs will exist but never work again. This script REFUSES to restore a
#   backup that has no ENCRYPTION_KEY unless you pass --force, and it warns
#   loudly if the target already runs a DIFFERENT key.
#
#   Metadata (DB) and bytes (buckets) are a matched pair — this script restores
#   both from the same artifact so they stay consistent.
#
# THIS IS DESTRUCTIVE. It overwrites the target database and the object bytes.
# You are prompted to type "yes" before anything is written (skip with --yes).
#
# Supports the same two layouts as backup.sh:
#   * omnibus  — single container; DB restored over the loopback Postgres,
#                bytes -> /data/buckets, secrets -> /data/secrets.env in the
#                container (the restored file KEEPS the target's current
#                DB_PASSWORD — the SQL restore does not change the Postgres
#                role's password, so the backup's would not authenticate).
#   * compose  — separate Postgres container; DB restored via the db container,
#                bytes -> ./data/buckets on the host. Secrets are written to
#                a .env.restored file for you to reconcile (see notes on exit).
#
# Usage:
#   ./scripts/restore.sh <artifact.tar.gz> [options]
#   ./scripts/restore.sh /backups/bkt-backup-20260901-020000.tar.gz --mode omnibus
#
# Run ./scripts/restore.sh --help for all options.
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

ARTIFACT=""
MODE="${BKT_MODE:-auto}"
OMNIBUS_CONTAINER="${BKT_CONTAINER:-bkt}"
DB_CONTAINER="${BKT_DB_CONTAINER:-bkt-db}"
COMPOSE_DB_SERVICE="${BKT_COMPOSE_DB_SERVICE:-postgres}"
COMPOSE_FILE="${BKT_COMPOSE_FILE:-}"
DB_USER="${BKT_DB_USER:-objectstore}"
DB_NAME="${BKT_DB_NAME:-objectstore}"
BUCKETS_DIR="${BKT_BUCKETS_DIR:-$REPO_ROOT/data/buckets}"
OMNIBUS_SECRETS="${BKT_SECRETS_FILE:-/data/secrets.env}"
COMPOSE_ENV_FILE="${BKT_ENV_FILE:-$REPO_ROOT/.env}"
ASSUME_YES=0
FORCE=0

usage() {
  cat <<'EOF'
bkt restore — restore a backup artifact produced by scripts/backup.sh.

USAGE:
  scripts/restore.sh <artifact.tar.gz> [options]

ARGUMENTS:
  <artifact.tar.gz>         The backup artifact to restore.

OPTIONS:
  --mode <auto|omnibus|compose>
                            Deployment layout. Default: auto-detect.
  --container <name>        Omnibus container name (default: bkt).
  --db-container <name>     Compose Postgres container name (default: bkt-db).
  --compose-service <name>  Compose Postgres service name (default: postgres).
  --compose-file <path>     Compose file for `docker compose exec`.
  --buckets-dir <path>      Host path for local object bytes (compose layout;
                            default: <repo>/data/buckets).
  --db-user <user>          Postgres user (default: objectstore).
  --db-name <name>          Postgres database (default: objectstore).
  --yes                     Skip the interactive confirmation prompts.
  --force                   Proceed even if the backup has NO ENCRYPTION_KEY, or
                            if the target runs a different key. USE WITH CARE.
  -h, --help                Show this help.

ENVIRONMENT:
  Same BKT_* env vars as backup.sh (BKT_MODE, BKT_CONTAINER, BKT_DB_CONTAINER,
  BKT_COMPOSE_DB_SERVICE, BKT_COMPOSE_FILE, BKT_BUCKETS_DIR, BKT_DB_USER,
  BKT_DB_NAME, BKT_SECRETS_FILE, BKT_ENV_FILE).

ORDER OF OPERATIONS: safety snapshot, then database (authenticating with the
TARGET's current DB credentials), then object bytes, then secrets last. A
database restore failure aborts before bytes or secrets are touched.

This operation OVERWRITES the target database and object bytes. Restart bkt
afterwards so it reloads secrets and reconnects to the restored database.
EOF
}

log()  { echo "[restore] $*"; }
warn() { echo "[restore] WARNING: $*" >&2; }
die()  { echo "[restore] ERROR: $*" >&2; exit 1; }

# Strip one layer of surrounding single or double quotes from stdin.
strip_quotes() { sed -e 's/^"\(.*\)"$/\1/' -e "s/^'\(.*\)'\$/\1/"; }

confirm() {
  # $1 = prompt. Honours --yes.
  [[ "$ASSUME_YES" == "1" ]] && return 0
  local reply
  read -r -p "$1 (type 'yes' to continue): " reply
  [[ "$reply" == "yes" ]] || die "aborted by user"
}

# ── Parse args ────────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help)          usage; exit 0 ;;
    --mode)             MODE="$2"; shift 2 ;;
    --container)        OMNIBUS_CONTAINER="$2"; shift 2 ;;
    --db-container)     DB_CONTAINER="$2"; shift 2 ;;
    --compose-service)  COMPOSE_DB_SERVICE="$2"; shift 2 ;;
    --compose-file)     COMPOSE_FILE="$2"; shift 2 ;;
    --buckets-dir)      BUCKETS_DIR="$2"; shift 2 ;;
    --db-user)          DB_USER="$2"; shift 2 ;;
    --db-name)          DB_NAME="$2"; shift 2 ;;
    --yes)              ASSUME_YES=1; shift ;;
    --force)            FORCE=1; shift ;;
    -*)                 die "unknown option: $1 (see --help)" ;;
    *)
      if [[ -z "$ARTIFACT" ]]; then ARTIFACT="$1"; shift
      else die "unexpected argument: $1"; fi ;;
  esac
done

[[ -n "$ARTIFACT" ]] || { usage; die "missing <artifact.tar.gz>"; }
[[ -f "$ARTIFACT" ]] || die "artifact not found: $ARTIFACT"
command -v docker >/dev/null 2>&1 || die "docker not found in PATH"

# ── Auto-detect the deployment layout ────────────────────────────────────────
container_running() { docker ps --format '{{.Names}}' | grep -qx "$1"; }

if [[ "$MODE" == "auto" ]]; then
  if container_running "$OMNIBUS_CONTAINER"; then
    MODE="omnibus"
  elif container_running "$DB_CONTAINER"; then
    MODE="compose"
  else
    die "could not auto-detect layout: neither '$OMNIBUS_CONTAINER' nor '$DB_CONTAINER' is running. Use --mode and --container/--db-container."
  fi
  log "Auto-detected mode: $MODE"
fi
[[ "$MODE" == "omnibus" || "$MODE" == "compose" ]] || die "invalid --mode: $MODE"

# ── Failure reporting ────────────────────────────────────────────────────────
# PHASE tracks how far the restore got so the EXIT trap can say exactly what
# was and wasn't changed, and how to recover.
PHASE="preflight"        # preflight → db → bytes → secrets → done
SNAP_SECRETS=""          # pre-restore secrets copy (omnibus, in-container path)
SNAP_BYTES=""            # pre-restore bytes archive path
PRESTORE_TS=""

on_exit() {
  local rc=$?
  rm -rf "$STAGE"
  if [[ "$rc" -eq 0 ]]; then return 0; fi
  echo >&2
  case "$PHASE" in
    db)
      warn "DATABASE RESTORE FAILED (exit $rc)."
      warn "Because the database is restored FIRST, nothing else was modified:"
      warn "  * object bytes: NOT touched"
      warn "  * secrets:      NOT touched (the target keeps its current secrets)"
      warn "  * database:     may be PARTIALLY restored — the dump replays"
      warn "                  DROP/CREATE statements and ON_ERROR_STOP=1 aborts"
      warn "                  mid-replay on the first error"
      warn "To retry: fix the cause (usually DB connectivity or credentials —"
      warn "this script authenticates with the TARGET's CURRENT DB password,"
      warn "read from the live deployment, not the backup) and re-run this"
      warn "script. The dump is --clean --if-exists, so re-running is safe."
      ;;
    bytes|secrets)
      warn "RESTORE FAILED during the '${PHASE}' step (exit $rc)."
      warn "The DATABASE WAS ALREADY RESTORED from the backup. Either re-run"
      warn "this script to finish the restore, or roll back everything using"
      warn "the pre-restore snapshot (pre-restore-${PRESTORE_TS}):"
      if [[ "$MODE" == "omnibus" ]]; then
        if [[ -n "$SNAP_BYTES" ]]; then
          warn "  object bytes snapshot (in container, if /data/buckets existed):"
          warn "    ${OMNIBUS_CONTAINER}:${SNAP_BYTES}"
          warn "    roll back with:"
          warn "      docker exec ${OMNIBUS_CONTAINER} sh -c 'rm -rf /data/buckets && tar -xzf ${SNAP_BYTES} -C /data'"
        fi
        if [[ -n "$SNAP_SECRETS" ]]; then
          warn "  secrets snapshot (in container, if ${OMNIBUS_SECRETS} existed):"
          warn "    ${OMNIBUS_CONTAINER}:${SNAP_SECRETS}"
          warn "    roll back with:"
          warn "      docker exec ${OMNIBUS_CONTAINER} cp '${SNAP_SECRETS}' '${OMNIBUS_SECRETS}'"
        fi
        warn "  database: no automatic snapshot — restore an earlier backup"
        warn "  artifact with this script to roll the database back."
      else
        if [[ -n "$SNAP_BYTES" ]]; then
          warn "  object bytes snapshot: ${SNAP_BYTES}"
          warn "    roll back with:"
          warn "      rm -rf '${BUCKETS_DIR}' && tar -xzf '${SNAP_BYTES}' -C '$(dirname "$BUCKETS_DIR")'"
        fi
        warn "  secrets: your live ${COMPOSE_ENV_FILE} was never modified"
        warn "  (restore only writes ${COMPOSE_ENV_FILE}.restored)."
        warn "  database: no automatic snapshot — restore an earlier backup"
        warn "  artifact with this script to roll the database back."
      fi
      ;;
  esac
}

# ── Unpack the artifact ──────────────────────────────────────────────────────
STAGE="$(mktemp -d)"
trap on_exit EXIT
log "Extracting $ARTIFACT…"
tar -xzf "$ARTIFACT" -C "$STAGE"

# The artifact holds a single top-level directory (bkt-backup-<ts>/).
BACKUP_DIR="$(find "$STAGE" -mindepth 1 -maxdepth 1 -type d | head -1)"
[[ -n "$BACKUP_DIR" ]] || die "artifact does not contain the expected backup directory"

DB_SQL="$BACKUP_DIR/database.sql"
BUCKETS_TGZ="$BACKUP_DIR/buckets.tar.gz"
SECRETS="$BACKUP_DIR/secrets.env"
[[ -f "$DB_SQL" ]]      || die "database.sql missing from artifact"
[[ -f "$BUCKETS_TGZ" ]] || die "buckets.tar.gz missing from artifact"
[[ -f "$SECRETS" ]]     || die "secrets.env missing from artifact"

[[ -f "$BACKUP_DIR/MANIFEST.txt" ]] && { echo; sed 's/^/  /' "$BACKUP_DIR/MANIFEST.txt"; echo; }

# ── Verify the encryption key is present ─────────────────────────────────────
BACKUP_KEY="$(grep -E '^ENCRYPTION_KEY=' "$SECRETS" | head -1 | cut -d= -f2- | strip_quotes || true)"
if [[ -z "$BACKUP_KEY" ]]; then
  warn "This backup has NO ENCRYPTION_KEY. Any stored external-S3 credentials in"
  warn "the database will be UNRESTORABLE (undecryptable ciphertext)."
  if [[ "$FORCE" != "1" ]]; then
    die "refusing to restore a backup without an ENCRYPTION_KEY. Re-run with --force to proceed anyway."
  fi
  warn "--force given: proceeding without an ENCRYPTION_KEY."
else
  log "ENCRYPTION_KEY present in backup."
fi

# ── Resolve the TARGET's CURRENT database credentials ────────────────────────
# The DB restore must authenticate against the *running* Postgres, whose role
# password is whatever the target currently uses. Replaying a SQL dump never
# changes role passwords, so the backup's DB_PASSWORD may simply be wrong here
# (e.g. restoring onto a fresh install with auto-generated secrets). Read the
# current credentials from the live deployment, NOT from the backup:
#   * omnibus — /data/secrets.env inside the container (read BEFORE it is
#               overwritten; the secrets restore happens last), falling back to
#               the container's DB_PASSWORD environment variable
#   * compose — the current project .env, falling back to POSTGRES_PASSWORD in
#               the same file, then to the db container's environment
BACKUP_DB_PASSWORD="$(grep -E '^DB_PASSWORD=' "$SECRETS" | head -1 | cut -d= -f2- | strip_quotes || true)"
CURRENT_DB_PASSWORD=""
CURRENT_DB_PASSWORD_LINE=""
if [[ "$MODE" == "omnibus" ]]; then
  if docker exec "$OMNIBUS_CONTAINER" test -f "$OMNIBUS_SECRETS" 2>/dev/null; then
    CURRENT_DB_PASSWORD_LINE="$(docker exec "$OMNIBUS_CONTAINER" sh -c "grep -E '^DB_PASSWORD=' '$OMNIBUS_SECRETS' | head -1" 2>/dev/null || true)"
    CURRENT_DB_PASSWORD="$(printf '%s\n' "${CURRENT_DB_PASSWORD_LINE#DB_PASSWORD=}" | strip_quotes)"
    [[ -n "$CURRENT_DB_PASSWORD_LINE" ]] || CURRENT_DB_PASSWORD=""
  fi
  if [[ -z "$CURRENT_DB_PASSWORD" ]]; then
    CURRENT_DB_PASSWORD="$(docker exec "$OMNIBUS_CONTAINER" sh -c 'printenv DB_PASSWORD' 2>/dev/null || true)"
    [[ -n "$CURRENT_DB_PASSWORD" ]] && CURRENT_DB_PASSWORD_LINE="DB_PASSWORD=${CURRENT_DB_PASSWORD}"
  fi
  [[ -n "$CURRENT_DB_PASSWORD" ]] || die "could not read the target's current DB password (looked in ${OMNIBUS_CONTAINER}:${OMNIBUS_SECRETS} and the container environment). Refusing to guess with the backup's password — it does not track the running Postgres role. Nothing has been changed. Set BKT_SECRETS_FILE / --container appropriately and re-run."
else
  if [[ -f "$COMPOSE_ENV_FILE" ]]; then
    CURRENT_DB_PASSWORD="$(grep -E '^DB_PASSWORD=' "$COMPOSE_ENV_FILE" | head -1 | cut -d= -f2- | strip_quotes || true)"
    if [[ -z "$CURRENT_DB_PASSWORD" ]]; then
      CURRENT_DB_PASSWORD="$(grep -E '^POSTGRES_PASSWORD=' "$COMPOSE_ENV_FILE" | head -1 | cut -d= -f2- | strip_quotes || true)"
    fi
  fi
  if [[ -z "$CURRENT_DB_PASSWORD" ]] && container_running "$DB_CONTAINER"; then
    CURRENT_DB_PASSWORD="$(docker exec "$DB_CONTAINER" sh -c 'printenv POSTGRES_PASSWORD' 2>/dev/null || true)"
  fi
  [[ -n "$CURRENT_DB_PASSWORD" ]] || die "could not read the target's current DB password (looked for DB_PASSWORD/POSTGRES_PASSWORD in ${COMPOSE_ENV_FILE} and in container '${DB_CONTAINER}'). Refusing to guess with the backup's password — it does not track the running Postgres role. Nothing has been changed. Set BKT_ENV_FILE / --db-container appropriately and re-run."
fi
log "Using the target's CURRENT DB credentials for the restore (not the backup's)."
if [[ -n "$BACKUP_DB_PASSWORD" && "$BACKUP_DB_PASSWORD" != "$CURRENT_DB_PASSWORD" ]]; then
  warn "The backup's DB_PASSWORD differs from the target's current one. The"
  warn "database restore authenticates with the CURRENT password; the Postgres"
  warn "role's password is NOT changed by replaying the dump."
fi

# ── Warn if the target already runs a DIFFERENT encryption key ───────────────
# (Restoring a DB encrypted under key A onto an instance still configured with
# key B makes the restored S3 configs undecryptable.)
CURRENT_KEY=""
if [[ "$MODE" == "omnibus" ]]; then
  if docker exec "$OMNIBUS_CONTAINER" test -f "$OMNIBUS_SECRETS" 2>/dev/null; then
    CURRENT_KEY="$(docker exec "$OMNIBUS_CONTAINER" sh -c "grep -E '^ENCRYPTION_KEY=' '$OMNIBUS_SECRETS' | head -1 | cut -d= -f2-" 2>/dev/null || true)"
  fi
elif [[ -f "$COMPOSE_ENV_FILE" ]]; then
  CURRENT_KEY="$(grep -E '^ENCRYPTION_KEY=' "$COMPOSE_ENV_FILE" | head -1 | cut -d= -f2- || true)"
fi
if [[ -n "$CURRENT_KEY" && -n "$BACKUP_KEY" && "$CURRENT_KEY" != "$BACKUP_KEY" ]]; then
  warn "The target already runs a DIFFERENT ENCRYPTION_KEY than this backup."
  warn "After restore you must run bkt with the backup's ENCRYPTION_KEY, or the"
  warn "restored external-S3 credentials will not decrypt."
fi

# ── Big red confirmation ─────────────────────────────────────────────────────
echo
warn "About to OVERWRITE the target deployment (mode: $MODE), in this order:"
warn "  0. a timestamped snapshot of the current secrets + object bytes is kept"
warn "  1. database '$DB_NAME' is restored from the backup (existing data"
warn "     dropped) — authenticating with the target's CURRENT DB credentials"
warn "  2. object bytes at the buckets directory are replaced"
warn "  3. secrets are restored LAST"
if [[ "$MODE" == "compose" ]]; then
  warn "     (compose: written to ${COMPOSE_ENV_FILE}.restored — your live .env is not modified)"
else
  warn "     (omnibus: ${OMNIBUS_SECRETS} is overwritten, keeping the current DB_PASSWORD)"
fi
warn "If the database restore fails, the script aborts BEFORE touching object"
warn "bytes or secrets."
echo
confirm "This is DESTRUCTIVE and cannot be undone. Proceed?"

# ── 0. Safety snapshot of what we're about to overwrite ──────────────────────
# The database is restored first, so a psql failure aborts before secrets or
# bytes are touched — but a failure AFTER the DB restore (bytes/secrets step)
# still needs a way back. Keep a timestamped pre-restore copy of both; the
# EXIT trap prints the exact roll-back commands if a later step fails.
PRESTORE_TS="$(date +%Y%m%d-%H%M%S)"
log "Snapshotting current secrets + object bytes (pre-restore-${PRESTORE_TS})…"
if [[ "$MODE" == "omnibus" ]]; then
  SNAP_SECRETS="${OMNIBUS_SECRETS}.pre-restore-${PRESTORE_TS}"
  SNAP_BYTES="/data/buckets.pre-restore-${PRESTORE_TS}.tgz"
  docker exec "$OMNIBUS_CONTAINER" sh -c "
    cp '$OMNIBUS_SECRETS' '$SNAP_SECRETS' 2>/dev/null || true
    if [ -d /data/buckets ]; then
      tar -czf '$SNAP_BYTES' -C /data buckets 2>/dev/null || true
    fi"
  log "Snapshot in ${OMNIBUS_CONTAINER}:/data (delete *.pre-restore-* once verified)"
else
  if [[ -d "$BUCKETS_DIR" ]]; then
    SNAP_BYTES="${BUCKETS_DIR%/}.pre-restore-${PRESTORE_TS}.tgz"
    tar -czf "$SNAP_BYTES" \
      -C "$(dirname "$BUCKETS_DIR")" "$(basename "$BUCKETS_DIR")" 2>/dev/null || true
    log "Snapshot at $SNAP_BYTES (delete once verified)"
  fi
fi

# ── 1. Restore the database (FIRST) ──────────────────────────────────────────
# The dump was taken with --clean --if-exists, so replaying it drops and
# recreates each object over the existing (initialised) database. Runs before
# anything else is overwritten: if psql fails (ON_ERROR_STOP=1), the target
# still has its current secrets and object bytes. Authentication uses the
# TARGET's CURRENT DB password resolved above — never the backup's.
PHASE="db"
log "Restoring database '$DB_NAME' (authenticating with the target's current credentials)…"
if [[ "$MODE" == "omnibus" ]]; then
  docker exec -i -e PGPASSWORD="$CURRENT_DB_PASSWORD" "$OMNIBUS_CONTAINER" \
    psql -h 127.0.0.1 -U "$DB_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 < "$DB_SQL"
else
  compose_cmd=(docker compose)
  [[ -n "$COMPOSE_FILE" ]] && compose_cmd+=(-f "$COMPOSE_FILE")
  if (cd "$REPO_ROOT" && "${compose_cmd[@]}" ps "$COMPOSE_DB_SERVICE" >/dev/null 2>&1); then
    (cd "$REPO_ROOT" && "${compose_cmd[@]}" exec -T -e PGPASSWORD="$CURRENT_DB_PASSWORD" \
      "$COMPOSE_DB_SERVICE" psql -U "$DB_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1) < "$DB_SQL"
  else
    docker exec -i -e PGPASSWORD="$CURRENT_DB_PASSWORD" "$DB_CONTAINER" \
      psql -U "$DB_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 < "$DB_SQL"
  fi
fi
log "Database restore complete."

# ── 2. Restore object bytes ──────────────────────────────────────────────────
PHASE="bytes"
BYTES_SIZE="$(wc -c < "$BUCKETS_TGZ" | tr -d ' ')"
if [[ "$BYTES_SIZE" -gt 0 ]]; then
  log "Restoring object bytes…"
  if [[ "$MODE" == "omnibus" ]]; then
    # Replace /data/buckets inside the container from the streamed archive.
    docker exec "$OMNIBUS_CONTAINER" sh -c 'rm -rf /data/buckets && mkdir -p /data'
    docker exec -i "$OMNIBUS_CONTAINER" tar -xzf - -C /data < "$BUCKETS_TGZ"
    log "Restored bytes to ${OMNIBUS_CONTAINER}:/data/buckets"
  else
    mkdir -p "$(dirname "$BUCKETS_DIR")"
    rm -rf "$BUCKETS_DIR"
    tar -xzf "$BUCKETS_TGZ" -C "$(dirname "$BUCKETS_DIR")"
    log "Restored bytes to $BUCKETS_DIR"
  fi
else
  warn "Backup contains no object bytes (external S3 backend, or empty). Skipping byte restore."
fi

# ── 3. Restore secrets (LAST) ────────────────────────────────────────────────
PHASE="secrets"
log "Restoring secrets…"
if [[ "$MODE" == "omnibus" ]]; then
  # Copy the secrets file into the container's /data volume. The omnibus treats
  # /data/secrets.env as authoritative on boot, so this pins the restored key.
  # KEEP the target's CURRENT DB_PASSWORD, though: the SQL restore does not
  # change the running Postgres role's password, so writing the backup's
  # DB_PASSWORD would leave bkt unable to reach its own database on restart.
  RESTORED_SECRETS="$STAGE/secrets.to-restore.env"
  grep -vE '^DB_PASSWORD=' "$SECRETS" > "$RESTORED_SECRETS" || true
  if [[ -n "$CURRENT_DB_PASSWORD_LINE" ]]; then
    printf '%s\n' "$CURRENT_DB_PASSWORD_LINE" >> "$RESTORED_SECRETS"
  else
    printf 'DB_PASSWORD=%s\n' "$CURRENT_DB_PASSWORD" >> "$RESTORED_SECRETS"
  fi
  chmod 600 "$RESTORED_SECRETS"
  docker cp "$RESTORED_SECRETS" "${OMNIBUS_CONTAINER}:${OMNIBUS_SECRETS}"
  docker exec "$OMNIBUS_CONTAINER" chmod 600 "$OMNIBUS_SECRETS" 2>/dev/null || true
  log "Wrote secrets to ${OMNIBUS_CONTAINER}:${OMNIBUS_SECRETS} (kept the target's current DB_PASSWORD)"
else
  # For compose the secrets live in the project-root .env. We DON'T clobber a
  # live .env automatically (it may hold host-specific tweaks) — we drop a
  # .env.restored next to it and tell the operator to reconcile it.
  RESTORED_ENV="${COMPOSE_ENV_FILE}.restored"
  cp "$SECRETS" "$RESTORED_ENV"
  chmod 600 "$RESTORED_ENV"
  log "Wrote restored secrets to $RESTORED_ENV (reconcile into your .env — see notes below)"
fi
PHASE="done"

# ── Done ─────────────────────────────────────────────────────────────────────
echo
log "Restore finished."
if [[ "$MODE" == "omnibus" ]]; then
  warn "Restart the container so bkt reloads /data/secrets.env and the restored DB:"
  warn "    docker restart $OMNIBUS_CONTAINER"
  warn "Note: the restored secrets keep the target's CURRENT DB_PASSWORD (the"
  warn "SQL restore does not change the Postgres role's password)."
else
  warn "Reconcile the restored secrets into your .env, then restart the stack:"
  warn "    review ${COMPOSE_ENV_FILE}.restored  (contains the backup's ENCRYPTION_KEY / DB_PASSWORD)"
  warn "    ensure ENCRYPTION_KEY in .env matches the backup, BUT keep your"
  warn "    CURRENT DB_PASSWORD — the SQL restore does not change the running"
  warn "    Postgres role's password, so the backup's DB_PASSWORD will not"
  warn "    authenticate unless you ALTER ROLE yourself. Then:"
  warn "    docker compose ${COMPOSE_FILE:+-f $COMPOSE_FILE} up -d --force-recreate backend"
fi
warn "Pre-restore snapshot kept as pre-restore-${PRESTORE_TS} (delete once verified)."
echo
warn "If the ENCRYPTION_KEY now in effect differs from the one the data was"
warn "encrypted with, stored external-S3 credentials will not decrypt."
