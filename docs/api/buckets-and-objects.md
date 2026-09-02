# Buckets and Objects API

Complete reference for bucket and object management endpoints.

## Buckets API

Base URL: `https://localhost:9443/api/buckets`

### Create Bucket
**Request Body:**
```json
{
  "name": "my-bucket",           // Required, 3-63 chars, alphanumeric + hyphens
  "is_public": false,            // Optional, default: false
  "region": "us-east-1",         // Optional, default: us-east-1
  "storage_backend": "local",
  "storage_backend": "local"     // Optional, "local" or "s3", default: local
}
```

**Success Response (201 Created):**
```json
{
  "id": "uuid",
  "name": "my-bucket",
  "owner_id": "uuid",
  "is_public": false,
  "region": "us-east-1",
  "storage_backend": "local",
  "storage_backend": "local",
  "created_at": "timestamp",
  "updated_at": "timestamp"
}
```

Create a new storage bucket.

**Endpoint:** `POST /buckets`

**Authentication:** Required

```json
{
  "id": "uuid",
  "name": "my-bucket",
  "owner_id": "uuid",
  "is_public": false,
  "region": "us-east-1",
  "storage_backend": "local",
  "created_at": "timestamp",
  "updated_at": "timestamp"
}
```

**Error Responses:**
- `400 Bad Request` - Invalid bucket name
- `409 Conflict` - Bucket already exists

---

### List Buckets

List all buckets accessible to the user.

**Endpoint:** `GET /buckets`

**Authentication:** Required

**Authorization:**
- Regular users see only their own buckets
- Admins see all buckets

**Success Response (200 OK):**
```json
[
  {
    "id": "uuid",
    "name": "my-bucket",
    "owner_id": "uuid",
    "is_public": false,
    "region": "us-east-1",
  "storage_backend": "local",
    "created_at": "timestamp",
    "updated_at": "timestamp",
    "owner": {
      "id": "uuid",
      "username": "user",
      "email": "user@example.com"
    }
  }
]
```

---

### Get Bucket

Get details of a specific bucket.

**Endpoint:** `GET /buckets/:name`

**Authentication:** Required

**Authorization:**
- Users can only access their own buckets
- Admins can access any bucket

**Success Response (200 OK):**
```json
{
  "id": "uuid",
  "name": "my-bucket",
  "owner_id": "uuid",
  "is_public": false,
  "region": "us-east-1",
  "storage_backend": "local",
  "created_at": "timestamp",
  "updated_at": "timestamp",
  "owner": {
    "id": "uuid",
    "username": "user",
    "email": "user@example.com"
  }
}
```

**Error Responses:**
- `403 Forbidden` - Access denied
- `404 Not Found` - Bucket not found

---

### Delete Bucket

Delete an empty bucket.

**Endpoint:** `DELETE /buckets/:name`

**Authentication:** Required

**Authorization:**
- Users can only delete their own buckets
- Admins can delete any bucket

**Success Response (200 OK):**
```json
{
  "message": "Bucket deleted successfully"
}
```

**Error Responses:**
- `403 Forbidden` - Access denied
- `404 Not Found` - Bucket not found
- `409 Conflict` - Bucket not empty

---

## Objects API

Base URL: `https://localhost:9443/api/buckets/:name/objects`

### Upload Object

Upload a file to a bucket.

**Endpoint:** `POST /buckets/:name/objects`

**Authentication:** Required

**Authorization:** Bucket owner or admin

**Request:**
- Content-Type: `multipart/form-data`
- Form fields:
  - `key` (string) - Object key/path
  - `file` (binary) - File data

**Example:**
```bash
curl -k -X POST https://localhost:9443/api/buckets/my-bucket/objects \
  -H "Authorization: Bearer $TOKEN" \
  -F "key=documents/report.pdf" \
  -F "file=@/path/to/report.pdf"
```

