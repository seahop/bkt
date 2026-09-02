#!/usr/bin/env bash
# =============================================================================
# bkt backup — consistent, self-contained backup of a bkt deployment
# =============================================================================
# Produces ONE timestamped .tar.gz artifact containing, as a consistent set:
#
#   1. database.sql   — pg_dump of the Postgres metadata (buckets, objects,
#                       users, policies, access keys, encrypted S3 configs)
#   2. buckets.tar.gz — the object BYTES for local-backend buckets
#                       (the STORAGE_ROOT / data/buckets directory)
#   3. secrets.env    — DB_PASSWORD, JWT_SECRET, ENCRYPTION_KEY, ADMIN_PASSWORD
#   4. MANIFEST.txt   — metadata about how/when the backup was taken
#
# ⚠️  READ THIS BEFORE YOU TRUST A BACKUP ⚠️
#
#   * ENCRYPTION_KEY (inside secrets.env) encrypts the external-S3 credentials
#     stored in Postgres. A database dump WITHOUT the matching ENCRYPTION_KEY is
#     effectively UNRESTORABLE for those S3 configs — the stored credentials
#     become undecryptable ciphertext. Keep secrets.env with the backup, but
#     ideally store it (or at least the ENCRYPTION_KEY) in a SEPARATE, secure
#     location (a secrets manager / vault) from the bulk data.
#
#   * Postgres holds the object METADATA; the buckets directory (or an external
#     S3 bucket) holds the BYTES. They are a matched pair. This script takes the
#     DB dump and the byte copy as close together in time as practical, but bkt
#     is not quiesced during the run — for a perfectly consistent snapshot on a
#     busy instance, pause writes (or stop the container) while backing up.
#
#   * If STORAGE_BACKEND=s3 (bytes live in an external S3 bucket, not on local
#     disk) this script backs up the metadata + secrets but NOT the remote
#     bytes — back those up with your S3 provider's tooling / versioning.
#
# Supports both deployment layouts:
#   * omnibus  — single container (API + UI + Postgres); secrets in
#                /data/secrets.env, bytes in /data/buckets, Postgres on loopback.
#   * compose  — docker-compose(.prod).yml; separate Postgres container, secrets
#                in the project-root .env, bytes in ./data/buckets on the host.
#
# Usage:
#   ./scripts/backup.sh <output-dir> [options]
#   ./scripts/backup.sh /backups
#   ./scripts/backup.sh /backups --mode omnibus --container bkt
#   ./scripts/backup.sh /backups --mode compose --compose-file docker-compose.prod.yml
#
# Run ./scripts/backup.sh --help for all options.
# =============================================================================
set -euo pipefail

# ── Defaults (override via flags or the matching BKT_* env vars) ──────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

OUTPUT_DIR=""
MODE="${BKT_MODE:-auto}"                       # auto | omnibus | compose
OMNIBUS_CONTAINER="${BKT_CONTAINER:-bkt}"      # omnibus container name
DB_CONTAINER="${BKT_DB_CONTAINER:-bkt-db}"     # compose Postgres container name
COMPOSE_DB_SERVICE="${BKT_COMPOSE_DB_SERVICE:-postgres}"  # compose service name
COMPOSE_FILE="${BKT_COMPOSE_FILE:-}"           # e.g. docker-compose.prod.yml
DB_USER="${BKT_DB_USER:-objectstore}"
DB_NAME="${BKT_DB_NAME:-objectstore}"
# Host path to the local object bytes (compose layout). Inside the omnibus the
# bytes are always at /data/buckets in the container.
BUCKETS_DIR="${BKT_BUCKETS_DIR:-$REPO_ROOT/data/buckets}"
# Secrets source: omnibus = in-container path; compose = host .env
OMNIBUS_SECRETS="${BKT_SECRETS_FILE:-/data/secrets.env}"
COMPOSE_ENV_FILE="${BKT_ENV_FILE:-$REPO_ROOT/.env}"

usage() {
  cat <<'EOF'
bkt backup — create a consistent, self-contained backup artifact.

USAGE:
  scripts/backup.sh <output-dir> [options]

ARGUMENTS:
  <output-dir>              Directory to write the .tar.gz artifact into (created
                            if missing).

OPTIONS:
  --mode <auto|omnibus|compose>
                            Deployment layout. Default: auto-detect from running
                            containers.
  --container <name>        Omnibus container name (default: bkt).
  --db-container <name>     Compose Postgres container name (default: bkt-db).
  --compose-service <name>  Compose Postgres service name (default: postgres).
  --compose-file <path>     Compose file to use for `docker compose exec`
                            (e.g. docker-compose.prod.yml).
  --buckets-dir <path>      Host path to local object bytes for the compose
                            layout (default: <repo>/data/buckets).
  --db-user <user>          Postgres user (default: objectstore).
  --db-name <name>          Postgres database (default: objectstore).
  -h, --help                Show this help.

ENVIRONMENT:
  Every option has a matching BKT_* env var: BKT_MODE, BKT_CONTAINER,
  BKT_DB_CONTAINER, BKT_COMPOSE_DB_SERVICE, BKT_COMPOSE_FILE, BKT_BUCKETS_DIR,
  BKT_DB_USER, BKT_DB_NAME, BKT_SECRETS_FILE, BKT_ENV_FILE.

EXAMPLES:
  scripts/backup.sh /backups
  scripts/backup.sh /backups --mode omnibus --container bkt
  scripts/backup.sh /backups --mode compose --compose-file docker-compose.prod.yml

The artifact restores with scripts/restore.sh. Store the ENCRYPTION_KEY (inside
secrets.env) securely — without it a database backup cannot be fully restored.
EOF
}

