# cURL Examples

Complete examples for interacting with the bkt API using cURL.

## Authentication

### Admin Login

```bash
# Login with the admin account created by setup.py
curl -k -X POST https://localhost:9443/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{
    "username": "testadmin",
    "password": "YOUR_PASSWORD_FROM_SETUP"
  }' | jq -r '.token' > token.txt

# Use the token
export TOKEN=$(cat token.txt)
```

**Note:** The admin password is in your `.env` file. Check with: `grep ADMIN_PASSWORD .env`

### Regular User Login

```bash
# After an admin creates your account
curl -k -X POST https://localhost:9443/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{
    "username": "youruser",
    "password": "yourpassword"
  }' | jq -r '.token' > token.txt

# Use the token
export TOKEN=$(cat token.txt)
```

### Refresh Token

```bash
# Save refresh token from login
REFRESH_TOKEN="eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."

curl -k -X POST https://localhost:9443/api/auth/refresh \
  -H 'Content-Type: application/json' \
  -d "{\"refresh_token\":\"$REFRESH_TOKEN\"}"
```

### Self-Registration (If Enabled)

**Note:** Registration is disabled by default (`ALLOW_REGISTRATION=false`). Only admins can create users.

If registration is enabled in production (not recommended):

```bash
curl -k -X POST https://localhost:9443/api/auth/register \
  -H 'Content-Type: application/json' \
  -d '{
    "username": "johndoe",
    "email": "john@example.com",
    "password": "SecurePass123"
  }'
```


## Admin - User Management

### Create User (Admin Only)

```bash
# Admins can create new users
curl -k -X POST https://localhost:9443/api/users \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "username": "newuser",
    "email": "newuser@example.com",
    "password": "SecurePassword123",
    "is_admin": false
  }'
```

### Create Admin User (Admin Only)

```bash
curl -k -X POST https://localhost:9443/api/users \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "username": "newadmin",
    "email": "newadmin@example.com",
    "password": "SecureAdminPass123",
    "is_admin": true
  }'
```

### Delete User (Admin Only)

```bash
curl -k -X DELETE https://localhost:9443/api/users/{user_id} \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

## Presigned URLs

### Generate a Presigned Download URL

Requires at least one active access key (the URL is signed with it).

```bash
# 1-hour shareable download link
curl -k -X POST https://localhost:9443/api/buckets/my-bucket/objects/presign \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "key": "documents/report.pdf",
    "expires_in": 3600
  }'

# Response: {"url": "https://host:9000/my-bucket/...", "expires_at": "...", ...}
# Anyone with the URL can download until it expires (no JWT needed):
curl -k -o report.pdf "URL_FROM_RESPONSE"
```

## Object Versioning

### Enable Versioning on a Bucket

```bash
# Bucket owner or admin
curl -k -X PUT https://localhost:9443/api/buckets/my-bucket/versioning \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"versioning": "enabled"}'
```

### List an Object's Versions

Note the path: `/object-versions`, not `/objects/versions`.

```bash
curl -k -X GET "https://localhost:9443/api/buckets/my-bucket/object-versions?key=documents/report.pdf" \
  -H "Authorization: Bearer $TOKEN"
```

### Restore a Version

```bash
# Grab a version_id from the listing above, then copy it forward as the
# new current version (history is preserved)
curl -k -X POST https://localhost:9443/api/buckets/my-bucket/objects/restore \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "key": "documents/report.pdf",
    "version_id": "31fc0d84-8f2e-4a11-9c33-2f9d1c7c1234"
  }'
```

### Permanently Delete a Version

```bash
curl -k -X DELETE "https://localhost:9443/api/buckets/my-bucket/object-versions?key=documents/report.pdf&version_id=31fc0d84-8f2e-4a11-9c33-2f9d1c7c1234" \
  -H "Authorization: Bearer $TOKEN"
```

## Bucket Settings

### Set Quota, Retention, Webhook, or Replication

Partial update — only the fields you send are changed (bucket owner or admin).

```bash
# 10 GiB quota + webhook on created/removed events
curl -k -X PUT https://localhost:9443/api/buckets/my-bucket/settings \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "quota_bytes": 10737418240,
    "webhook_url": "https://example.com/hooks/bkt",
    "webhook_secret": "s3cret",
    "webhook_events": "created,removed"
  }'

# 30-day retention (requires versioning enabled first)
curl -k -X PUT https://localhost:9443/api/buckets/my-bucket/settings \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"retention_days": 30}'

# Mirror into another bucket on the same instance
curl -k -X PUT https://localhost:9443/api/buckets/my-bucket/settings \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"replicate_to": "my-bucket-mirror"}'
```

### Set Lifecycle Expiry

```bash
# Expire logs/ objects after 90 days; purge noncurrent versions after 30
curl -k -X PUT https://localhost:9443/api/buckets/my-bucket/lifecycle \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "expire_days": 90,
    "prefix": "logs/",
    "noncurrent_expire_days": 30
  }'
```

## Admin - Groups

### Create a Group and Add a Member

```bash
# Create the group
curl -k -X POST https://localhost:9443/api/groups \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "engineering",
    "description": "Engineering team"
  }'
# Response includes the group "id"

# Add a member (users in the group inherit its policies)
curl -k -X POST https://localhost:9443/api/groups/{group_id}/members \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"user_id": "{user_id}"}'

# Attach a policy to the group
curl -k -X POST https://localhost:9443/api/groups/{group_id}/policies \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"policy_id": "{policy_id}"}'
```

## Temporary Credentials (bkt-STS)

### Issue Temporary S3 Credentials

Short-lived access key pair (default 1h, max 12h). Not AWS STS API-compatible. Temporary keys don't count against the 5-key limit and are removed automatically after expiry.

```bash
curl -k -X POST https://localhost:9443/api/sts/credentials \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "duration_seconds": 7200,
    "read_only": true
  }'

# Response (secret shown only once):
# {"access_key": "AK...", "secret_key": "SK...", "expires_at": "...", "read_only": true}

# Use them against the S3 API listener like any key pair:
AWS_ACCESS_KEY_ID=AK... AWS_SECRET_ACCESS_KEY=SK... \
  aws --endpoint-url https://localhost:9000 --no-verify-ssl s3 ls s3://my-bucket/
```