**Success Response (200 OK):**
```json
{
  "message": "Object uploaded successfully",
  "bucket": "my-bucket",
  "key": "documents/report.pdf",
  "size": 1048576,
  "etag": "d41d8cd98f00b204e9800998ecf8427e",
  "content_type": "application/pdf"
}
```

**Error Responses:**
- `400 Bad Request` - Missing key or file
- `403 Forbidden` - Access denied
- `404 Not Found` - Bucket not found
- `413 Payload Too Large` - File exceeds size limit

---

### List Objects

List objects in a bucket.

**Endpoint:** `GET /buckets/:name/objects`

**Authentication:** Required

**Authorization:** Bucket owner, admin, or public bucket

**Query Parameters:**
- `prefix` (string) - Filter objects by prefix
- `max-keys` (integer) - Maximum objects to return (1-1000, default: 1000)
- `continuation-token` (string) - Resume from a previous page's `next_continuation_token`

**Example:**
```bash
# List all objects
curl -k -X GET https://localhost:9443/api/buckets/my-bucket/objects \
  -H "Authorization: Bearer $TOKEN"

# List with prefix
curl -k -X GET "https://localhost:9443/api/buckets/my-bucket/objects?prefix=documents/" \
  -H "Authorization: Bearer $TOKEN"

# Limit results
curl -k -X GET "https://localhost:9443/api/buckets/my-bucket/objects?max-keys=100" \
  -H "Authorization: Bearer $TOKEN"
```

**Success Response (200 OK):**
```json
{
  "bucket": "my-bucket",
  "count": 2,
  "objects": [
    {
      "id": "uuid",
      "bucket_id": "uuid",
      "key": "documents/report.pdf",
      "size": 1048576,
      "content_type": "application/pdf",
      "etag": "d41d8cd98f00b204e9800998ecf8427e",
      "sha256": "abc123...",
      "created_at": "timestamp",
      "updated_at": "timestamp"
    }
  ],
  "is_truncated": false,
  "next_continuation_token": ""
}
```

**Pagination:** when `is_truncated` is `true`, pass `next_continuation_token` back as `continuation-token` to fetch the next page:

```bash
curl -k -X GET "https://localhost:9443/api/buckets/my-bucket/objects?max-keys=100&continuation-token=documents/report.pdf" \
  -H "Authorization: Bearer $TOKEN"
```

---

### Download Object

Download an object from a bucket.

**Endpoint:** `GET /buckets/:name/objects/*key`

**Authentication:** Required

**Authorization:** Bucket owner, admin, or public bucket

**Query Parameters:**
- `download=true` - Force download (sets Content-Disposition: attachment)

**Headers:**
```
Authorization: Bearer {token}
```

**Success Response (200 OK):**
- Headers:
  - `Content-Type`: Object's content type
  - `Content-Length`: Object size
  - `ETag`: MD5 hash
  - `Last-Modified`: Last update timestamp
  - `Accept-Ranges`: bytes
  - `Content-Disposition`: inline or attachment
- Body: File binary data

**Example:**
```bash
# Download to file
curl -k -X GET https://localhost:9443/api/buckets/my-bucket/objects/file.txt \
  -H "Authorization: Bearer $TOKEN" \
  -o downloaded.txt

# Force download with attachment header
curl -k -X GET "https://localhost:9443/api/buckets/my-bucket/objects/file.txt?download=true" \
  -H "Authorization: Bearer $TOKEN" \
  -O -J
```

**Error Responses:**
- `403 Forbidden` - Access denied
- `404 Not Found` - Bucket or object not found

---

### Get Object Metadata

Get object metadata without downloading the file (HEAD request).

**Endpoint:** `HEAD /buckets/:name/objects/*key`

**Authentication:** Required

**Authorization:** Bucket owner, admin, or public bucket

**Success Response (200 OK):**
- Headers only (no body):
  - `Content-Type`: Object's content type
  - `Content-Length`: Object size
  - `ETag`: MD5 hash
  - `Last-Modified`: Last update timestamp
  - `Accept-Ranges`: bytes