log()  { echo "[backup] $*"; }
warn() { echo "[backup] WARNING: $*" >&2; }
die()  { echo "[backup] ERROR: $*" >&2; exit 1; }

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
    -*)                 die "unknown option: $1 (see --help)" ;;
    *)
      if [[ -z "$OUTPUT_DIR" ]]; then OUTPUT_DIR="$1"; shift
      else die "unexpected argument: $1"; fi ;;
  esac
done

[[ -n "$OUTPUT_DIR" ]] || { usage; die "missing <output-dir>"; }
command -v docker >/dev/null 2>&1 || die "docker not found in PATH"

# ── Auto-detect the deployment layout ────────────────────────────────────────
container_running() { docker ps --format '{{.Names}}' | grep -qx "$1"; }

if [[ "$MODE" == "auto" ]]; then
  if container_running "$OMNIBUS_CONTAINER"; then
    MODE="omnibus"
  elif container_running "$DB_CONTAINER"; then
    MODE="compose"
  else
    die "could not auto-detect layout: neither '$OMNIBUS_CONTAINER' (omnibus) nor '$DB_CONTAINER' (compose) is running. Use --mode and --container/--db-container."
  fi
  log "Auto-detected mode: $MODE"
fi
[[ "$MODE" == "omnibus" || "$MODE" == "compose" ]] || die "invalid --mode: $MODE"

# ── Prepare the staging area ─────────────────────────────────────────────────
mkdir -p "$OUTPUT_DIR"
TIMESTAMP="$(date +%Y%m%d-%H%M%S)"
STAMP_UTC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
NAME="bkt-backup-${TIMESTAMP}"
STAGE="$(mktemp -d)"
WORK="$STAGE/$NAME"
mkdir -p "$WORK"
trap 'rm -rf "$STAGE"' EXIT

log "Mode: $MODE   Artifact: ${OUTPUT_DIR%/}/${NAME}.tar.gz"
echo
warn "The database dump and object bytes are taken close in time but bkt is NOT"
warn "quiesced. For a strictly consistent snapshot on a busy instance, pause"
warn "writes (or stop the app container) for the duration of this run."
echo

# ── 1. Secrets (grabbed FIRST — the key protects everything else) ────────────
log "Capturing secrets…"
if [[ "$MODE" == "omnibus" ]]; then
  if ! docker exec "$OMNIBUS_CONTAINER" test -f "$OMNIBUS_SECRETS" 2>/dev/null; then
    die "secrets file $OMNIBUS_SECRETS not found in container '$OMNIBUS_CONTAINER'"
  fi
  docker exec "$OMNIBUS_CONTAINER" cat "$OMNIBUS_SECRETS" > "$WORK/secrets.env"
else
  [[ -f "$COMPOSE_ENV_FILE" ]] || die "env/secrets file not found: $COMPOSE_ENV_FILE"
  cp "$COMPOSE_ENV_FILE" "$WORK/secrets.env"
fi
chmod 600 "$WORK/secrets.env"

# Sanity-check that the encryption key is actually present.
if grep -q '^ENCRYPTION_KEY=..*' "$WORK/secrets.env"; then
  HAS_KEY="yes"
else
  HAS_KEY="no"
  warn "ENCRYPTION_KEY is MISSING or empty in the captured secrets!"
  warn "Stored external-S3 credentials will NOT be restorable from this backup."
fi

# Read DB_PASSWORD from the captured secrets for the pg_dump auth
# (strips surrounding single/double quotes if present).
DB_PASSWORD="$(grep -E '^DB_PASSWORD=' "$WORK/secrets.env" | head -1 | cut -d= -f2- | sed -e 's/^"\(.*\)"$/\1/' -e "s/^'\(.*\)'\$/\1/" || true)"

# ── 2. Database dump (metadata) ──────────────────────────────────────────────
# --clean --if-exists makes the dump self-drop objects on restore, so it can be
# replayed into an already-initialised database without duplicate-key errors.
log "Dumping database '$DB_NAME'…"
PGDUMP_OPTS=(--clean --if-exists --no-owner --no-privileges)

