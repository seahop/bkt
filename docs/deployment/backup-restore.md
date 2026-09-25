# Backup & Restore

How to take a **consistent, restorable** backup of a bkt deployment and how to
restore it — using the helper scripts in [`scripts/`](../../scripts).

> ## ⚠️ Read this first
>
> **1. The encryption key is part of the backup.**
> bkt encrypts the external-S3 credentials it stores in Postgres with
> `ENCRYPTION_KEY`. A **database backup without the matching `ENCRYPTION_KEY` is
> not fully restorable** — the stored S3 configs come back as undecryptable
> ciphertext. The key lives in `secrets.env` (omnibus: `/data/secrets.env`;
> compose: your project-root `.env`). Keep it with every backup, and ideally
> store the key (or the whole `secrets.env`) in a **separate secure location** —
> a secrets manager / vault — so a leaked data archive alone is not enough to
> use it, and a lost archive does not take the key with it.
>
> **2. Metadata and bytes are a matched pair.**
> Postgres holds the object **metadata** (buckets, objects, users, policies,
> access keys). The `data/buckets` directory (local backend) — or an external S3
> bucket — holds the object **bytes**. They must be backed up and restored **as a
> consistent set**. The scripts take the DB dump and the byte copy back-to-back;
> for a strictly consistent snapshot on a busy instance, pause writes (or stop
> the app container) while backing up.

## Contents

