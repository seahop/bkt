# bkt API Documentation

Complete API reference for bkt.

## Quick Reference

### Public Endpoints (No Authentication)

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/auth/register` | Register new account |
| POST | `/api/auth/login` | Login |
| POST | `/api/auth/refresh` | Refresh token |
| GET | `/api/auth/sso/config` | Get SSO configuration |
| GET | `/api/auth/google/login` | Initiate Google OAuth |
| GET | `/api/auth/google/callback` | Google OAuth callback |
| POST | `/api/auth/vault/login` | Vault JWT login |

### User Endpoints (Authentication Required)

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/auth/logout` | Logout |
| GET | `/api/users/me` | Get current user |
| PUT | `/api/users/me` | Update current user |
| GET | `/api/access-keys` | List access keys |
| POST | `/api/access-keys` | Create access key |
| DELETE | `/api/access-keys/:id` | Revoke access key |
| GET | `/api/access-keys/stats` | Get key stats |
| GET | `/api/buckets` | List buckets |
| GET | `/api/buckets/:name` | Get bucket |
| GET | `/api/buckets/:name/policy` | Get bucket policy |
| GET | `/api/buckets/:name/objects` | List objects |
| POST | `/api/buckets/:name/objects` | Upload object |
| POST | `/api/buckets/:name/objects/async` | Upload async |
| GET | `/api/buckets/:name/objects/*key` | Download object |
| HEAD | `/api/buckets/:name/objects/*key` | Head object |
| DELETE | `/api/buckets/:name/objects/*key` | Delete object |
| POST | `/api/buckets/:name/objects/move` | Move object |
| POST | `/api/buckets/:name/objects/rename` | Rename object |
| POST | `/api/buckets/:name/folders/move` | Move folder |
| GET | `/api/uploads` | List uploads |
| GET | `/api/uploads/:id/status` | Get upload status |
| GET | `/api/policies` | List policies |

### Admin Endpoints (Admin Required)

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/users` | List all users |
| POST | `/api/users` | Create user |
| DELETE | `/api/users/:id` | Delete user |
| POST | `/api/users/:id/lock` | Lock user |
| POST | `/api/users/:id/unlock` | Unlock user |
| GET | `/api/users/:id/access-keys` | List user's keys |
| DELETE | `/api/users/:id/access-keys/:key_id` | Delete user's key |
| POST | `/api/buckets` | Create bucket |
| DELETE | `/api/buckets/:name` | Delete bucket |
| PUT | `/api/buckets/:name/policy` | Set bucket policy |
| POST | `/api/policies` | Create policy |
| GET | `/api/policies/:id` | Get policy |
| PUT | `/api/policies/:id` | Update policy |
| DELETE | `/api/policies/:id` | Delete policy |
| POST | `/api/policies/users/:user_id/attach` | Attach policy |
| DELETE | `/api/policies/users/:user_id/detach/:policy_id` | Detach policy |
| GET | `/api/s3-configs` | List S3 configs |
| POST | `/api/s3-configs` | Create S3 config |
| GET | `/api/s3-configs/:id` | Get S3 config |
| PUT | `/api/s3-configs/:id` | Update S3 config |
| DELETE | `/api/s3-configs/:id` | Delete S3 config |

### S3-Compatible API (Access Key Auth)

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/` | List buckets |
| HEAD | `/:bucket` | Head bucket |
| GET | `/:bucket` | List objects |
| PUT | `/:bucket` | Create bucket (disabled) |
| HEAD | `/:bucket/*key` | Head object |
| GET | `/:bucket/*key` | Get object |
| PUT | `/:bucket/*key` | Put object |
| DELETE | `/:bucket/*key` | Delete object |

---

## Base URL

All API endpoints are prefixed with `/api` except for the S3-compatible API which uses the root path.

## Authentication

All protected endpoints require a JWT token in the Authorization header:

```
Authorization: Bearer <token>
```

Authentication endpoints are rate-limited to **5 requests per minute per IP**.

---

## Authentication Endpoints

<details>
<summary><code>POST /api/auth/register</code> - Register new account</summary>

**Rate Limited:** Yes (5 req/min)

