# bkt API Documentation

Complete API reference for bkt.

## Table of Contents

- [Authentication](#authentication)
- [Users](#users)
- [Access Keys](#access-keys)
- [Buckets](#buckets)
- [Objects](#objects)
- [Upload Status](#upload-status)
- [Policies](#policies)
- [S3 Configurations](#s3-configurations)
- [S3-Compatible API](#s3-compatible-api)
- [Error Handling](#error-handling)

---

## Base URL

All API endpoints are prefixed with `/api` except for the S3-compatible API which uses the root path.

## Authentication

All protected endpoints require a JWT token in the Authorization header:

```
Authorization: Bearer <token>
```

### Rate Limiting

Authentication endpoints are rate-limited to **5 requests per minute per IP** to prevent brute force attacks.

---

## Authentication Endpoints

### Register

Create a new user account.

```
POST /api/auth/register
```

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

---

### Login

Authenticate and receive JWT tokens.

```
POST /api/auth/login
```

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

---

### Refresh Token

Generate a new access token using a refresh token.

```
POST /api/auth/refresh
```

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

---

### Logout

Invalidate the current session.

```
POST /api/auth/logout
```

**Authentication:** Required

**Response (200 OK):**
```json
{
  "message": "Successfully logged out"
}
```

---

### Get SSO Configuration

Get SSO provider configuration for the frontend.

```
GET /api/auth/sso/config
```

**Response (200 OK):**
```json
{
  "google_enabled": true,
  "google_auth_url": "https://accounts.google.com/o/oauth2/...",
  "vault_enabled": false
}
```

---

### Google OAuth Login

Initiate Google OAuth login flow.

```
GET /api/auth/google/login
```

Redirects to Google's OAuth consent page.

---

### Google OAuth Callback

Handle Google OAuth callback (called by Google).

```
GET /api/auth/google/callback
```

---

### Vault JWT Login

Login using HashiCorp Vault JWT.

```
POST /api/auth/vault/login
```

**Request Body:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| jwt | string | Yes | Vault-issued JWT token |

---

## Users

### Get Current User

Get the authenticated user's profile.

```
GET /api/users/me
```

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

---

### Update Current User

Update the authenticated user's profile.

```
PUT /api/users/me
```

**Authentication:** Required

**Request Body:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| email | string | No | New email address |
| password | string | No | New password (min 8 chars) |

**Response (200 OK):** Updated user object

**Error Codes:**
- `400` - Invalid email format

---

### List Users

List all users (admin only).

```
GET /api/users
```

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

---

### Create User

Create a new user (admin only).

```
POST /api/users
```

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

---

### Delete User

Delete a user account (admin only).

```
DELETE /api/users/:id
```

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

---

### Lock User

Lock a user account to prevent login (admin only).

```
POST /api/users/:id/lock
```

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

---

### Unlock User

Unlock a user account (admin only).

```
POST /api/users/:id/unlock
```

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

---

### List User's Access Keys

List all access keys for a specific user (admin only).

```
GET /api/users/:id/access-keys
```

**Authentication:** Required (Admin)

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| id | UUID | User ID |

**Response (200 OK):** Array of access key objects

---

### Delete User's Access Key

Delete a specific user's access key (admin only).

```
DELETE /api/users/:id/access-keys/:key_id
```

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

---

## Access Keys

Access keys are used for S3-compatible API authentication. Each user can have up to **5 active access keys**.

### Generate Access Key

Create a new access key pair.

```
POST /api/access-keys
```

**Authentication:** Required

**Request Body:** None

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

> **Important:** The `secret_key` is only shown once at creation time. Store it securely.

**Error Codes:**
- `400` - Maximum access keys reached (5 limit)

---

### List Access Keys

List the authenticated user's access keys.

```
GET /api/access-keys
```

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

> Note: Only the last 4 characters of the access key are visible for security.

---

### Revoke Access Key

Revoke (soft delete) an access key.

```
DELETE /api/access-keys/:id
```

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

---

### Get Access Key Stats

Get statistics about the user's access keys.

```
GET /api/access-keys/stats
```

**Authentication:** Required

**Response (200 OK):**
```json
{
  "active_keys": 2,
  "total_keys": 3,
  "max_keys": 5
}
```

---

## Buckets

### List Buckets

List buckets accessible to the user.

```
GET /api/buckets
```

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

---

### Create Bucket

Create a new bucket (admin only).

```
POST /api/buckets
```

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

---

### Get Bucket

Get bucket details.

```
GET /api/buckets/:name
```

**Authentication:** Required

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| name | string | Bucket name |

**Response (200 OK):** Bucket object

**Error Codes:**
- `403` - Permission denied
- `404` - Bucket not found

---

### Delete Bucket

Delete a bucket (admin only, must be empty).

```
DELETE /api/buckets/:name
```

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

---

### Set Bucket Policy

Set the bucket's access policy (admin only).

```
PUT /api/buckets/:name/policy
```

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

---

### Get Bucket Policy

Get the bucket's access policy.

```
GET /api/buckets/:name/policy
```

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

---

## Objects

### List Objects

List objects in a bucket.

```
GET /api/buckets/:name/objects
```

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

---

### Upload Object (Synchronous)

Upload a file synchronously.

```
POST /api/buckets/:name/objects
```

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

---

### Upload Object (Asynchronous)

Upload a file asynchronously with progress tracking. Recommended for large files.

```
POST /api/buckets/:name/objects/async
```

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

---

### Download Object

Download or stream an object.

```
GET /api/buckets/:name/objects/*key
```

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

---

### Head Object

Get object metadata without downloading.

```
HEAD /api/buckets/:name/objects/*key
```

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

---

### Delete Object

Delete an object.

```
DELETE /api/buckets/:name/objects/*key
```

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

---

### Move Object

Move an object to a different location within the same bucket.

```
POST /api/buckets/:name/objects/move
```

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

---

### Rename Object

Rename an object (within the same folder).

```
POST /api/buckets/:name/objects/rename
```

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

---

### Move Folder

Recursively move all objects with a prefix (folder).

```
POST /api/buckets/:name/folders/move
```

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

---

## Upload Status

### List Uploads

List the user's async uploads.

```
GET /api/uploads
```

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

---

### Get Upload Status

Get real-time status of a specific upload.

```
GET /api/uploads/:id/status
```

**Authentication:** Required

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| id | UUID | Upload ID |

**Response (200 OK):** Upload status object (same format as list)

**Error Codes:**
- `404` - Upload not found or doesn't belong to user

---

## Policies

### List Policies

List policies. Admins see all policies; users see only their attached policies.

```
GET /api/policies
```

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

---

### Create Policy

Create a new policy (admin only).

```
POST /api/policies
```

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

---

### Get Policy

Get policy details (admin only).

```
GET /api/policies/:id
```

**Authentication:** Required (Admin)

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| id | UUID | Policy ID |

**Response (200 OK):** Policy object

---

### Update Policy

Update a policy (admin only).

```
PUT /api/policies/:id
```

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

---

### Delete Policy

Delete a policy (admin only).

```
DELETE /api/policies/:id
```

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

---

### Attach Policy to User

Attach a policy to a user (admin only).

```
POST /api/policies/users/:user_id/attach
```

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

---

### Detach Policy from User

Detach a policy from a user (admin only).

```
DELETE /api/policies/users/:user_id/detach/:policy_id
```

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

---

## S3 Configurations

Manage external S3-compatible storage backends (admin only).

### List S3 Configurations

```
GET /api/s3-configs
```

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

---

### Create S3 Configuration

```
POST /api/s3-configs
```

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

> **Note:** Credentials are encrypted before storage.

**Response (201 Created):** S3 configuration object

**Error Codes:**
- `409` - Configuration name already exists

---

### Get S3 Configuration

```
GET /api/s3-configs/:id
```

**Authentication:** Required (Admin)

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| id | UUID | Configuration ID |

**Response (200 OK):** S3 configuration object

---

### Update S3 Configuration

```
PUT /api/s3-configs/:id
```

**Authentication:** Required (Admin)

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| id | UUID | Configuration ID |

**Request Body:** Same as create (all fields optional)

**Response (200 OK):** Updated configuration object

---

### Delete S3 Configuration

```
DELETE /api/s3-configs/:id
```

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

---

## S3-Compatible API

The S3-compatible API enables tools like `s3fs-fuse`, AWS CLI, and other S3 clients to interact with BKT.

### Authentication

Uses **AWS Signature V4** authentication with access keys generated from the BKT API.

**Required Headers:**
- `Authorization`: AWS4-HMAC-SHA256 signature
- `X-Amz-Date`: Request timestamp
- `X-Amz-Content-Sha256`: Content hash

### List Buckets

```
GET /
```

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

---

### Head Bucket

Check if bucket exists and is accessible.

```
HEAD /:bucket
```

**Status Codes:**
- `200` - Bucket exists
- `403` - Access denied
- `404` - Bucket not found

---

### List Objects (S3)

```
GET /:bucket
```

**Query Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| prefix | string | Filter by key prefix |
| delimiter | string | Hierarchy delimiter (e.g., "/") |
| max-keys | integer | Maximum objects to return |

**Response (200 OK):** XML ListBucketResult

---

### Put Object (S3)

```
PUT /:bucket/:key
```

**Required Headers:**
- `Content-Length`: File size
- `Content-Type`: MIME type

**Response Headers:**
- `ETag`: MD5 hash of uploaded object

**Error Codes:**
- `411` - Missing Content-Length
- `413` - Entity too large

---

### Get Object (S3)

```
GET /:bucket/:key
```

**Response:** Binary file stream with appropriate headers

---

### Head Object (S3)

```
HEAD /:bucket/:key
```

**Response Headers:**
- `Content-Type`, `Content-Length`, `ETag`, `Last-Modified`

---

### Delete Object (S3)

```
DELETE /:bucket/:key
```

**Status:** `204 No Content` (per S3 specification)

---

### Create Bucket (S3)

```
PUT /:bucket
```

**Status:** `403 Forbidden`

> **Note:** Bucket creation via S3 API is disabled. Use the web UI or REST API.

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