- [What's in a backup](#whats-in-a-backup)
- [On-disk layout (local backend)](#on-disk-layout-local-backend)
- [Taking a backup](#taking-a-backup)
- [Restoring a backup](#restoring-a-backup)
- [Deployment layouts](#deployment-layouts)
- [External S3 backend](#external-s3-backend)
- [Scheduling](#scheduling)
- [Options reference](#options-reference)

## What's in a backup

`scripts/backup.sh` produces **one** timestamped artifact,
`bkt-backup-YYYYMMDD-HHMMSS.tar.gz`, containing:

| Member | Contents |
|---|---|
| `database.sql` | `pg_dump` of the Postgres database (metadata), taken with `--clean --if-exists` so it replays cleanly over an initialised DB |
| `buckets.tar.gz` | The object **bytes** for local-backend buckets (the `data/buckets` / `STORAGE_ROOT` directory) |
| `secrets.env` | `DB_PASSWORD`, `JWT_SECRET`, **`ENCRYPTION_KEY`**, `ADMIN_PASSWORD` |
| `MANIFEST.txt` | When/how the backup was taken, sizes, and whether the encryption key is present |

The artifact is written mode `600` because it contains secrets. **Treat it like
a private key.**

## On-disk layout (local backend)

The local backend does not store objects under their key names. Every object
lives under the SHA-256 (lowercase hex) of its raw key bytes, `h`:

```text
<STORAGE_ROOT>/
  .objects/<bucket>/<h[0:2]>/<h[2:4]>/<h>        current object bytes
  .objects/<bucket>/<h[0:2]>/<h[2:4]>/<h>.key    the object's key (UTF-8)
  .objects/<bucket>/.layout-v2                   migration completion marker
  .objversions/<bucket>/<h>/<version-id>         archived version bytes
  .objversions/<bucket>/<h>/.key                 the key those versions belong to
  .multipart/<upload-id>/part.NNNNN, meta.json   in-progress multipart uploads
```

This is what lets keys be arbitrary S3 keys (`p` next to `p/q`, `..`, a
leading `/`, very long names) — see
[object keys](../guides/features.md#object-keys-and-uploads). Consequences for
operators:

- **Back up the whole data directory** (`data/buckets` / `STORAGE_ROOT`,
  hidden directories included — `scripts/backup.sh` and a plain
  `tar -C <parent> buckets` do). Partial copies by bucket or by object path are
  not possible from the file names alone.
- The files are **not human-browsable**: a `<h>.key` file next to each object
  (and `.key` in each version directory) holds the key in plain UTF-8, which
  is enough to identify or recover single objects by hand
  (`grep -rl --include='*.key' -x 'photos/cat.jpg' .objects/<bucket>`); for
  anything else use the S3 API or console.
- Content type, user metadata, tags and version order live in Postgres (as
  before); the bytes and the database must be restored as a pair.
- Files named `.tmp-upload-*`, `.tmp-assemble-*` and `.tmp-key-*` are
  interrupted writes and are safe to delete while bkt is stopped.

### Upgrading from the key-as-path layout

Releases before the blob layout stored each object at `<STORAGE_ROOT>/<bucket>/<key>`
and versions under `<STORAGE_ROOT>/.versions/`. The first start of a newer
release **migrates** that data automatically, before it serves any request:

- **Back up the data volume (and the database) first.**
- Stop every bkt instance that uses the volume; only the upgraded one may run
  during the migration.
- Every legacy file is renamed (same filesystem, atomic; nothing is copied)
  into the blob layout after its key file was written; emptied legacy
  directories are removed and `.objects/<bucket>/.layout-v2` marks the bucket
  as done. Progress and a per-bucket summary are logged
  (`Local storage: ...` lines, with a start and a finish line).
- It is **resumable**: if it stops (crash, full disk, permission error) bkt
  exits with the reason; fix it and start again — the migration continues
  where it stopped without losing or duplicating anything.
- Files it cannot place are **left in the legacy directory and logged**, never
  deleted: interrupted-upload temp files, symlinks/special files, archived
  versions with an unexpected name, and (only possible after a crash or manual
  copying) a key that already exists in the blob layout with *different*
  bytes (the blob-layout copy wins; an identical legacy copy is removed).
- Folder markers `dir/` (stored as `dir/.bkt-folder`) become the object
  `dir/`. Releases before folder-marker support stored a marker `dir/` as an
  empty file `dir`, which cannot be told apart from an empty object `dir`; it
  becomes the object `dir` (an empty marker whose metadata row says `dir/` then
  reads as missing — re-create the folder if needed). The same applies to
  version files of such markers.
- Restoring a backup taken **before** the upgrade is supported: restore it,
  then restart bkt (as `scripts/restore.sh` instructs) so the migration runs
  again. Replace the data directory rather than extracting an old archive over
  a migrated one (`scripts/restore.sh` does): a bucket whose
  `.objects/<bucket>/.layout-v2` marker survives is not migrated again, and
  its legacy files are only reported.

## Taking a backup

```bash
# Auto-detects omnibus vs compose from the running containers.
./scripts/backup.sh /backups
```

Force a layout or point at a specific deployment:

```bash
# Omnibus (single container named "bkt")
./scripts/backup.sh /backups --mode omnibus --container bkt

# Compose (production stack)
./scripts/backup.sh /backups --mode compose --compose-file docker-compose.prod.yml
```

The script prints a summary and warns you if the encryption key is missing:

```
[backup] Mode: omnibus   Artifact: /backups/bkt-backup-20260901-020000.tar.gz
[backup] Database dump: 148213 bytes
[backup] Object bytes archive: 20481 bytes
[backup] Backup complete: /backups/bkt-backup-20260901-020000.tar.gz
[backup]   secrets.env     ENCRYPTION_KEY present: yes
```

## Restoring a backup

Restore is **destructive** — it overwrites the target database and the object
bytes — so it prompts for confirmation before writing anything.

```bash
./scripts/restore.sh /backups/bkt-backup-20260901-020000.tar.gz
```

The script:

1. **Verifies the encryption key** is in the backup. If it is missing it
   **refuses** to continue unless you pass `--force`.
2. **Resolves the target's *current* database credentials** — omnibus: from
   `/data/secrets.env` inside the container (read *before* anything is
   overwritten), falling back to the container's environment; compose: from
   your project `.env` (`DB_PASSWORD`, then `POSTGRES_PASSWORD`), falling back
   to the Postgres container's environment. The database restore authenticates
   with **these**, never the backup's: replaying a SQL dump does not change the
   running Postgres role's password, so the backup's `DB_PASSWORD` may simply
   not work on the target (e.g. a fresh install with auto-generated secrets).
   If no current password can be found, the script aborts before changing
   anything.
3. **Warns loudly** if the target already runs a *different* `ENCRYPTION_KEY`
   than the backup (restoring under the wrong key breaks stored S3 configs).
4. Prompts you to type `yes` before overwriting anything (skip with `--yes`).
5. Takes a **timestamped pre-restore snapshot** of the current secrets and
   object bytes (`*.pre-restore-<timestamp>*`, kept next to the originals).
6. Restores the **database first**, then the object bytes, then the secrets
   **last**.

Ordered this way, a failed database restore (`psql` runs with
`ON_ERROR_STOP=1`) **aborts before the object bytes or secrets are touched** —
the target keeps its current secrets and data, and re-running the script after
fixing the cause is safe (the dump is `--clean --if-exists`). If a *later* step
fails, the database has already been restored; the script then prints the
snapshot paths and the exact commands to roll the bytes/secrets back.

After a restore, **restart bkt** so it reloads the secrets and reconnects to the
restored database:

```bash
# Omnibus
docker restart bkt

# Compose — reconcile the restored secrets into your .env first (see below),
# then recreate the backend:
docker compose -f docker-compose.prod.yml up -d --force-recreate backend
```

> **Compose note:** to avoid clobbering host-specific tweaks, restore does **not**
> overwrite a live compose `.env`. It writes the backed-up secrets to
> `.env.restored` and asks you to reconcile them — at minimum, make sure
> `ENCRYPTION_KEY` in your `.env` matches the one in the backup, or the restored
> S3 credentials will not decrypt. **Keep your current `DB_PASSWORD`**, though:
> the SQL restore does not change the running Postgres role's password, so the
> backup's `DB_PASSWORD` will not authenticate unless you `ALTER ROLE` yourself.
>
> **Omnibus note:** the restored `/data/secrets.env` automatically **keeps the
> target's current `DB_PASSWORD`** for the same reason — everything else
> (`ENCRYPTION_KEY`, `JWT_SECRET`, `ADMIN_PASSWORD`) comes from the backup.

## Deployment layouts

Both scripts support the two multi-state layouts and auto-detect which one is
running. See [deployment-options.md](deployment-options.md) for the full picture.

| | Omnibus (single container) | Compose (`docker-compose(.prod).yml`) |
|---|---|---|
| Secrets | `/data/secrets.env` (in container) | project-root `.env` |
| Object bytes | `/data/buckets` (in container) | `./data/buckets` (host bind mount) |
| Database | Postgres on loopback in the same container | separate `postgres` container (`bkt-db`) |
| Restore of secrets | copied back into `/data/secrets.env` (keeps the current `DB_PASSWORD`) | written to `.env.restored` to reconcile |
| DB restore auth | current `DB_PASSWORD` from `/data/secrets.env` (or container env) | current `DB_PASSWORD`/`POSTGRES_PASSWORD` from `.env` (or db container env) |

If your container or service names differ from the defaults (`bkt`, `bkt-db`,
service `postgres`), pass `--container` / `--db-container` / `--compose-service`.

## External S3 backend

If `STORAGE_BACKEND=s3` (object bytes live in an external S3 bucket rather than on
local disk), the backup still captures the **metadata and secrets**, but **not the
remote bytes** — there is no local `data/buckets` to archive. Back up those bytes
with your S3 provider's own tooling (versioning, replication, lifecycle). The
metadata + `ENCRYPTION_KEY` backup remains essential: without it you cannot
reconnect the stored (encrypted) S3 configuration.

## Scheduling

Run the backup from cron and prune old artifacts:

```bash
# /etc/cron.d/bkt-backup — daily at 02:00
0 2 * * *  root  /opt/bkt/scripts/backup.sh /backups >> /var/log/bkt-backup.log 2>&1

# Keep 14 days of artifacts (run after the backup, or as its own job)
0 3 * * *  root  find /backups -name 'bkt-backup-*.tar.gz' -mtime +14 -delete
```

Because each artifact contains secrets, store `/backups` on encrypted, access-
controlled storage — and, per the warning above, keep a copy of the
`ENCRYPTION_KEY` somewhere separate so a single lost or leaked archive is neither
useless nor dangerous.

## Options reference

Both scripts accept `--help`. Common options (each also has a matching `BKT_*`
environment variable):

| Option | Default | Meaning |
|---|---|---|
| `--mode <auto\|omnibus\|compose>` | `auto` | Deployment layout |
| `--container <name>` | `bkt` | Omnibus container name |
| `--db-container <name>` | `bkt-db` | Compose Postgres container name |
| `--compose-service <name>` | `postgres` | Compose Postgres service name |
| `--compose-file <path>` | _(none)_ | Compose file for `docker compose exec` |
| `--buckets-dir <path>` | `<repo>/data/buckets` | Host path to local object bytes (compose) |
| `--db-user` / `--db-name` | `objectstore` | Postgres user / database |

Restore-only:

| Option | Meaning |
|---|---|
| `--yes` | Skip the interactive confirmation prompts |
| `--force` | Proceed even if the backup has no `ENCRYPTION_KEY`, or the target runs a different key |