if [[ "$MODE" == "omnibus" ]]; then
  # Postgres listens on loopback inside the container (scram auth); pass the
  # password we just read from secrets.env.
  docker exec -e PGPASSWORD="$DB_PASSWORD" "$OMNIBUS_CONTAINER" \
    pg_dump -h 127.0.0.1 -U "$DB_USER" "${PGDUMP_OPTS[@]}" "$DB_NAME" \
    > "$WORK/database.sql"
else
  compose_cmd=(docker compose)
  [[ -n "$COMPOSE_FILE" ]] && compose_cmd+=(-f "$COMPOSE_FILE")
  # Prefer `docker compose exec`; fall back to `docker exec` on the container.
  if (cd "$REPO_ROOT" && "${compose_cmd[@]}" ps "$COMPOSE_DB_SERVICE" >/dev/null 2>&1); then
    (cd "$REPO_ROOT" && "${compose_cmd[@]}" exec -T -e PGPASSWORD="$DB_PASSWORD" \
      "$COMPOSE_DB_SERVICE" pg_dump -U "$DB_USER" "${PGDUMP_OPTS[@]}" "$DB_NAME") \
      > "$WORK/database.sql"
  else
    docker exec -e PGPASSWORD="$DB_PASSWORD" "$DB_CONTAINER" \
      pg_dump -U "$DB_USER" "${PGDUMP_OPTS[@]}" "$DB_NAME" \
      > "$WORK/database.sql"
  fi
fi
DB_BYTES="$(wc -c < "$WORK/database.sql" | tr -d ' ')"
[[ "$DB_BYTES" -gt 0 ]] || die "database dump is empty — aborting"
log "Database dump: ${DB_BYTES} bytes"

# ── 3. Object bytes (local backend) ──────────────────────────────────────────
# Taken right after the DB dump to keep metadata and bytes as close as possible.
log "Archiving object bytes…"
BYTES_NOTE=""
if [[ "$MODE" == "omnibus" ]]; then
  # tar the /data/buckets dir out of the container as a stream.
  if docker exec "$OMNIBUS_CONTAINER" test -d /data/buckets 2>/dev/null; then
    docker exec "$OMNIBUS_CONTAINER" tar -czf - -C /data buckets > "$WORK/buckets.tar.gz"
  else
    BYTES_NOTE="/data/buckets not present in container (external S3 backend?) — no local bytes archived."
    warn "$BYTES_NOTE"
    : > "$WORK/buckets.tar.gz"
  fi
else
  if [[ -d "$BUCKETS_DIR" ]]; then
    tar -czf "$WORK/buckets.tar.gz" -C "$(dirname "$BUCKETS_DIR")" "$(basename "$BUCKETS_DIR")"
  else
    BYTES_NOTE="buckets dir $BUCKETS_DIR not found (external S3 backend?) — no local bytes archived."
    warn "$BYTES_NOTE"
    : > "$WORK/buckets.tar.gz"
  fi
fi
BYTES_SIZE="$(wc -c < "$WORK/buckets.tar.gz" | tr -d ' ')"
log "Object bytes archive: ${BYTES_SIZE} bytes"

# ── 4. Manifest ──────────────────────────────────────────────────────────────
cat > "$WORK/MANIFEST.txt" <<EOF
bkt backup manifest
===================
created_utc:        $STAMP_UTC
mode:               $MODE
db_name:            $DB_NAME
db_user:            $DB_USER
database.sql:       ${DB_BYTES} bytes (pg_dump --clean --if-exists)
buckets.tar.gz:     ${BYTES_SIZE} bytes
encryption_key:     ${HAS_KEY}
$( [[ -n "$BYTES_NOTE" ]] && echo "bytes_note:         $BYTES_NOTE" )

CONSISTENCY: the database (metadata) and the object bytes are a matched pair.
Restore both together. The DB dump and byte copy were taken sequentially without
quiescing the instance.

ENCRYPTION KEY: secrets.env carries ENCRYPTION_KEY, which decrypts stored
external-S3 credentials in the database. Without it those configs are
UNRESTORABLE. Store secrets.env (or the key) securely — ideally separately from
the bulk data.

Restore with: scripts/restore.sh ${NAME}.tar.gz
EOF

# ── 5. Bundle everything into one artifact ───────────────────────────────────
ARTIFACT="${OUTPUT_DIR%/}/${NAME}.tar.gz"
tar -czf "$ARTIFACT" -C "$STAGE" "$NAME"
chmod 600 "$ARTIFACT"

echo
log "Backup complete: $ARTIFACT"
log "  database.sql    ${DB_BYTES} bytes"
log "  buckets.tar.gz  ${BYTES_SIZE} bytes"
log "  secrets.env     ENCRYPTION_KEY present: ${HAS_KEY}"
echo
warn "This artifact contains SECRETS (DB password, JWT secret, ENCRYPTION_KEY,"
warn "admin password). Protect it like a private key. To restore it you MUST"
warn "keep the ENCRYPTION_KEY — a DB backup without it cannot be fully restored."
[[ "$HAS_KEY" == "yes" ]] || warn "This particular backup is MISSING its ENCRYPTION_KEY (see above)."
