# Getting Started

This guide will help you get up and running with bkt in minutes.

## Prerequisites

- Docker 20.10+
- `curl` or similar HTTP client
- (Docker Compose 2.0+ and Python 3.8+ only for the multi-container option)

bkt exposes two ports:

- **`9443`** — web console + REST API (browser, `curl`)
- **`9000`** — S3-compatible API (`aws`, `s3fs`)

## Quick Start

The two options below get you running fastest. For the full picture — including the
production multi-container / Helm path and how the image is built from the repo — see
[Deployment Options](../deployment/deployment-options.md). Pick one, then continue to
**First Steps**.

### Option A — Single container (fastest)

The omnibus image bundles the API, web UI, and PostgreSQL. It boots with zero
configuration: local storage, self-signed TLS, and an auto-generated admin
password printed to the logs.

```bash
docker run -d --name bkt \
  -p 9443:9443 \
  -p 9000:9000 \
  -v bkt-data:/data \
  ghcr.io/seahop/bkt

# Grab the first-boot admin password (or set your own with -e ADMIN_PASSWORD=...)
docker logs bkt | grep -A2 "admin credentials"
```

All state (database, objects, certs, secrets) lives in the `bkt-data` volume.

You can override defaults with `-e` at run time — e.g. set your own admin password
(`-e ADMIN_PASSWORD=...`), enable S3 (`-e S3_ENABLED=true -e S3_ACCESS_KEY_ID=... -e S3_SECRET_ACCESS_KEY=...`),
or serve plain HTTP behind a proxy (`-e TLS_ENABLED=false`). Secrets you don't set are
generated on first boot. Never pass secrets as build args — only at run time. See the
full list in [Configuration](../deployment/configuration.md).

### Option B — Docker Compose (separate Postgres)

```bash
# Run the unified setup script (first time only) — generates the .env,
# admin credentials, JWT secret, encryption key, and TLS certificates.
python3 setup.py

# Start the stack, then check status
docker compose up -d
docker compose ps
```

**IMPORTANT:** Save the admin credentials displayed by the setup script!

You should see `bkt-db` (PostgreSQL) and `bkt-backend` (API + embedded web UI).
The dev compose also runs a `bkt-frontend` container that serves the UI with
hot-reload on `https://localhost:5173`; in production the backend serves the UI
itself on the console port.

## First Steps

### 1. Login as Admin

```bash
# Use the credentials from setup.py output
curl -k -X POST https://localhost:9443/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{
    "username": "admin",
    "password": "YOUR_PASSWORD_FROM_SETUP"
  }'
```

Save the `token` from the response - you'll need it for authentication.

### 2. Create a Bucket

```bash
# Replace YOUR_TOKEN with the token from step 1
export TOKEN="eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."

curl -k -X POST https://localhost:9443/api/buckets \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "my-first-bucket",
    "is_public": false,
    "storage_backend": "local"
  }'
```

You can choose `"local"` or `"s3"` for the storage backend - each bucket can use a different backend!

### 3. Upload a File

```bash
# Create a test file
echo "Hello, Object Storage!" > test.txt

# Upload it
curl -k -X POST https://localhost:9443/api/buckets/my-first-bucket/objects \
  -H "Authorization: Bearer $TOKEN" \
  -F "key=test.txt" \
  -F "file=@test.txt"
```

### 4. Download the File

```bash
curl -k -X GET https://localhost:9443/api/buckets/my-first-bucket/objects/test.txt \
  -H "Authorization: Bearer $TOKEN" \
  -o downloaded.txt

cat downloaded.txt
# Output: Hello, Object Storage!
```

Congratulations! 🎉 You've successfully:
- ✅ Set up bkt with security
- ✅ Logged in as admin
- ✅ Created a bucket
- ✅ Uploaded and downloaded a file

A few things you can do with that file right away (details in the
[Feature guide](features.md)):