**Request Body:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| username | string | Yes | Unique username |
| email | string | Yes | Valid email address |
| password | string | Yes | Minimum 8 characters |

**Response (201 Created):**
```json
{
  "token": "eyJhbGciOiJIUzI1NiIs...",
  "refresh_token": "eyJhbGciOiJIUzI1NiIs...",
  "user": {
    "id": "uuid",
    "username": "string",
    "email": "string",
    "is_admin": false,
    "created_at": "timestamp"
  }
}
```

**Error Codes:**
- `400` - Invalid request or validation failure
- `403` - Registration disabled by administrator
- `409` - Username or email already exists

</details>

<details>
<summary><code>POST /api/auth/login</code> - Authenticate user</summary>

**Rate Limited:** Yes (5 req/min)

**Request Body:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| username | string | Yes | Username |
| password | string | Yes | Password |

**Response (200 OK):**
```json
{
  "token": "eyJhbGciOiJIUzI1NiIs...",
  "refresh_token": "eyJhbGciOiJIUzI1NiIs...",
  "user": {
    "id": "uuid",
    "username": "string",
    "email": "string",
    "is_admin": false,
    "created_at": "timestamp"
  }
}
```

**Error Codes:**
- `400` - Invalid request format
- `401` - Invalid credentials
- `403` - Account locked by administrator

</details>

<details>
<summary><code>POST /api/auth/refresh</code> - Refresh access token</summary>

**Rate Limited:** Yes (5 req/min)

**Request Body:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| refresh_token | string | Yes | Valid refresh token |

**Response (200 OK):**
```json
{
  "token": "eyJhbGciOiJIUzI1NiIs..."
}
```

**Error Codes:**
- `400` - Invalid request format
- `401` - Invalid or expired refresh token

</details>

<details>
<summary><code>POST /api/auth/logout</code> - Invalidate session</summary>

**Authentication:** Required

**Response (200 OK):**
```json
{
  "message": "Successfully logged out"
}
```

</details>

<details>
<summary><code>GET /api/auth/sso/config</code> - Get SSO configuration</summary>

**Response (200 OK):**
```json
{
  "google_enabled": true,
  "google_auth_url": "https://accounts.google.com/o/oauth2/...",
  "vault_enabled": false
}
```

</details>

<details>
<summary><code>GET /api/auth/google/login</code> - Initiate Google OAuth</summary>

Redirects to Google's OAuth consent page.

</details>

<details>
<summary><code>GET /api/auth/google/callback</code> - Google OAuth callback</summary>

Called by Google after authentication. Redirects to frontend with token.

</details>

<details>
<summary><code>POST /api/auth/vault/login</code> - Login with Vault JWT</summary>

**Request Body:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| jwt | string | Yes | Vault-issued JWT token |

</details>

---

## User Endpoints

<details>
<summary><code>GET /api/users/me</code> - Get current user profile</summary>

**Authentication:** Required

**Response (200 OK):**
```json
{
  "id": "uuid",
  "username": "string",
  "email": "string",
  "is_admin": false,
  "is_locked": false,
  "created_at": "timestamp",
  "updated_at": "timestamp"
}
```

</details>

<details>
<summary><code>PUT /api/users/me</code> - Update current user</summary>

**Authentication:** Required

**Request Body:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| email | string | No | New email address |
| password | string | No | New password (min 8 chars) |

**Response (200 OK):** Updated user object

**Error Codes:**
- `400` - Invalid email format

</details>

<details>
<summary><code>GET /api/users</code> - List all users <strong>[Admin]</strong></summary>

**Authentication:** Required (Admin)

**Response (200 OK):**
```json
[
  {
    "id": "uuid",
    "username": "string",
    "email": "string",
    "is_admin": false,
    "is_locked": false,
    "created_at": "timestamp"
  }
]
```

</details>

<details>
<summary><code>POST /api/users</code> - Create user <strong>[Admin]</strong></summary>

**Authentication:** Required (Admin)

**Request Body:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| username | string | Yes | Unique username |
| email | string | Yes | Valid email address |
| password | string | Yes | Minimum 8 characters |
| is_admin | boolean | No | Grant admin privileges (default: false) |

