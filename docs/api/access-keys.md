# Access Keys API

Access keys provide programmatic access to the API without requiring username/password authentication. They consist of an access key (public) and secret key (private) pair, similar to AWS IAM access keys.

## Base URL

```
https://localhost:9443/api/access-keys
```

## Security Model

- **Access Key Format:** `AK` + 20 random bytes (~27 characters)
- **Secret Key Format:** `SK` + 40 random bytes (~54 characters)
- **Secret Storage:** Bcrypt hashed (cost 12), never stored in plaintext
- **Secret Display:** Shown only once during creation
- **Validation:** Constant-time comparison to prevent timing attacks
- **Limits:** Maximum 5 active **regular** keys per user (temporary bkt-STS keys are excluded from the limit)
- **Scoping:** Keys can be named, given an expiry, and marked read-only (read-only denies all mutating S3 operations)

## Endpoints

### Generate Access Key

Create a new access key pair.

**Endpoint:** `POST /access-keys`

**Authentication:** Required (Bearer token)

**Request Body (optional):** an empty body creates an unnamed, non-expiring, full-access key.

```json
{
  "name": "ci-deploy",
  "read_only": false,
  "expires_in_days": 90
}
```

- `name` (string, optional) - Human-readable key name
- `read_only` (boolean, optional) - Deny all mutating S3 operations with this key
- `expires_in_days` (integer, optional) - Auto-expire after N days (0/omitted = never)

**Success Response (201 Created):**
```json
{
  "message": "Access key created successfully",
  "access_key": "AKGAUJicHqerbIjN9m7WSCCyRtZJ0",
  "secret_key": "SKMUprmvSZ_eBYwIgOKRENHXHBIiGOxX_xOm8FHNmmBP_4xDPQY41TeA",
  "name": "ci-deploy",
  "read_only": false,
  "expires_at": "2026-12-01T21:30:11Z",
  "created_at": "2026-09-02T21:30:11.064968622Z",
  "warning": "Save your secret key now. It will not be shown again!"
}
```

⚠️ **IMPORTANT:** The `secret_key` is shown **only once** during creation. Save it securely. You cannot retrieve it later.

**Error Responses:**
- `400 Bad Request` - Maximum keys reached (5 active regular keys per user), or malformed body
- `401 Unauthorized` - Invalid or expired token
- `500 Internal Server Error` - Key generation failed

**Example:**
```bash
# Unscoped key
curl -k -X POST https://localhost:9443/api/access-keys \
  -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...'

# Named, read-only key expiring in 30 days
curl -k -X POST https://localhost:9443/api/access-keys \
  -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...' \
  -H 'Content-Type: application/json' \
  -d '{"name": "reporting", "read_only": true, "expires_in_days": 30}'
```

---

### List Access Keys

List all access keys for the authenticated user.

**Endpoint:** `GET /access-keys`

**Authentication:** Required (Bearer token)

**Success Response (200 OK):**
```json
[
  {
    "id": "f98f3285-3156-44d5-8c57-ee003cefe0ea",
    "user_id": "ece39642-19ac-4ea3-b5cb-e818ce0a9fb9",
    "access_key": "AKGAUJicHqerbIjN9m7WSCCyRtZJ0",
    "name": "ci-deploy",
    "read_only": false,
    "is_active": true,
    "expires_at": null,
    "last_used_at": "2026-09-01T21:35:42Z",
    "created_at": "2026-09-01T21:30:11.064968Z"
  }
]
```

**Notes:**
- Secret key hashes are **never** returned
- `last_used_at` is updated each time the key is used for authentication
- Only **active** keys are shown — revoked keys are soft-deleted for the audit trail and hidden from this list (admins see the full history, including revoked keys, via `GET /api/users/:id/access-keys`)
- Temporary bkt-STS keys are excluded from this list

**Error Responses:**
- `401 Unauthorized` - Invalid or expired token

**Example:**
```bash
curl -k -X GET https://localhost:9443/api/access-keys \
  -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...'
```

---

### Revoke Access Key

Deactivate an access key. Revocation is a **soft delete**: the key stops working immediately, but the record is retained for the audit trail. Revoked keys disappear from the user's own key list; admins can still see them via `GET /api/users/:id/access-keys`.

**Endpoint:** `DELETE /access-keys/:id`

**Authentication:** Required (Bearer token)

**Parameters:**
- `id` (path) - UUID of the access key to revoke

**Success Response (200 OK):**
```json
{
  "message": "Access key revoked successfully"
}
```

**Authorization:**
- Users can only revoke their own keys
- Admins can revoke any user's keys