- **Share it** — the Share action in the console (or
  `POST /api/buckets/{name}/objects/presign`) creates a time-limited download
  link that works without login. See
  [Presigned share links](features.md#presigned-share-links).
- **Version it** — enable versioning in the bucket's Settings and overwrites
  and deletes become recoverable, with per-file history and restore. See
  [Object versioning](features.md#object-versioning).
- **Script against it safely** — mint short-lived, optionally read-only S3
  credentials from Profile → Temporary credentials. See
  [Temporary credentials](features.md#temporary-credentials-bkt-sts).

## Next Steps

### For Users
- [Feature Guide](features.md) - Versioning, lifecycle, quotas, share links, webhooks, and more
- [Admin Guide](admin-guide.md) - Learn about access keys, policies, and more
- [API Documentation](../api/API.md) - Explore all available endpoints

### For Administrators
- [Admin Guide](admin-guide.md) - User management, policies, and system configuration
- [Security Best Practices](../security/security-overview.md)

### For Developers
- [Full API Reference](../api/API.md) - Every REST and S3-compatible endpoint
- [cURL Examples](../examples/curl-examples.md) - Command-line examples
- [S3fs Mounting](MOUNTING.md) - Mount buckets as a local filesystem

## Common Tasks

### Using the Web Interface

Open the **console** in your browser:
- Single container / production: `https://localhost:9443`
- Dev compose (hot-reload): `https://localhost:5173` (also published on `8443`)

You may need to accept the self-signed certificate warning.

### List Your Buckets

```bash
curl -k -X GET https://localhost:9443/api/buckets \
  -H "Authorization: Bearer $TOKEN"
```

### List Objects in a Bucket

```bash
curl -k -X GET https://localhost:9443/api/buckets/my-first-bucket/objects \
  -H "Authorization: Bearer $TOKEN"
```

### Create Folders

You can organize files in folders:

```bash
# Upload a file to a folder
curl -k -X POST https://localhost:9443/api/buckets/my-first-bucket/objects \
  -H "Authorization: Bearer $TOKEN" \
  -F "key=documents/report.pdf" \
  -F "file=@report.pdf"
```

Folders are virtual - they're represented as prefixes in the object key.

### Delete an Object

```bash
curl -k -X DELETE https://localhost:9443/api/buckets/my-first-bucket/objects/test.txt \
  -H "Authorization: Bearer $TOKEN"
```

On a bucket with [versioning](features.md#object-versioning) enabled, this
hides the object behind a delete marker instead of destroying it — you can
restore it from the file's History in the console.

### Delete a Bucket

```bash
# Bucket must be empty first
curl -k -X DELETE https://localhost:9443/api/buckets/my-first-bucket \
  -H "Authorization: Bearer $TOKEN"
```

## Using Access Keys (Alternative to JWT)

Access keys are useful for scripts, CLI tools, and long-running applications.

### Generate an Access Key

```bash
curl -k -X POST https://localhost:9443/api/access-keys \
  -H "Authorization: Bearer $TOKEN"
```

The body is optional — you can pass `{"name": "...", "expires_in_days": 30,
"read_only": true}` to create a named, expiring, or read-only key. For
short-lived credentials (default 1 hour, max 12) use
[temporary credentials](features.md#temporary-credentials-bkt-sts) instead.

**Response:**
```json
{
  "access_key": "AKGAUJicHqerbIjN9m7WSCCyRtZJ0",
  "secret_key": "SKMUprmvSZ_eBYwIgOKRENHXHBIiGOxX_xOm8FHNmmBP_4xDPQY41TeA",
  "warning": "Save your secret key now. It will not be shown again!"
}
```

⚠️ **IMPORTANT:** Save the `secret_key` - it will never be shown again!

### Use Access Keys for Authentication

```bash
# Set your keys
export ACCESS_KEY="AKGAUJicHqerbIjN9m7WSCCyRtZJ0"
export SECRET_KEY="SKMUprmvSZ_eBYwIgOKRENHXHBIiGOxX_xOm8FHNmmBP_4xDPQY41TeA"

# Use HTTP Basic Auth
curl -k -X GET https://localhost:9443/api/buckets \
  -u "$ACCESS_KEY:$SECRET_KEY"
```

## Troubleshooting

### Service Not Starting

```bash
# Check logs
docker compose logs backend
docker compose logs postgres

# Restart services
docker compose restart
```

### SSL Certificate Errors

The system uses self-signed certificates for development. Use `-k` flag with curl:

```bash
curl -k https://localhost:9443/health
```

For browsers, you can import the CA certificate (`certs/ca/ca.crt`) or accept the security warning.

See `certs/README.md` for instructions on trusting the CA certificate.

### Connection Refused

Make sure services are running:

```bash
docker compose ps

# Should show all services as "Up"
```

### Invalid Token

Access tokens are short-lived (15 minutes by default, configurable with
`ACCESS_TOKEN_EXPIRY`); when one expires — in the web console or in scripts —
simply log in again for a new token:

```bash
curl -k -X POST https://localhost:9443/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{
    "username": "admin",
    "password": "YOUR_PASSWORD"
  }'
```

## Environment Overview

### Endpoints

- **Console (web UI + REST API)**: `https://localhost:9443`
- **S3-compatible API**: `https://localhost:9000`
- **PostgreSQL**: internal only (loopback in the omnibus; the `bkt-db` container in compose)

### Data Storage

- **Single container**: everything (Postgres data, objects, certs, secrets) lives under `/data` in the `bkt-data` volume
- **Docker Compose**: database in `./data/postgres`, objects in `./data/buckets`, certs in `./certs`

### Configuration

The single container runs with sensible defaults and needs no config. For the
compose path, configuration is managed via the `.env` file generated by
`setup.py`. See the main `README.md` and `.env.example` for environment variables.

## Health Check

Verify the system is healthy:

```bash
curl -k https://localhost:9443/health
```

**Response:**
```json
{
  "status": "healthy"
}
```

## Default Admin Account

The setup script creates a default admin account:
- **Username**: `admin`
- **Password**: Generated randomly (displayed during setup)
- **Email**: `admin@example.com`

**IMPORTANT:** Change the admin password after first login!

## Getting Help

- **Documentation Index**: See [docs/DOCUMENTATION_INDEX.md](../DOCUMENTATION_INDEX.md)
- **API Reference**: See [docs/api/](../api/)
- **Examples**: See [docs/examples/](../examples/)

## What's Next?

Now that you have the basics, explore more features (most are covered in
depth in the [Feature guide](features.md)):

- **Policies**: Create fine-grained access control policies
- **Versioning & Retention**: Keep object history, restore versions, and protect data with WORM retention
- **Lifecycle & Quotas**: Auto-expire objects and cap bucket sizes
- **Share Links**: Generate time-limited presigned download URLs
- **Webhooks**: Get notified on object created/removed events
- **Replication**: Mirror a bucket into another bkt bucket
- **Public Buckets**: Share files publicly
- **Storage Backends**: Use local or AWS S3 storage per bucket
- **Folder Organization**: Create virtual folders to organize files
- **S3 Compatibility**: Mount buckets with `s3fs` or use the AWS CLI / SDKs against the S3 endpoint on port `9000` — see [S3fs Mounting](MOUNTING.md)

Happy storing! 🚀
