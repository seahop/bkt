# Feature guide

The capabilities bkt provides beyond basic object storage, with console and
API usage for each. All S3 examples assume an `aws` CLI profile configured for
your bkt access key with `s3.addressing_style = path` (see
[MOUNTING](MOUNTING.md) / [getting started](getting-started.md)).

- [Object versioning](#object-versioning)
- [Lifecycle expiry](#lifecycle-expiry)
- [Storage quotas](#storage-quotas)
- [Retention (WORM)](#retention-worm)
- [Presigned share links](#presigned-share-links)
- [User metadata and tags](#user-metadata-and-tags)
- [Event notifications (webhooks)](#event-notifications-webhooks)
- [Groups](#groups)
- [Temporary credentials (bkt-STS)](#temporary-credentials-bkt-sts)
- [Replication (bucket mirroring)](#replication-bucket-mirroring)
- [Server-side encryption](#server-side-encryption)

---

## Object versioning

When versioning is **enabled** on a bucket, overwriting an object archives the
previous version, and deleting an object hides it behind a *delete marker*
instead of destroying it. Every version stays retrievable and restorable.

**Console**: bucket → Settings (gear icon) → Versioning → Enable. Each file
row gains a History action listing versions with Restore and
permanently-delete controls.

**S3 API** (standard AWS shapes):

```bash
aws s3api put-bucket-versioning --bucket my-bucket \
  --versioning-configuration Status=Enabled --endpoint-url $BKT --profile bkt

aws s3api list-object-versions --bucket my-bucket --endpoint-url $BKT --profile bkt
aws s3api get-object --bucket my-bucket --key doc.txt --version-id <id> out.txt \
  --endpoint-url $BKT --profile bkt

# Plain delete → delete marker (object hidden, data kept)
aws s3api delete-object --bucket my-bucket --key doc.txt --endpoint-url $BKT --profile bkt
# Delete the MARKER by its version id → the object comes back
# Delete a specific version id → permanent removal of that version
```

Semantics match AWS: deleting the current version by id promotes the
next-newest; deleting the newest delete marker resurrects the object.
Version ids are UUIDs; objects written before versioning report id `null`.

**Deviations from AWS (by design):**
- *Suspended* stops creating new versions; existing history stays browsable
  (no AWS null-version overwrite behavior).
- Console **move/rename** relocate an object together with its identity — no
  versions or markers are recorded for a move.
- Version bytes live in hidden storage (`.versions/` locally, a
  `.bkt-versions/` prefix inside the real bucket on the S3 backend) and never
  appear in listings.

## Lifecycle expiry

One rule per bucket: expire current objects after N days (optionally under a
key prefix), and/or permanently remove noncurrent versions after M days.
Expiry of current objects goes through the versioned path, so on a versioned
bucket it produces delete markers (recoverable until the noncurrent expiry
removes them). The sweep runs hourly, plus once shortly after startup.

**Console**: bucket Settings → Lifecycle.
**S3 API**: `put-bucket-lifecycle-configuration` /
`get-bucket-lifecycle-configuration` / `delete-bucket-lifecycle` with the AWS
XML subset (`Expiration.Days`, `Filter.Prefix`,
`NoncurrentVersionExpiration.NoncurrentDays`). Multiple enabled rules return
`NotImplemented`.

## Storage quotas

`quota_bytes` caps the total size of a bucket's **current** objects (version
storage is not counted). Writes that would exceed it are rejected with
`QuotaExceeded` before any bytes are stored. 0 = unlimited.

**Console**: bucket Settings → Quota.
**API**: `PUT /api/buckets/{name}/settings` `{"quota_bytes": 1073741824}`.

## Retention (WORM)

`retention_days` makes a versioned bucket write-once-ish: while any object or
version is younger than the window, version-addressed deletions are refused,
lifecycle purges skip it, versioning cannot be suspended, and the bucket
cannot be deleted. Plain deletes still create markers — data is preserved,
only hidden. Requires versioning to be enabled first; 0 turns it off.

This is a bucket-level setting, not the AWS Object Lock API — S3
`x-amz-object-lock-*` headers are not implemented.

## Presigned share links

Generate a time-limited download URL for any object — the link works without
authentication until it expires.

**Console**: the Share action on a file row (expiry 15 min–7 days, copy button).
**API**: `POST /api/buckets/{name}/objects/presign`
`{"key": "path/file.txt", "expires_in": 3600}`.

Links are signed with one of your own access keys, so you need at least one
active key, and a link can never outlive the key that signed it. Client-side
presigning (`aws s3 presign`) also works and is verified by the same SigV4
checker. If the S3 API is reached through a proxy or its own hostname, set
`S3_PUBLIC_ENDPOINT` so generated links carry the right host.

## User metadata and tags

`x-amz-meta-*` headers on upload (or multipart initiate) are persisted,
echoed on GET/HEAD, and — on the external S3 backend — stored on the real S3
object too. 2KB total limit. `CopyObject` honors
`x-amz-metadata-directive: COPY|REPLACE`.

Object tags: `x-amz-tagging: k=v&k2=v2` on upload, or the `?tagging`
subresource (`get-object-tagging` / `put-object-tagging` /
`delete-object-tagging`). Limits: 10 tags, key ≤ 128 chars, value ≤ 256.

## Event notifications (webhooks)

Per bucket: an HTTP(S) URL receives a JSON POST for `object:created` and/or
`object:removed` events.

```json
{"event":"object:created","bucket":"my-bucket","key":"a.txt",
 "size":123,"etag":"…","version_id":"…","timestamp":"…"}
```

With a webhook secret set, the raw body is signed:
`X-Bkt-Signature: sha256=<hex HMAC-SHA256>`. Delivery is asynchronous
(queued, 3 retries with backoff) and never blocks uploads; persistent
failures are logged, not queued forever. Configure in bucket Settings →
Notifications, or via `PUT /api/buckets/{name}/settings`.

## Groups

Groups attach policies to many users at once: a user's effective permissions
are their directly-attached policies **plus** the policies of every group they
belong to. Manage groups in Admin → Groups (create, membership, attach/detach
policies) or via `/api/groups` (admin only).

## Temporary credentials (bkt-STS)

`POST /api/sts/credentials` (or Profile → Temporary credentials) mints a
short-lived S3 key pair — default 1 hour, max 12, optionally read-only. Temp
keys don't count against the 5-key limit, don't appear in your key list, and
are deleted automatically after expiry. The secret is shown once.

> bkt-STS is a simple REST endpoint, **not** an AWS STS (`AssumeRole`)
> compatible API.

## Replication (bucket mirroring)

`replicate_to` mirrors a bucket's current objects one-way into another bkt
bucket: a periodic sync (every 5 minutes) copies new/changed objects and
mirrors deletions. The target is managed by replication — treat it as
read-only. Guards prevent self-targets, cycles, and two sources sharing a
target.

For cross-region or cross-provider DR, back the *target* bucket with a
different S3 configuration — the mirror then lands on that provider.
Configure in bucket Settings → Replication.

## Server-side encryption

- **External S3 backend**: set `S3_SSE=true` and every object bkt writes to
  the backing S3 carries SSE-S3 (AES256).
- **Local backend**: bkt does not encrypt object bytes at rest — use
  disk-level encryption (LUKS/dm-crypt) on the storage volume. (Streaming
  application-level encryption that preserves HTTP Range requests is a
  substantial project and is deliberately not half-implemented.)
- Stored S3 *credentials* are always encrypted with `ENCRYPTION_KEY`,
  independent of the above.
