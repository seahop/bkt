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
- [Object keys and uploads](#object-keys-and-uploads)

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

Changing versioning requires admin or the `s3:PutBucketVersioning` policy
action; bucket settings in general are authorized by policy, not by bucket
ownership (see [bucket-configuration actions](../api/policies.md#bucket-configuration-actions)).

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
`NotImplemented`. Requires admin or `s3:PutLifecycleConfiguration`
(`s3:GetLifecycleConfiguration` to read it).

Noncurrent expiry never removes a key's *latest* delete marker while older
versions of that key remain (it would resurrect the object); the marker is
removed only once it is the key's sole remaining version (AWS
`ExpiredObjectDeleteMarker` behavior). Retained versions are skipped.

## Storage quotas

`quota_bytes` caps the total size of a bucket's **current** objects (version
storage is not counted). Writes that would exceed it are rejected with
`QuotaExceeded` before any bytes are stored. 0 = unlimited. The quota applies
to every write path — PUT, copy, multipart completion (sum of the parts),
console and async uploads — and chunked uploads are measured on the bytes
actually received, not the declared size. Concurrent writers are accounted
together within one bkt process; with several replicas the quota can be
overshot by at most the uploads in flight.

**Console**: bucket Settings → Quota.
**API**: `PUT /api/buckets/{name}/settings` `{"quota_bytes": 1073741824}`
(admin or `s3:PutBucketQuota`).

## Retention (WORM)

`retention_days` makes a versioned bucket write-once-ish: while any object or
version is younger than the window, version-addressed deletions are refused,
lifecycle purges skip it, versioning cannot be suspended, and the bucket
cannot be deleted. Plain deletes still create markers — data is preserved,
only hidden. Requires versioning to be enabled first.

The window can be **raised at any time**, but **lowered or cleared (0) only
once no object or version is still inside the current window** — otherwise
the request fails with 409. So retention cannot be switched off to purge
data it protects; wait until the newest data has aged out. Requires admin or
`s3:PutBucketObjectLockConfiguration`.

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
`X-Bkt-Signature: sha256=<hex HMAC-SHA256>`. The secret is stored encrypted
(same scheme as S3 credentials). Configure in bucket Settings →
Notifications, or via `PUT /api/buckets/{name}/settings` (admin or
`s3:PutBucketNotification`).

Delivery is asynchronous and never blocks uploads:

- Each destination (`scheme://host:port`) has its own queue and at most 2
  concurrent deliveries, so a slow or unresponsive receiver only delays its
  own events.
- Up to 3 attempts with backoff, bounded to ~20 s per event in total (8 s per
  attempt). 4xx responses (other than 408/429) are not retried. Redirects are
  not followed — a 3xx is a failed delivery.
- Per bucket, at most 512 events may be pending and events are rate-limited
  (50/s sustained, bursts up to 1000). Events beyond these limits — e.g. from
  moving a folder with tens of thousands of objects — are **dropped** and
  counted; drops are logged once when they start and summarized every minute.
  A single folder move additionally emits at most 1,000 events (the first
  500 moved objects) and logs how many it skipped.
- Persistent failures are logged (with the URL reduced to scheme and host —
  paths and queries often carry tokens), not queued forever.

**Allowed destinations**: webhook URLs must be `http(s)` URLs without
embedded credentials, and deliveries only reach public addresses — loopback,
private, link-local (including the cloud metadata endpoint), CGNAT, multicast
and reserved ranges are refused both when the URL is saved (for literal IPs
and `localhost`) and on every connection (for the address a hostname actually
resolves to). To deliver to an internal receiver, list it in
`WEBHOOK_ALLOWED_HOSTS` (see [configuration](../deployment/configuration.md#webhooks)).

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
read-only. Guards prevent self-targets, cycles (including longer chains such
as A→B→C→A), and two sources sharing a target.

Configuring it requires admin, or `s3:PutReplicationConfiguration` on the
source **plus** `s3:GetObject` on all source objects and `s3:PutObject` +
`s3:DeleteObject` on all target objects — you can only mirror data you could
copy yourself into a bucket you could overwrite yourself.

Replication then **runs as the user who configured it, with that user's
current permissions, checked per object** on every sync: an object is copied
only if they may `s3:GetObject` it in the source and `s3:PutObject` it in the
target, and a target object is removed only if they may `s3:DeleteObject` it.
So a narrower Deny (e.g. on `source/secret/*`) or a permission revoked later
is honored — denied objects are skipped and counted in the log. If that user
is deleted or locked, replication is disabled for the bucket. Configurations
saved before this was recorded run as the bucket owner while the owner is an
admin, and are otherwise paused (with a warning) until re-saved.

The target is pinned by identity when replication is configured: if the
target bucket is deleted — even if a new bucket is later created under the
same name — replication is disabled instead of writing into the new bucket.

Syncs (and lifecycle sweeps) never wait long on objects that are busy (e.g.
a large upload in progress): such objects are skipped and retried on the
next run.

Safety on the target: when the target has versioning enabled, replication
archives the target's current version before overwriting it and mirrors
deletions as delete markers, so target history is never destroyed — a
retention (WORM) target keeps every retained version (a good pattern for an
immutable backup copy). The target's quota applies to replicated copies
(objects that would exceed it are skipped and logged). On an unversioned
target, overwrites and deletions are permanent, as with any mirror.

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

## Object keys and uploads

- On every bucket, `..`, a leading `/`, backslashes and NUL bytes are
  rejected, and the `.bkt-versions/` prefix is reserved for bkt's version
  storage.
- On **local**-backend buckets keys must also be canonical paths: empty
  segments (`a//b`) and `.` segments (`./x`, `a/./b`) are rejected, because
  the filesystem would alias them onto another key. On **S3**-backed buckets
  those are distinct, valid S3 keys and are accepted.
- S3 folder-marker objects (keys ending in `/`, created by s3fs `mkdir`,
  Cyberduck/rclone "new folder" or `aws s3api put-object --key dir/`) work on
  both backends. The local backend stores `dir/` as the file
  `dir/.bkt-folder` inside the folder, so the marker and the folder's
  contents (`dir/file.txt`) coexist; deleting `dir/` removes only the marker.
  The name `.bkt-folder` is therefore reserved as a key segment on local
  buckets. Folder markers created by earlier releases (stored as an empty
  file `dir`) stay readable and deletable, and re-creating the marker
  upgrades them. A local bucket still cannot hold both an object `dir` and a
  folder `dir/` (a file and a directory of the same name).
- `aws-chunked` uploads must send `X-Amz-Decoded-Content-Length` (as on AWS);
  the decoded length is enforced exactly. Signed streaming uploads
  (`STREAMING-AWS4-HMAC-SHA256-PAYLOAD[-TRAILER]`) have every chunk signature
  verified and are refused on presigned requests; `STREAMING-UNSIGNED-PAYLOAD-TRAILER`
  (aws-cli v2 over TLS) is supported. A body that fails verification is never
  stored.
- Moving or renaming objects and folders requires `s3:GetObject` +
  `s3:DeleteObject` on every source key and `s3:PutObject` on every
  destination key (bucket ownership grants nothing), never overwrites an
  existing destination, and in versioned buckets leaves a delete marker at the
  source.
- Non-admin bucket listings omit the owner's account details and the storage
  configuration; the webhook URL and replication target are shown only to
  callers allowed to change them.