**Example:**
```bash
curl -k -X HEAD https://localhost:9443/api/buckets/my-bucket/objects/file.txt \
  -H "Authorization: Bearer $TOKEN" \
  -I
```

**Error Responses:**
- `403 Forbidden` - Access denied
- `404 Not Found` - Bucket or object not found

---

### Delete Object

Delete an object from a bucket.

**Endpoint:** `DELETE /buckets/:name/objects/*key`

**Authentication:** Required

**Authorization:** Bucket owner or admin

On a **versioning-enabled** bucket, this creates a **delete marker**: the object disappears from listings but its data is kept and stays recoverable via the version history (see [Object Versioning](#object-versioning) below).

**Success Response (200 OK):**
```json
{
  "message": "Object deleted successfully"
}
```

**Example:**
```bash
curl -k -X DELETE https://localhost:9443/api/buckets/my-bucket/objects/old-file.txt \
  -H "Authorization: Bearer $TOKEN"
```

**Error Responses:**
- `403 Forbidden` - Access denied
- `404 Not Found` - Bucket or object not found

---

### Presigned Download URLs

Generate a time-limited, shareable download URL for an object. The URL is signed (SigV4) with one of your **active access keys** and points at the S3 API listener, so it works without a JWT — anyone with the link can download until it expires.

**Endpoint:** `POST /buckets/:name/objects/presign`

**Authentication:** Required (and at least one active access key — generate one in Profile first)

**Request Body:**
```json
{
  "key": "documents/report.pdf",
  "expires_in": 3600
}
```

- `key` (string, required) - Object key
- `expires_in` (integer, optional) - Lifetime in seconds. Default 900 (15 min); minimum 60; maximum 604800 (7 days)

**Success Response (200 OK):**
```json
{
  "url": "https://host:9000/my-bucket/documents/report.pdf?X-Amz-Algorithm=AWS4-HMAC-SHA256&...",
  "expires_at": "2026-09-02T13:00:00Z",
  "capped_by_key": false,
  "signing_key_name": "my-key"
}
```

If the signing key expires before the requested lifetime, the URL's effective expiry is capped by the key's expiry and `capped_by_key` is `true`.

The URL's host comes from `S3_PUBLIC_ENDPOINT` when set, otherwise it is derived from the console request host with the S3 API port swapped in.

**Error Responses:**
- `403 Forbidden` - No read permission on the object
- `404 Not Found` - Bucket or object not found
- `409 Conflict` - No active (or no sufficiently long-lived) access key

---

## Object Versioning

Versioning keeps a history of every overwrite and delete so objects can be inspected and restored.

### Semantics

- **Enable/suspend** versioning per bucket (owner or admin). `suspended` means "stop creating new versions" — existing history stays browsable.
- **Overwrites** archive the previous current version.
- **Plain deletes** on a versioning-enabled bucket create a **delete marker**: the object is hidden but its data is kept.
- **Deleting a delete marker** (by version id) resurrects the object.
- **Deleting the current version** by id **promotes** the next-newest surviving version to current.
- **Restore** copies an archived version forward as the new current version — history is preserved (the previous current is archived first).
- Version IDs are UUIDs; objects written before versioning was enabled report version id `"null"`.
- Console move/rename relocate an object with its identity — no versions or markers are recorded.
- Version bytes live in hidden storage (local backend: outside the bucket directories; S3 backend: a `.bkt-versions/` prefix inside the real bucket, excluded from listings).
- Bucket quotas count **current** objects only, not version storage.

### Set Bucket Versioning

**Endpoint:** `PUT /buckets/:name/versioning`

**Authorization:** Bucket owner or admin

**Request Body:**
```json
{ "versioning": "enabled" }
```

`versioning` must be `"enabled"` or `"suspended"`.

**Success Response (200 OK):** `{"message": "Versioning enabled"}`

**Error Responses:**
- `400 Bad Request` - Invalid value
- `403 Forbidden` - Not the bucket owner or an admin
- `409 Conflict` - Cannot suspend while a retention period is set (clear retention first)

### List Object Versions

**Endpoint:** `GET /buckets/:name/object-versions?key=K`

> **Note the path:** it is `/object-versions`, **not** `/objects/versions` — a static segment under `/objects/` would conflict with the download wildcard route.

**Success Response (200 OK):**
```json
{
  "bucket": "my-bucket",
  "key": "documents/report.pdf",
  "versioning": "enabled",
  "versions": [
    {
      "version_id": "8b7a...uuid",
      "is_latest": true,
      "is_delete_marker": false,
      "size": 1048576,
      "content_type": "application/pdf",
      "etag": "d41d8cd98f00b204e9800998ecf8427e",
      "last_modified": "timestamp"
    },
    {
      "version_id": "31fc...uuid",
      "is_latest": false,
      "is_delete_marker": false,
      "size": 1040001,
      "content_type": "application/pdf",
      "etag": "aa11bb22...",
      "last_modified": "timestamp"
    }
  ]
}
```

### Restore a Version

Copies an archived version forward as the new current version. Delete markers cannot be restored.

**Endpoint:** `POST /buckets/:name/objects/restore`

**Request Body:**
```json
{
  "key": "documents/report.pdf",
  "version_id": "31fc...uuid"
}
```

**Success Response (200 OK):** `{"message": "Version restored"}`

### Permanently Delete a Version

Removes one version (or delete marker) for good. Removing the current version or a latest delete marker promotes the next-newest surviving version.

**Endpoint:** `DELETE /buckets/:name/object-versions?key=K&version_id=V`

**Success Response (200 OK):** `{"message": "Version deleted"}`

> Version-addressed deletes are refused while the bucket's retention window covers the version (see Bucket Settings below).

---

## Bucket Settings

One endpoint updates quota, retention, webhooks, and replication. Only fields present in the request body are changed.

**Endpoint:** `PUT /buckets/:name/settings`

**Authorization:** Bucket owner or admin

**Request Body (all fields optional):**
```json
{
  "quota_bytes": 10737418240,
  "retention_days": 30,
  "webhook_url": "https://example.com/hooks/bkt",
  "webhook_secret": "s3cret",
  "webhook_events": "created,removed",
  "replicate_to": "my-bucket-mirror"
}
```

**Success Response (200 OK):** `{"message": "Settings updated"}`

### Quota (`quota_bytes`)

- `0` = unlimited.
- Counts **current** objects only (not version storage).
- Writes that would exceed the quota fail — the S3 API returns `QuotaExceeded` (403).

### Retention (`retention_days`, WORM-lite)

- **Requires versioning enabled** on the bucket; `0` = off.
- While any object/version is younger than the window:
  - version-addressed deletes are refused,
  - lifecycle purges skip it,
  - versioning cannot be suspended,
  - the bucket cannot be deleted.
- Plain deletes still create delete markers (data is retained).

### Webhooks (`webhook_url`, `webhook_secret`, `webhook_events`)

- POSTs JSON to `webhook_url` on `object:created` / `object:removed` events.
- `webhook_events` is a CSV filter of `created`,`removed`; empty = both.
- When `webhook_secret` is set, the raw body is HMAC-SHA256 signed in `X-Bkt-Signature: sha256=<hex>`.
- Delivery is a background queue with 3 retries and backoff — it never blocks uploads; failures are logged, not stored.

**Payload:**
```json
{
  "event": "object:created",
  "bucket": "my-bucket",
  "key": "documents/report.pdf",
  "size": 1048576,
  "etag": "d41d8cd98f00b204e9800998ecf8427e",
  "version_id": "uuid",
  "timestamp": "2026-09-02T12:00:00Z"
}
```

### Replication (`replicate_to`)

- One-way mirror into **another bkt bucket on the same instance** (`""` disables).
- Periodic sync every 5 minutes (plus shortly after startup): copies new/changed objects (compared by ETag) and mirrors deletions (via the versioned path if the target has versioning).
- Guards: no self-target, no two-bucket cycles, and a target can have only one source. The target bucket is managed by replication.
- For cross-region/provider DR, point the target bucket at a different S3 configuration.

**Error Responses:**
- `400 Bad Request` - Negative values, non-http(s) webhook URL, retention without versioning, self-replication, replication cycle, or empty body
- `403 Forbidden` - Not the bucket owner or an admin
- `404 Not Found` - Replication target bucket not found
- `409 Conflict` - Target already mirrored by another bucket

---

## Bucket Lifecycle

Age-based expiry, one rule per bucket. Setting both day counts to zero clears the configuration. Rules run hourly (and once shortly after startup).

**Endpoint:** `PUT /buckets/:name/lifecycle`

**Authorization:** Bucket owner or admin

**Request Body:**
```json
{
  "expire_days": 90,
  "prefix": "logs/",
  "noncurrent_expire_days": 30
}
```

- `expire_days` - Expire current objects older than N days (on versioned buckets this creates delete markers)
- `prefix` (optional) - Only apply to keys with this prefix
- `noncurrent_expire_days` - **Permanently** remove noncurrent versions older than N days

**Success Response (200 OK):** `{"message": "Lifecycle updated"}`

The same configuration is also manageable through the S3 API's `?lifecycle` subresource (AWS XML subset, single rule).

---

## Object Metadata

Objects store the following metadata:

- **ID:** Unique identifier (UUID)
- **Bucket ID:** Parent bucket UUID
- **Key:** Object path/name
- **Size:** File size in bytes
- **Content Type:** MIME type
- **ETag:** MD5 hash (for caching/validation)
- **SHA256:** SHA-256 hash (for integrity verification)
- **Storage Path:** Internal file system path (not exposed)
- **Created At:** Upload timestamp
- **Updated At:** Last modification timestamp

## Best Practices

### Bucket Naming

- Use lowercase letters, numbers, and hyphens only
- Start and end with alphanumeric characters
- 3-63 characters long
- Avoid dots (.) for SSL compatibility
- Make names descriptive and meaningful

**Good Names:**
- `user-documents`
- `app-backups-2025`
- `public-assets`

**Bad Names:**
- `my.bucket.name` (dots cause SSL issues)
- `A` (too short)
- `very-long-bucket-name-that-exceeds-the-maximum-length-limit` (too long)

### Object Keys

- Use forward slashes (/) for hierarchy
- Avoid leading slashes
- Use descriptive names
- Include file extensions
- No need to encode versions in key names — enable [bucket versioning](#object-versioning) instead

**Good Keys:**
- `documents/reports/2025/Q1-report.pdf`
- `images/products/product-123.jpg`
- `backups/database-20251208.sql.gz`

**Bad Keys:**
- `/documents/file.txt` (leading slash)
- `img` (no extension)
- `file1`, `file2` (not descriptive)

### Public vs Private Buckets

**Public Buckets:**
- ✅ Good for static assets
- ✅ Good for shared files
- ❌ Bad for sensitive data
- ❌ Bad for user data

**Private Buckets:**
- ✅ Good for user data
- ✅ Good for sensitive files
- ✅ Good for backups
- ✅ Fine-grained access control via policies

### Performance

- **Large Files:** Use chunked uploads for files > 100MB
- **Many Files:** Batch operations when possible
- **Caching:** Use ETags for cache validation
- **CDN:** Consider CDN for public content

### Security

- **Access Control:** Use policies for fine-grained control
- **Encryption:** Data encrypted in transit (TLS)
- **Integrity:** Verify SHA256 hashes
- **Audit:** Monitor access logs

## Related Documentation

- [Authentication API](authentication.md)
- [Access Keys API](access-keys.md)
- [Policies API](policies.md)
- [cURL Examples](../examples/curl-examples.md)

## Folder Organization

While the object storage system doesn't have true folders (like a traditional file system), it supports **virtual folders** through object key prefixes.

### How Folders Work

Folders are represented using forward slashes (`/`) in object keys:
- `documents/report.pdf` - "documents" is a folder
- `images/2025/photo.jpg` - "images/2025" is a nested folder structure
- `data/backups/database.sql.gz` - "data/backups" is a two-level folder

### Creating Folders

There are two ways to create folders:

1. **Implicit Creation** - Upload an object with a folder prefix:
```bash
curl -k -X POST https://localhost:9443/api/buckets/my-bucket/objects \
  -H "Authorization: Bearer $TOKEN" \
  -F "key=documents/report.pdf" \
  -F "file=@report.pdf"
```

2. **Explicit Creation** - Create a `.keep` marker file:
```bash
curl -k -X POST https://localhost:9443/api/buckets/my-bucket/objects \
  -H "Authorization: Bearer $TOKEN" \
  -F "key=empty-folder/.keep" \
  -F "file=@/dev/null"
```

### Listing Objects in a Folder

Use the `prefix` query parameter to list objects in a specific folder:

```bash
# List all objects in the "documents" folder
curl -k -X GET "https://localhost:9443/api/buckets/my-bucket/objects?prefix=documents/" \
  -H "Authorization: Bearer $TOKEN"

# List nested folder
curl -k -X GET "https://localhost:9443/api/buckets/my-bucket/objects?prefix=images/2025/" \
  -H "Authorization: Bearer $TOKEN"
```

### Web Interface

The web interface (the console at `https://localhost:9443`, or `https://localhost:5173` with the dev compose) provides a user-friendly folder browser with:
- Click-to-navigate folder structure
- Breadcrumb navigation
- Create Folder button
- Upload files directly to folders
- Automatic folder parsing from object keys

### Folder Best Practices

1. **Consistent Naming**: Use lowercase with hyphens (`my-folder/`)
2. **Logical Hierarchy**: Organize by date, category, or function
3. **Avoid Deep Nesting**: Keep folder depth reasonable (3-4 levels max)
4. **Trailing Slashes**: Folder prefixes typically end with `/`

**Examples of Good Folder Structure:**
```
documents/
  ├── reports/
  │   ├── 2025-Q1.pdf
  │   └── 2025-Q2.pdf
  └── invoices/
      ├── january/
      └── february/

images/
  ├── products/
  └── banners/

backups/
  ├── daily/
  └── weekly/
```

## Storage Backend Selection

### Per-Bucket Storage Backends

Each bucket can independently choose its storage backend:

- **Local Storage** (`"local"`):
  - Files stored on the server's filesystem
  - Fast access for frequently used files
  - Good for development and small deployments
  - Path: `./data/buckets/`

- **S3 Storage** (`"s3"`):
  - Files stored in S3-compatible object storage
  - Scalable for large deployments
  - Geographic redundancy
  - Requires S3 configuration in `.env`

### Choosing a Storage Backend

Specify the backend when creating a bucket:

```bash
# Local storage (default)
curl -k -X POST https://localhost:9443/api/buckets \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "local-bucket",
    "storage_backend": "local"
  }'

# S3 storage
curl -k -X POST https://localhost:9443/api/buckets \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "s3-bucket",
    "storage_backend": "s3"
  }'
```

### Storage Backend Considerations

**Use Local Storage When:**
- Development and testing
- Small to medium deployments
- Predictable storage requirements
- Fast local access is priority

**Use S3 Storage When:**
- Production deployments
- Large-scale storage needs
- Geographic redundancy required
- Cost-effective scaling needed
- Integration with AWS ecosystem

### Mixed Storage Strategy

You can use different backends for different buckets within the same system:

```bash
# Frequently accessed data → local
curl -k -X POST https://localhost:9443/api/buckets \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name": "hot-data", "storage_backend": "local"}'

# Archival data → S3
curl -k -X POST https://localhost:9443/api/buckets \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name": "cold-archive", "storage_backend": "s3"}'
```

