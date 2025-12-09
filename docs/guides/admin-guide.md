# Administrator Guide

This guide covers administrative tasks for managing the Object Storage system.

## Table of Contents

- [User Management](#user-management)
- [Policy Management](#policy-management)
- [Access Key Management](#access-key-management)
- [System Monitoring](#system-monitoring)
- [Security](#security)
- [Backup and Recovery](#backup-and-recovery)
- [Troubleshooting](#troubleshooting)

## User Management
## User Management

### Default Admin Account

The `setup.py` script creates a default admin account:
- **Username**: `testadmin`
- **Password**: Generated randomly (displayed during setup - check `.env` file)
- **Email**: `admin@example.com`

To view the admin password:
```bash
grep ADMIN_PASSWORD .env
```

**IMPORTANT:** Change the admin password after first login!

### Creating Users

As an admin, you create users via the admin API endpoint:

```bash
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

**Request Body:**
- `username` (required) - Unique username
- `email` (required) - Valid email address
- `password` (required) - Minimum 8 characters
- `is_admin` (optional) - Set to `true` to create an admin user, default: `false`

**Success Response (201 Created):**
```json
{
  "id": "uuid",
  "username": "newuser",
  "email": "newuser@example.com",
  "is_admin": false,
  "created_at": "timestamp",
  "updated_at": "timestamp"
}
```

**Note:** Public registration is disabled by default. To enable self-registration, set `ALLOW_REGISTRATION=true` in `.env` (not recommended for production).

### Listing All Users

```bash
curl -k -X GET https://localhost:9443/api/users \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

**Response:**
```json
[
  {
    "id": "uuid",
    "username": "user1",
    "email": "user1@example.com",
    "is_admin": false,
    "created_at": "2025-12-08T10:00:00Z",
    "updated_at": "2025-12-08T10:00:00Z"
  }
]
```

### Deleting a User

```bash
curl -k -X DELETE https://localhost:9443/api/users/{user_id} \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

**Important:** This permanently deletes the user and all their access keys. Their buckets and objects are NOT deleted - consider reassigning or deleting them first.

### Viewing User Details

```bash
# Get specific user
curl -k -X GET https://localhost:9443/api/users/{user_id} \
  -H "Authorization: Bearer $ADMIN_TOKEN"

# View current admin user
curl -k -X GET https://localhost:9443/api/users/me \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

## Policy Management

### Policy Lifecycle

1. Create policy document
2. Validate and create policy
3. Attach to users
4. Monitor and audit
5. Update as needed
6. Detach before deletion

### Creating a Policy

```bash
curl -k -X POST https://localhost:9443/api/policies \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "ReadOnlyS3Access",
    "description": "Allows read-only access to S3 buckets",
    "document": "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":[\"s3:GetObject\",\"s3:ListBucket\"],\"Resource\":[\"*\"]}]}"
  }'
```

### Common Policy Templates

#### Read-Only Access
```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "ReadOnlyAccess",
      "Effect": "Allow",
      "Action": [
        "s3:GetObject",
        "s3:ListBucket",
        "objectstore:GetObject",
        "objectstore:ListBucket"
      ],
      "Resource": ["*"]
    }
  ]
}
```

#### Full Access to Specific Bucket
```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "FullBucketAccess",
      "Effect": "Allow",
      "Action": ["s3:*"],
      "Resource": ["mybucket/*"]
    }
  ]
}
```

#### Upload-Only Access
```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "UploadOnly",
      "Effect": "Allow",
      "Action": ["s3:PutObject"],
      "Resource": ["uploads/*"]
    }
  ]
}
```

#### Deny Deletion
```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "AllowReadWrite",
      "Effect": "Allow",
      "Action": ["s3:GetObject", "s3:PutObject"],
      "Resource": ["*"]
    },
    {
      "Sid": "DenyDelete",
      "Effect": "Deny",
      "Action": ["s3:DeleteObject"],
      "Resource": ["*"]
    }
  ]
}
```

### Attaching Policies to Users

```bash
curl -k -X POST https://localhost:9443/api/policies/users/{user_id}/attach \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "policy_id": "{policy_uuid}"
  }'
```

### Detaching Policies

```bash
curl -k -X DELETE https://localhost:9443/api/policies/users/{user_id}/detach/{policy_id} \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

### Updating Policies

```bash
curl -k -X PUT https://localhost:9443/api/policies/{policy_id} \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "description": "Updated description"
  }'
```

### Deleting Policies

Policies can only be deleted if not attached to any users:

```bash
# First, detach from all users
curl -k -X DELETE https://localhost:9443/api/policies/users/{user_id}/detach/{policy_id} \
  -H "Authorization: Bearer $ADMIN_TOKEN"

# Then delete the policy
curl -k -X DELETE https://localhost:9443/api/policies/{policy_id} \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

### Policy Best Practices

1. **Start Restrictive**: Begin with minimal permissions and add as needed
2. **Use Deny Carefully**: Explicit denies cannot be overridden
3. **Document Policies**: Always add meaningful descriptions
4. **Test Before Deployment**: Test policies in non-production environments
5. **Regular Audits**: Review attached policies quarterly
6. **Version Control**: Keep policy documents in git
7. **Naming Convention**: Use descriptive names (e.g., `ReadOnly-S3-Production`)

## Access Key Management

### Viewing User Access Keys

As an admin, you can view any user's access keys:

```bash
# This requires direct database access
docker exec objectstore-db psql -U objectstore -d objectstore \
  -c "SELECT id, user_id, access_key, is_active, created_at, last_used_at FROM access_keys WHERE user_id = '{user_uuid}';"
```

**Note:** Secret key hashes are never displayed for security.

### Revoking User Access Keys

Admins can revoke any access key:

```bash
curl -k -X DELETE https://localhost:9443/api/access-keys/{key_id} \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

### Access Key Limits

- **Per User Limit:** 5 active keys maximum
- **Purpose:** Prevents resource exhaustion and key sprawl
- **Enforcement:** Automatic at creation time

### Monitoring Access Key Usage

```sql
-- Recently used keys
SELECT
  u.username,
  ak.access_key,
  ak.last_used_at,
  ak.created_at
FROM access_keys ak
JOIN users u ON ak.user_id = u.id
WHERE ak.is_active = true
ORDER BY ak.last_used_at DESC NULLS LAST
LIMIT 20;

-- Unused keys (over 90 days)
SELECT
  u.username,
  ak.access_key,
  ak.created_at,
  ak.last_used_at
FROM access_keys ak
JOIN users u ON ak.user_id = u.id
WHERE ak.is_active = true
  AND (ak.last_used_at IS NULL OR ak.last_used_at < NOW() - INTERVAL '90 days');
```


## System Monitoring

## Storage Backend Management

### Per-Bucket Storage Backends

Each bucket can use a different storage backend - either local filesystem or S3-compatible storage.

**Available Backends:**
- `local` - Store files on the local filesystem
- `s3` - Store files in S3-compatible storage (AWS S3, MinIO, etc.)

### Viewing Bucket Storage Backends

```bash
# List all buckets with their storage backends and S3 configurations
docker exec objectstore-db psql -U objectstore -d objectstore \
  -c "SELECT b.name, b.storage_backend, b.region, s.name as s3_config FROM buckets b LEFT JOIN s3_configurations s ON b.s3_config_id = s.id;"
```

### S3 Configuration Management

The system supports multiple S3 configurations, allowing buckets to use different S3-compatible storage providers (AWS S3, MinIO, DigitalOcean Spaces, etc.).

#### Default S3 Configuration (.env)

The `.env` file provides a default S3 configuration that will be used when:
1. No S3 configurations exist in the database
2. A bucket uses S3 storage but doesn't specify a configuration

```bash
# Default S3 Configuration in .env
S3_ENDPOINT=s3.amazonaws.com
S3_REGION=us-east-1
S3_ACCESS_KEY_ID=your_access_key
S3_SECRET_ACCESS_KEY=your_secret_key
S3_BUCKET_PREFIX=objectstore-
S3_USE_SSL=true
S3_FORCE_PATH_STYLE=false  # Set to true for MinIO
```

#### Managing S3 Configurations (Database)

You can manage multiple S3 configurations through the Web UI or API:

**Via Web UI:**
1. Log in as admin
2. Navigate to "S3 Configs" in the sidebar
3. Click "Add Configuration"
4. Fill in the configuration details:
   - Name (e.g., "AWS Production", "MinIO Dev")
   - Endpoint
   - Region
   - Access Key ID
   - Secret Access Key
   - Optional: Bucket Prefix
   - SSL/TLS toggle
   - Force Path Style (for MinIO)
   - Set as default checkbox

**Via API:**

```bash
# List all S3 configurations
curl -k -X GET https://localhost:9443/api/s3-configs \
  -H "Authorization: Bearer $ADMIN_TOKEN"

# Create new S3 configuration
curl -k -X POST https://localhost:9443/api/s3-configs \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "MinIO Production",
    "endpoint": "minio.example.com:9000",
    "region": "us-east-1",
    "access_key_id": "minioadmin",
    "secret_access_key": "minioadmin",
    "bucket_prefix": "prod-",
    "use_ssl": true,
    "force_path_style": true,
    "is_default": false
  }'

# Update S3 configuration
curl -k -X PUT https://localhost:9443/api/s3-configs/{config_id} \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "MinIO Production Updated",
    "is_default": true
  }'

# Delete S3 configuration (fails if in use by buckets)
curl -k -X DELETE https://localhost:9443/api/s3-configs/{config_id} \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

#### Creating Buckets with S3 Configurations

When creating a bucket with S3 storage backend, you can specify which S3 configuration to use:

```bash
# Create bucket using specific S3 configuration
curl -k -X POST https://localhost:9443/api/buckets \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "my-s3-bucket",
    "storage_backend": "s3",
    "s3_config_id": "uuid-of-s3-config",
    "region": "us-east-1"
  }'

# Create bucket using default S3 configuration
curl -k -X POST https://localhost:9443/api/buckets \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "my-s3-bucket",
    "storage_backend": "s3",
    "region": "us-east-1"
  }'
```

#### S3 Configuration Priority

The system uses this priority order for S3 configurations:
1. **Bucket-specific configuration**: If bucket has `s3_config_id` set, use that configuration
2. **Default database configuration**: If a configuration is marked `is_default=true`, use it
3. **Environment configuration**: Fall back to `.env` settings

### Storage Backend Best Practices

1. **Local Storage**:
   - Best for development and testing
   - Fast access for small to medium deployments
   - Ensure sufficient disk space
   - Regular backups required

2. **S3 Storage**:
   - Best for production and large-scale deployments
   - Highly scalable and durable
   - Geographic redundancy available
   - Pay for what you use

3. **Multiple S3 Configurations**:
   - Use different configurations for different environments (dev/staging/prod)
   - Separate configurations for different regions or providers
   - One config for AWS S3, another for MinIO, another for DigitalOcean Spaces
   - Hot-reload: Changes take effect immediately without restart

4. **Mixed Approach**:
   - Use local storage for frequently accessed data
   - Use S3 storage for archival and large files
   - Use different S3 providers based on cost/performance needs
   - Choose per-bucket based on access patterns


### Health Checks

```bash
# API health
curl -k https://localhost:9443/health

# Database health
docker exec objectstore-db pg_isready -U objectstore

# Service status
docker compose ps
```

### Database Queries

#### System Statistics
```sql
-- User count
SELECT COUNT(*) as user_count FROM users;

-- Bucket count
SELECT COUNT(*) as bucket_count FROM buckets;

-- Object count and total size
SELECT
  COUNT(*) as object_count,
  pg_size_pretty(SUM(size)) as total_size
FROM objects;

-- Active access keys
SELECT COUNT(*) as active_keys FROM access_keys WHERE is_active = true;

-- Policy count
SELECT COUNT(*) as policy_count FROM policies;
```

#### Storage Per User
```sql
SELECT
  u.username,
  COUNT(DISTINCT b.id) as bucket_count,
  COUNT(o.id) as object_count,
  pg_size_pretty(COALESCE(SUM(o.size), 0)) as total_size
FROM users u
LEFT JOIN buckets b ON u.id = b.owner_id
LEFT JOIN objects o ON b.id = o.bucket_id
GROUP BY u.id, u.username
ORDER BY SUM(o.size) DESC NULLS LAST;
```

### Log Monitoring

```bash
# Backend logs
docker logs objectstore-backend --tail 100 -f

# Database logs
docker logs objectstore-db --tail 100 -f

# All services
docker compose logs -f
```

## Security

### TLS/SSL Certificates

#### Production Deployment

Replace self-signed certificates with CA-signed certificates:

```bash
# 1. Obtain certificates from CA (Let's Encrypt, DigiCert, etc.)

# 2. Replace certificate files
cp /path/to/production/cert.pem certs/backend/backend.crt
cp /path/to/production/key.pem certs/backend/backend.key

# 3. Restart backend
docker compose restart backend
```

#### Certificate Rotation

```bash
# Generate new certificates
python3 setup.py

# Restart services to pick up new certs
docker compose restart backend postgres
```

### Database Security

#### Change Default Passwords

Update `docker-compose.yml`:

```yaml
postgres:
  environment:
    POSTGRES_PASSWORD: STRONG_RANDOM_PASSWORD_HERE

backend:
  environment:
    DB_PASSWORD: SAME_STRONG_PASSWORD_HERE
```

#### Enable PostgreSQL SSL

Already enabled! Verify:

```bash
docker exec objectstore-db psql -U objectstore -d objectstore \
  -c "SHOW ssl;"
```

Should return: `on`

### JWT Secret Rotation

1. Update `docker-compose.yml`:
```yaml
backend:
  environment:
    JWT_SECRET: new_random_secret_here
```

2. Restart backend:
```bash
docker compose restart backend
```

**Note:** This invalidates all existing tokens. Users must log in again.

### Security Auditing

#### Failed Login Attempts
```sql
-- Requires audit logging (see Audit Logging section)
SELECT * FROM audit_log
WHERE action = 'login_failed'
ORDER BY created_at DESC
LIMIT 50;
```

#### Policy Changes
```sql
-- Requires audit logging
SELECT * FROM audit_log
WHERE action IN ('policy_created', 'policy_updated', 'policy_deleted')
ORDER BY created_at DESC;
```

## Backup and Recovery

### Database Backup

```bash
# Full backup
docker exec objectstore-db pg_dump -U objectstore objectstore > backup_$(date +%Y%m%d).sql

# Compressed backup
docker exec objectstore-db pg_dump -U objectstore objectstore | gzip > backup_$(date +%Y%m%d).sql.gz
```

### Object Storage Backup

```bash
# Backup all buckets
tar -czf buckets_backup_$(date +%Y%m%d).tar.gz ./data/buckets/

# Or use rsync for incremental backups
rsync -av --progress ./data/buckets/ /backup/location/
```

### Restore from Backup

```bash
# Database restore
docker exec -i objectstore-db psql -U objectstore objectstore < backup_20251208.sql

# Object storage restore
tar -xzf buckets_backup_20251208.tar.gz -C ./data/
```

### Automated Backup Script

```bash
#!/bin/bash
# backup.sh - Automated backup script

BACKUP_DIR="/backups"
DATE=$(date +%Y%m%d_%H%M%S)

# Backup database
docker exec objectstore-db pg_dump -U objectstore objectstore | \
  gzip > "${BACKUP_DIR}/db_${DATE}.sql.gz"

# Backup object storage
tar -czf "${BACKUP_DIR}/buckets_${DATE}.tar.gz" ./data/buckets/

# Keep only last 7 days of backups
find "${BACKUP_DIR}" -name "db_*.sql.gz" -mtime +7 -delete
find "${BACKUP_DIR}" -name "buckets_*.tar.gz" -mtime +7 -delete

echo "Backup completed: ${DATE}"
```

Schedule with cron:
```bash
# Run daily at 2 AM
0 2 * * * /path/to/backup.sh >> /var/log/objectstore-backup.log 2>&1
```

## Troubleshooting

### Common Issues

#### Service Won't Start

```bash
# Check logs
docker compose logs backend

# Common causes:
# - Certificate files missing
# - Database not ready
# - Port already in use

# Solution: Ensure certs exist
python3 setup.py

# Wait for database
docker compose up -d postgres
sleep 10
docker compose up -d backend
```

#### Database Connection Errors

```bash
# Test database connectivity
docker exec objectstore-db pg_isready -U objectstore

# Check PostgreSQL logs
docker logs objectstore-db

# Verify SSL configuration
docker exec objectstore-db psql -U objectstore -d objectstore \
  -c "SHOW ssl;"
```

#### High Memory Usage

```sql
-- Check largest tables
SELECT
  schemaname,
  tablename,
  pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) AS size
FROM pg_tables
WHERE schemaname = 'public'
ORDER BY pg_total_relation_size(schemaname||'.'||tablename) DESC;

-- Vacuum database
VACUUM ANALYZE;
```

#### Slow Queries

```sql
-- Enable query logging (in postgresql.conf or via docker)
log_min_duration_statement = 1000  -- Log queries over 1 second

-- View slow queries
SELECT
  pid,
  now() - query_start as duration,
  query
FROM pg_stat_activity
WHERE state = 'active'
  AND now() - query_start > interval '5 seconds';
```

### Performance Tuning

#### PostgreSQL

Update `docker-compose.yml`:

```yaml
postgres:
  command: >
    postgres
    -c shared_buffers=256MB
    -c effective_cache_size=1GB
    -c maintenance_work_mem=64MB
    -c work_mem=16MB
```

#### Backend

Adjust environment variables:

```yaml
backend:
  environment:
    STORAGE_MAX_FILE_SIZE: 5368709120  # 5GB
    STORAGE_MAX_CONCURRENT_UPLOADS: 10
```

## Related Documentation

- [Security Overview](../security/security-overview.md)
- [Production Checklist](../deployment/production-checklist.md)
- [API Documentation](../api/)