**Error Responses:**
- `400 Bad Request` - Invalid access key ID format
- `401 Unauthorized` - Invalid or expired token
- `403 Forbidden` - Not authorized to revoke this key
- `404 Not Found` - Access key not found

**Example:**
```bash
curl -k -X DELETE https://localhost:9443/api/access-keys/f98f3285-3156-44d5-8c57-ee003cefe0ea \
  -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...'
```

---

### Get Access Key Statistics

Get statistics about access keys for the authenticated user.

**Endpoint:** `GET /access-keys/stats`

**Authentication:** Required (Bearer token)

**Success Response (200 OK):**
```json
{
  "active_keys": 5,
  "total_keys": 8,
  "max_keys": 5
}
```

**Fields:**
- `active_keys` - Number of currently active keys
- `total_keys` - Total keys (including revoked)
- `max_keys` - Maximum allowed active regular keys (5)

**Example:**
```bash
curl -k -X GET https://localhost:9443/api/access-keys/stats \
  -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...'
```

---

## Temporary Credentials (bkt-STS)

bkt can mint **short-lived S3 access key pairs** for the authenticated user — handy for scripts, demos, or handing a colleague scoped access without creating a long-lived key.

> ⚠️ **Not AWS STS:** bkt-STS is **not** compatible with the AWS STS API (`AssumeRole`, `GetSessionToken`, etc.). It is a simple REST endpoint that issues expiring access key pairs usable against bkt's S3-compatible API.

**Properties:**
- Default lifetime **1 hour**, maximum **12 hours** (minimum 1 minute)
- **Excluded** from the 5-active-key limit and from `GET /api/access-keys`
- Automatically **hard-deleted** after expiry
- Optionally read-only
- Issuance is recorded in the audit log as `sts.issue`

### Issue Temporary Credentials

**Endpoint:** `POST /sts/credentials` (i.e. `https://localhost:9443/api/sts/credentials`)

**Authentication:** Required (Bearer token)

**Request Body (optional):**
```json
{
  "duration_seconds": 3600,
  "read_only": true
}
```

- `duration_seconds` (integer, optional) - Lifetime in seconds (default 3600, max 43200)
- `read_only` (boolean, optional) - Deny all mutating S3 operations

**Success Response (201 Created):**
```json
{
  "access_key": "AK...",
  "secret_key": "SK...",
  "expires_at": "2026-09-02T13:00:00Z",
  "read_only": true
}
```

⚠️ The `secret_key` is shown only once, like a regular key.

**Example:**
```bash
curl -k -X POST https://localhost:9443/api/sts/credentials \
  -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...' \
  -H 'Content-Type: application/json' \
  -d '{"duration_seconds": 7200, "read_only": true}'
```

---

## Using Access Keys for Authentication

Access keys can be used instead of JWT tokens for API authentication. This is useful for:
- Server-to-server communication
- CLI tools
- Automated scripts
- Long-running applications

### Authentication Methods

**Method 1: HTTP Basic Auth (Recommended)**
```bash
curl -k -X GET https://localhost:9443/api/buckets \
  -u "AKGAUJicHqerbIjN9m7WSCCyRtZJ0:SKMUprmvSZ_eBYwIgOKRENHXHBIiGOxX_xOm8FHNmmBP_4xDPQY41TeA"
```

**Method 2: Custom Headers**
```bash
curl -k -X GET https://localhost:9443/api/buckets \
  -H 'X-Access-Key: AKGAUJicHqerbIjN9m7WSCCyRtZJ0' \
  -H 'X-Secret-Key: SKMUprmvSZ_eBYwIgOKRENHXHBIiGOxX_xOm8FHNmmBP_4xDPQY41TeA'
```

**Method 3: AWS Signature V4 (S3-Compatible)**
```bash
# Used by AWS SDKs automatically
# See S3 API documentation for details
```

---

## Access Key Lifecycle

```
┌─────────────┐
│   Create    │ ← Secret shown only once
└──────┬──────┘
       │
       ▼
┌─────────────┐
│   Active    │ ← Can be used for authentication
└──────┬──────┘   Last used timestamp updated
       │
       ▼
┌─────────────┐
│   Revoked   │ ← Soft deleted, cannot be used
└─────────────┘   Kept for audit trail
```

---

## Best Practices

### 1. Secret Key Management

**DO:**
- ✅ Save the secret key immediately after creation
- ✅ Store secrets in environment variables or secret managers
- ✅ Use different keys for different environments (dev/staging/prod)
- ✅ Rotate keys regularly (every 90 days)
- ✅ Revoke unused keys

**DON'T:**
- ❌ Commit secrets to version control
- ❌ Share secrets via email or chat
- ❌ Embed secrets in client-side code
- ❌ Log secrets in application logs
- ❌ Store secrets in plain text files