**Response (201 Created):** User object

**Error Codes:**
- `400` - Validation failed
- `409` - Username or email already exists

</details>

<details>
<summary><code>DELETE /api/users/:id</code> - Delete user <strong>[Admin]</strong></summary>

**Authentication:** Required (Admin)

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| id | UUID | User ID |

**Response (200 OK):**
```json
{
  "message": "User deleted successfully"
}
```

</details>

<details>
<summary><code>POST /api/users/:id/lock</code> - Lock user account <strong>[Admin]</strong></summary>

**Authentication:** Required (Admin)

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| id | UUID | User ID |

**Response (200 OK):**
```json
{
  "message": "User locked successfully"
}
```

**Error Codes:**
- `403` - Cannot lock admin users

</details>

<details>
<summary><code>POST /api/users/:id/unlock</code> - Unlock user account <strong>[Admin]</strong></summary>

**Authentication:** Required (Admin)

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| id | UUID | User ID |

**Response (200 OK):**
```json
{
  "message": "User unlocked successfully"
}
```

</details>

<details>
<summary><code>GET /api/users/:id/access-keys</code> - List user's access keys <strong>[Admin]</strong></summary>

**Authentication:** Required (Admin)

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| id | UUID | User ID |

**Response (200 OK):** Array of access key objects

</details>

<details>
<summary><code>DELETE /api/users/:id/access-keys/:key_id</code> - Delete user's access key <strong>[Admin]</strong></summary>

**Authentication:** Required (Admin)

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| id | UUID | User ID |
| key_id | UUID | Access key ID |

**Response (200 OK):**
```json
{
  "message": "Access key deleted successfully"
}
```

</details>

---

## Access Keys

Access keys are used for S3-compatible API authentication. Each user can have up to **5 active access keys**.

<details>
<summary><code>POST /api/access-keys</code> - Generate access key</summary>

**Authentication:** Required

**Response (201 Created):**
```json
{
  "message": "Access key created successfully",
  "access_key": "AKIA...",
  "secret_key": "wJalrXUtnFEMI...",
  "created_at": "timestamp",
  "warning": "Save your secret key now. It will not be shown again!"
}
```

> **Important:** The `secret_key` is only shown once at creation time.

**Error Codes:**
- `400` - Maximum access keys reached (5 limit)

</details>

<details>
<summary><code>GET /api/access-keys</code> - List access keys</summary>

**Authentication:** Required

**Response (200 OK):**
```json
[
  {
    "id": "uuid",
    "access_key": "AKIA...XXXX",
    "is_active": true,
    "last_used_at": "timestamp",
    "created_at": "timestamp"
  }
]
```

> Note: Only the last 4 characters of the access key are visible.

</details>

<details>
<summary><code>DELETE /api/access-keys/:id</code> - Revoke access key</summary>

**Authentication:** Required

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| id | UUID | Access key ID |

**Response (200 OK):**
```json
{
  "message": "Access key revoked successfully"
}
```

**Error Codes:**
- `403` - Cannot revoke another user's keys (unless admin)
- `404` - Key not found

</details>

<details>
<summary><code>GET /api/access-keys/stats</code> - Get access key statistics</summary>

**Authentication:** Required

**Response (200 OK):**
```json
{
  "active_keys": 2,
  "total_keys": 3,
  "max_keys": 5
}
```

</details>

---

## Buckets

<details>
<summary><code>GET /api/buckets</code> - List buckets</summary>

**Authentication:** Required

**Response (200 OK):**
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
    "updated_at": "timestamp"
  }
]
```

</details>

<details>
<summary><code>POST /api/buckets</code> - Create bucket <strong>[Admin]</strong></summary>

**Authentication:** Required (Admin)

**Request Body:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| name | string | Yes | S3-compliant bucket name |
| region | string | Yes | AWS region (e.g., "us-east-1") |
| is_public | boolean | No | Public access (default: false) |
| storage_backend | string | No | "local" or "s3" (default: "local") |
| s3_config_id | UUID | No | S3 configuration ID (if using S3 backend) |

**Bucket Naming Rules:**
- 3-63 characters
- Lowercase letters, numbers, and hyphens only
- Must start with a letter or number
- Cannot end with a hyphen
- No consecutive hyphens

**Response (201 Created):** Bucket object

**Error Codes:**
- `400` - Invalid bucket name or region
- `409` - Bucket already exists

</details>

<details>
<summary><code>GET /api/buckets/:name</code> - Get bucket details</summary>

**Authentication:** Required

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| name | string | Bucket name |

**Response (200 OK):** Bucket object

**Error Codes:**
- `403` - Permission denied
- `404` - Bucket not found

</details>

<details>
<summary><code>DELETE /api/buckets/:name</code> - Delete bucket <strong>[Admin]</strong></summary>

**Authentication:** Required (Admin)

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| name | string | Bucket name |

**Response (200 OK):**
```json
{
  "message": "Bucket deleted successfully"
}
```

**Error Codes:**
- `409` - Bucket not empty

</details>

<details>
<summary><code>PUT /api/buckets/:name/policy</code> - Set bucket policy <strong>[Admin]</strong></summary>

**Authentication:** Required (Admin)

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| name | string | Bucket name |

**Request Body:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| policy | string | Yes | JSON policy document |

**Response (200 OK):**
```json
{
  "message": "Bucket policy set successfully"
}
```

</details>

<details>
<summary><code>GET /api/buckets/:name/policy</code> - Get bucket policy</summary>

**Authentication:** Required

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| name | string | Bucket name |

**Response (200 OK):**
```json
{
  "policy": "{\"Version\":\"2012-10-17\",...}"
}
```

</details>

---

## Objects

<details>
<summary><code>GET /api/buckets/:name/objects</code> - List objects</summary>

**Authentication:** Required

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| name | string | Bucket name |

**Query Parameters:**
| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| prefix | string | "" | Filter by key prefix |
| max-keys | integer | 1000 | Maximum objects (1-1000) |

**Response (200 OK):**
```json
{
  "bucket": "my-bucket",
  "objects": [
    {
      "id": "uuid",
      "key": "folder/file.txt",
      "size": 1024,
      "content_type": "text/plain",
      "etag": "d41d8cd98f00b204e9800998ecf8427e",
      "created_at": "timestamp",
      "updated_at": "timestamp"
    }
  ],
  "count": 1
}
```

</details>

<details>
<summary><code>POST /api/buckets/:name/objects</code> - Upload object (synchronous)</summary>

**Authentication:** Required

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| name | string | Bucket name |

**Form Data:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| file | binary | Yes | File to upload |
| key | string | Yes | Object key/path |

**Response (200 OK):**
```json
{
  "message": "Object uploaded successfully",
  "bucket": "my-bucket",
  "key": "folder/file.txt",
  "size": 1024,
  "etag": "d41d8cd98f00b204e9800998ecf8427e",
  "content_type": "text/plain"
}
```

**Error Codes:**
- `400` - Missing key, invalid key, forbidden file type
- `413` - File too large

</details>

<details>
<summary><code>POST /api/buckets/:name/objects/async</code> - Upload object (asynchronous)</summary>

Recommended for large files with progress tracking.

**Authentication:** Required

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| name | string | Bucket name |

**Form Data:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| file | binary | Yes | File to upload |
| key | string | Yes | Object key/path |

**Response (202 Accepted):**
```json
{
  "upload_id": "uuid",
  "status": "pending",
  "message": "Upload initiated. Use /api/uploads/{upload_id}/status to check progress."
}
```

</details>

<details>
<summary><code>GET /api/buckets/:name/objects/*key</code> - Download object</summary>

**Authentication:** Required

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| name | string | Bucket name |
| key | string | Object key (can include slashes) |

**Query Parameters:**
| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| download | boolean | false | Set "true" for attachment download |

**Response Headers:**
- `Content-Type`: Object's MIME type
- `Content-Length`: File size in bytes
- `ETag`: MD5 hash
- `Last-Modified`: Modification timestamp
- `Accept-Ranges`: bytes
- `Content-Disposition`: "inline" or "attachment"

**Response:** Binary file stream

</details>

<details>
<summary><code>HEAD /api/buckets/:name/objects/*key</code> - Get object metadata</summary>

**Authentication:** Required

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| name | string | Bucket name |
| key | string | Object key |

**Response Headers:** Same as GET (no body)

**Status Codes:**
- `200` - Object exists
- `404` - Not found

</details>

<details>
<summary><code>DELETE /api/buckets/:name/objects/*key</code> - Delete object</summary>

**Authentication:** Required

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| name | string | Bucket name |
| key | string | Object key |

**Response (200 OK):**
```json
{
  "message": "Object deleted successfully"
}
```

</details>

<details>
<summary><code>POST /api/buckets/:name/objects/move</code> - Move object</summary>

Move an object to a different location within the same bucket.

**Authentication:** Required

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| name | string | Bucket name |

**Request Body:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| source_key | string | Yes | Current object key |
| destination_key | string | Yes | New object key |

**Response (200 OK):**
```json
{
  "message": "Object moved successfully",
  "object": { ... }
}
```

**Error Codes:**
- `400` - Source and destination are the same
- `404` - Source object not found
- `409` - Destination already exists

</details>

<details>
<summary><code>POST /api/buckets/:name/objects/rename</code> - Rename object</summary>

Rename an object within the same folder.

**Authentication:** Required

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| name | string | Bucket name |

**Request Body:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| source_key | string | Yes | Full path to object |
| new_name | string | Yes | New filename (no slashes) |

**Response (200 OK):**
```json
{
  "message": "Object renamed successfully",
  "object": { ... }
}
```

**Error Codes:**
- `400` - New name contains slashes
- `409` - Object with new name already exists

</details>

<details>
<summary><code>POST /api/buckets/:name/folders/move</code> - Move folder</summary>

Recursively move all objects with a prefix.

**Authentication:** Required

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| name | string | Bucket name |

**Request Body:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| source_prefix | string | Yes | Source folder prefix (e.g., "folder1/") |
| destination_prefix | string | Yes | Destination folder prefix (e.g., "folder2/") |

**Response (200 OK):**
```json
{
  "message": "Folder moved successfully",
  "moved_count": 15
}
```

</details>

---

## Upload Status

<details>
<summary><code>GET /api/uploads</code> - List uploads</summary>

**Authentication:** Required

**Query Parameters:**
| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| status | string | all | Filter: "pending", "processing", "completed", "failed" |
| limit | integer | 50 | Maximum results (1-100) |

**Response (200 OK):**
```json
[
  {
    "id": "uuid",
    "status": "processing",
    "filename": "large-file.zip",
    "object_key": "uploads/large-file.zip",
    "total_size": 104857600,
    "uploaded_size": 52428800,
    "progress_percent": 50.0,
    "error_message": null,
    "object_id": null,
    "created_at": "timestamp",
    "completed_at": null
  }
]
```

</details>

<details>
<summary><code>GET /api/uploads/:id/status</code> - Get upload status</summary>

**Authentication:** Required

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| id | UUID | Upload ID |

**Response (200 OK):** Upload status object

**Error Codes:**
- `404` - Upload not found or doesn't belong to user

</details>

---

## Policies

<details>
<summary><code>GET /api/policies</code> - List policies</summary>

Admins see all policies; users see only their attached policies.

**Authentication:** Required

**Response (200 OK):**
```json
[
  {
    "id": "uuid",
    "name": "ReadOnlyAccess",
    "description": "Read-only access to all buckets",
    "document": "{\"Version\":\"2012-10-17\",...}",
    "created_at": "timestamp",
    "updated_at": "timestamp"
  }
]
```

</details>

<details>
<summary><code>POST /api/policies</code> - Create policy <strong>[Admin]</strong></summary>

**Authentication:** Required (Admin)

**Request Body:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| name | string | Yes | Policy name |
| description | string | No | Policy description |
| document | string | Yes | JSON policy document |

**Policy Document Format:**
```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": ["s3:GetObject", "s3:ListBucket"],
      "Resource": ["arn:aws:s3:::my-bucket/*"]
    }
  ]
}
```

**Response (201 Created):** Policy object

</details>

<details>
<summary><code>GET /api/policies/:id</code> - Get policy <strong>[Admin]</strong></summary>

**Authentication:** Required (Admin)

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| id | UUID | Policy ID |

**Response (200 OK):** Policy object

</details>

<details>
<summary><code>PUT /api/policies/:id</code> - Update policy <strong>[Admin]</strong></summary>

**Authentication:** Required (Admin)

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| id | UUID | Policy ID |

**Request Body:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| name | string | No | Policy name |
| description | string | No | Policy description |
| document | string | No | JSON policy document |

**Response (200 OK):** Updated policy object

</details>

<details>
<summary><code>DELETE /api/policies/:id</code> - Delete policy <strong>[Admin]</strong></summary>

**Authentication:** Required (Admin)

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| id | UUID | Policy ID |

**Response (200 OK):**
```json
{
  "message": "Policy deleted successfully"
}
```

**Error Codes:**
- `409` - Policy is attached to users (detach first)

</details>

<details>
<summary><code>POST /api/policies/users/:user_id/attach</code> - Attach policy to user <strong>[Admin]</strong></summary>

**Authentication:** Required (Admin)

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| user_id | UUID | User ID |

**Request Body:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| policy_id | UUID | Yes | Policy ID to attach |

**Response (200 OK):**
```json
{
  "message": "Policy attached successfully"
}
```

</details>

<details>
<summary><code>DELETE /api/policies/users/:user_id/detach/:policy_id</code> - Detach policy <strong>[Admin]</strong></summary>

**Authentication:** Required (Admin)

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| user_id | UUID | User ID |
| policy_id | UUID | Policy ID |

**Response (200 OK):**
```json
{
  "message": "Policy detached successfully"
}
```

</details>

---

## S3 Configurations

Manage external S3-compatible storage backends (admin only).

<details>
<summary><code>GET /api/s3-configs</code> - List S3 configurations <strong>[Admin]</strong></summary>

**Authentication:** Required (Admin)

**Response (200 OK):**
```json
[
  {
    "id": "uuid",
    "name": "AWS Production",
    "endpoint": "s3.amazonaws.com",
    "region": "us-east-1",
    "access_key_id": "AKIA...",
    "bucket_prefix": "prod-",
    "use_ssl": true,
    "force_path_style": false,
    "is_default": true,
    "created_at": "timestamp"
  }
]
```

</details>

<details>
<summary><code>POST /api/s3-configs</code> - Create S3 configuration <strong>[Admin]</strong></summary>

**Authentication:** Required (Admin)

**Request Body:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| name | string | Yes | Configuration name |
| endpoint | string | Yes | S3 endpoint (e.g., "s3.amazonaws.com") |
| region | string | Yes | AWS region |
| access_key_id | string | Yes | AWS access key ID |
| secret_access_key | string | Yes | AWS secret access key |
| bucket_prefix | string | No | Prefix for bucket names |
| use_ssl | boolean | No | Use HTTPS (default: true) |
| force_path_style | boolean | No | Use path-style URLs (default: false) |
| is_default | boolean | No | Set as default configuration |

> Credentials are encrypted before storage.

**Response (201 Created):** S3 configuration object

**Error Codes:**
- `409` - Configuration name already exists

</details>

<details>
<summary><code>GET /api/s3-configs/:id</code> - Get S3 configuration <strong>[Admin]</strong></summary>

**Authentication:** Required (Admin)

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| id | UUID | Configuration ID |

**Response (200 OK):** S3 configuration object

</details>

<details>
<summary><code>PUT /api/s3-configs/:id</code> - Update S3 configuration <strong>[Admin]</strong></summary>

**Authentication:** Required (Admin)

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| id | UUID | Configuration ID |

**Request Body:** Same as create (all fields optional)

**Response (200 OK):** Updated configuration object

</details>

<details>
<summary><code>DELETE /api/s3-configs/:id</code> - Delete S3 configuration <strong>[Admin]</strong></summary>

**Authentication:** Required (Admin)

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| id | UUID | Configuration ID |

**Response (200 OK):**
```json
{
  "message": "S3 configuration deleted successfully"
}
```

**Error Codes:**
- `409` - Configuration is in use by buckets

</details>

---

## S3-Compatible API

The S3-compatible API enables tools like `s3fs-fuse`, AWS CLI, and other S3 clients to interact with bkt.

### Authentication

Uses **AWS Signature V4** with access keys generated from the bkt API.

**Required Headers:**
- `Authorization`: AWS4-HMAC-SHA256 signature
- `X-Amz-Date`: Request timestamp
- `X-Amz-Content-Sha256`: Content hash

<details>
<summary><code>GET /</code> - List buckets (S3)</summary>

**Response (200 OK):** XML
```xml
<?xml version="1.0" encoding="UTF-8"?>
<ListAllMyBucketsResult>
  <Owner>
    <ID>user-uuid</ID>
    <DisplayName>username</DisplayName>
  </Owner>
  <Buckets>
    <Bucket>
      <Name>my-bucket</Name>
      <CreationDate>2024-01-15T10:30:00Z</CreationDate>
    </Bucket>
  </Buckets>