### 2. Key Rotation

```bash
# 1. Generate new key
NEW_KEY=$(curl -k -X POST https://localhost:9443/api/access-keys \
  -H "Authorization: Bearer $TOKEN" | jq -r '.access_key,.secret_key')

# 2. Update application configuration with new key
export ACCESS_KEY="AK..."
export SECRET_KEY="SK..."

# 3. Verify new key works
curl -k -X GET https://localhost:9443/api/buckets \
  -u "$ACCESS_KEY:$SECRET_KEY"

# 4. Revoke old key
curl -k -X DELETE https://localhost:9443/api/access-keys/$OLD_KEY_ID \
  -H "Authorization: Bearer $TOKEN"
```

### 3. Environment Variables

```bash
# .env file (NEVER commit this!)
OBJECTSTORE_ACCESS_KEY=AKGAUJicHqerbIjN9m7WSCCyRtZJ0
OBJECTSTORE_SECRET_KEY=SKMUprmvSZ_eBYwIgOKRENHXHBIiGOxX_xOm8FHNmmBP_4xDPQY41TeA
OBJECTSTORE_ENDPOINT=https://objectstore.example.com

# Use in application
export $(cat .env | xargs)
./my-app
```

### 4. Monitoring

Monitor access key usage:
- Check `last_used_at` timestamps regularly
- Revoke keys that haven't been used in 90+ days
- Alert on new key creation
- Audit key usage in logs

---

## Security Features

### Cryptographic Security
- **Random Generation:** Uses `crypto/rand` (not `math/rand`)
- **Key Length:** 20 bytes (access) and 40 bytes (secret)
- **Encoding:** Base64URL (no padding)
- **Hashing:** Bcrypt with cost factor 12
- **Comparison:** Constant-time to prevent timing attacks

### Rate Limiting
Access key authentication is subject to rate limiting:
- 100 requests per minute per key
- 429 Too Many Requests if exceeded

### Audit Trail
- All key operations are logged
- Soft delete preserves revoked keys
- Last used timestamp tracked
- Creation timestamp preserved

---

## Example Use Cases

### CLI Tool
```bash
#!/bin/bash
# upload.sh - Upload files using access keys

ACCESS_KEY="AKGAUJicHqerbIjN9m7WSCCyRtZJ0"
SECRET_KEY="SKMUprmvSZ_eBYwIgOKRENHXHBIiGOxX_xOm8FHNmmBP_4xDPQY41TeA"
ENDPOINT="https://objectstore.example.com"

# Upload file
curl -k -X POST "$ENDPOINT/api/buckets/mybucket/objects" \
  -u "$ACCESS_KEY:$SECRET_KEY" \
  -F "key=myfile.txt" \
  -F "file=@/path/to/myfile.txt"
```

### Python Application
```python
import os
import requests

ACCESS_KEY = os.environ['OBJECTSTORE_ACCESS_KEY']
SECRET_KEY = os.environ['OBJECTSTORE_SECRET_KEY']
ENDPOINT = os.environ['OBJECTSTORE_ENDPOINT']

# List buckets
response = requests.get(
    f'{ENDPOINT}/api/buckets',
    auth=(ACCESS_KEY, SECRET_KEY),
    verify=False  # Only for self-signed certs
)

buckets = response.json()
print(f'Found {len(buckets)} buckets')
```

### Automated Backup Script
```python
#!/usr/bin/env python3
import os
import requests
from datetime import datetime

def backup_to_objectstore(file_path, bucket_name):
    access_key = os.environ['BACKUP_ACCESS_KEY']
    secret_key = os.environ['BACKUP_SECRET_KEY']
    endpoint = os.environ['OBJECTSTORE_ENDPOINT']

    filename = os.path.basename(file_path)
    timestamp = datetime.now().strftime('%Y%m%d_%H%M%S')
    object_key = f'backups/{timestamp}_{filename}'

    with open(file_path, 'rb') as f:
        response = requests.post(
            f'{endpoint}/api/buckets/{bucket_name}/objects',
            auth=(access_key, secret_key),
            data={'key': object_key},
            files={'file': f},
            verify=False
        )

    if response.status_code == 200:
        print(f'✓ Backup successful: {object_key}')
    else:
        print(f'✗ Backup failed: {response.text}')

if __name__ == '__main__':
    backup_to_objectstore('/data/database.sql', 'backups')
```

---

## Related Documentation

- [Authentication API](authentication.md) - JWT-based authentication
- [Policies API](policies.md) - Access control policies for keys
- [Security Overview](../security/security-overview.md) - Security best practices
- [S3fs Mounting](../guides/MOUNTING.md) - Use access keys to mount buckets