</ListAllMyBucketsResult>
```

</details>

<details>
<summary><code>HEAD /:bucket</code> - Check bucket exists (S3)</summary>

**Status Codes:**
- `200` - Bucket exists
- `403` - Access denied
- `404` - Bucket not found

</details>

<details>
<summary><code>GET /:bucket</code> - List objects (S3)</summary>

**Query Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| prefix | string | Filter by key prefix |
| delimiter | string | Hierarchy delimiter (e.g., "/") |
| max-keys | integer | Maximum objects to return |

**Response (200 OK):** XML ListBucketResult

</details>

<details>
<summary><code>PUT /:bucket/:key</code> - Put object (S3)</summary>

**Required Headers:**
- `Content-Length`: File size
- `Content-Type`: MIME type

**Response Headers:**
- `ETag`: MD5 hash of uploaded object

**Error Codes:**
- `411` - Missing Content-Length
- `413` - Entity too large

</details>

<details>
<summary><code>GET /:bucket/:key</code> - Get object (S3)</summary>

**Response:** Binary file stream with appropriate headers

</details>

<details>
<summary><code>HEAD /:bucket/:key</code> - Head object (S3)</summary>

**Response Headers:**
- `Content-Type`, `Content-Length`, `ETag`, `Last-Modified`

</details>

<details>
<summary><code>DELETE /:bucket/:key</code> - Delete object (S3)</summary>

**Status:** `204 No Content`

</details>

<details>
<summary><code>PUT /:bucket</code> - Create bucket (S3) - DISABLED</summary>

**Status:** `403 Forbidden`

Bucket creation via S3 API is disabled. Use the web UI or REST API.

</details>

---

## Error Handling

### Standard Error Response

```json
{
  "error": "Error type",
  "message": "Detailed error message"
}
```

### HTTP Status Codes

| Code | Description |
|------|-------------|
| 200 | OK - Request successful |
| 201 | Created - Resource created |
| 202 | Accepted - Async operation started |
| 204 | No Content - Success with no body |
| 400 | Bad Request - Validation error |
| 401 | Unauthorized - Missing or invalid token |
| 403 | Forbidden - Permission denied |
| 404 | Not Found - Resource doesn't exist |
| 409 | Conflict - Duplicate or resource in use |
| 411 | Length Required - Missing Content-Length |
| 413 | Payload Too Large - File exceeds limit |
| 429 | Too Many Requests - Rate limit exceeded |
| 500 | Internal Server Error |

---

## Security Features

### Authentication
- JWT tokens with configurable expiration
- Refresh token rotation
- Account locking capability

### Authorization
- Role-based access (admin/user)
- Policy-based bucket and object permissions
- Per-resource access control

### Data Protection
- Passwords hashed with bcrypt
- S3 credentials encrypted at rest
- Secret keys shown only once at creation
- Constant-time comparison for sensitive values

### Input Validation
- Content type detection from file magic numbers
- Path traversal prevention
- SQL injection protection
- Rate limiting on authentication endpoints

### Headers
- Idempotency support via `Idempotency-Key` header
- Cache control for sensitive responses
- CORS configuration support
