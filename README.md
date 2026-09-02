<div align="center">
  <img src="docs/images/logo.png" alt="bkt logo" width="500">
  <h1>bkt</h1>
  <p>A self-hosted S3-compatible object storage gateway with multi-backend support, policy-based access control, and a modern web interface.</p>
  <p><a href="https://bkt.tips">https://bkt.tips</a></p>
</div>

## Overview

bkt is a unified object storage system that provides a single interface to manage files across multiple storage backends (local filesystem, AWS S3, MinIO, DigitalOcean Spaces, etc.). It includes user authentication, fine-grained access policies, and an S3-compatible API that enables filesystem mounting with tools like s3fs-fuse.

## Quick start

The fastest way to run bkt is the single-container **omnibus** image. It bundles the API, the web UI, and PostgreSQL, and boots with zero configuration — local storage, self-signed TLS, and an auto-generated admin password printed to the logs:

```bash
docker run -d --name bkt \
  -p 9443:9443 \
  -p 9000:9000 \
  -v bkt-data:/data \
  ghcr.io/seahop/bkt
```

- **`9443`** — web console + REST API → open **https://localhost:9443** (accept the self-signed cert)
- **`9000`** — S3-compatible API for `aws` / `s3fs`

Then:

- Get the first-boot admin password: `docker logs bkt | grep -A2 "admin credentials"` — or set your own with `-e ADMIN_PASSWORD=...`.
- Create an access key in the UI (Profile) and point S3 tools at `https://localhost:9000`.

All state (database, objects, certs, secrets) lives in the `bkt-data` volume, so the container itself is disposable. For multi-node / external-Postgres deployments, use the Helm chart in [`charts/bkt`](charts/bkt) or [docker-compose.prod.yml](docker-compose.prod.yml).

### Configuration

Everything is configured with **environment variables passed at run time** (`docker run -e …`) — never at build time, and you never need a `.env` file for the single container. Anything you omit gets a safe default; secrets are auto-generated on first boot and persisted to the volume. Common ones:

```bash
docker run -d -p 9443:9443 -p 9000:9000 -v bkt-data:/data \
  -e ADMIN_PASSWORD='choose-a-strong-password' \
  -e CORS_ALLOWED_ORIGINS='https://bkt.example.com' \
  -e TLS_ENABLED=false \                 # serve HTTP behind your own proxy
  -e S3_ENABLED=true \
  -e S3_ACCESS_KEY_ID='AKIA...' -e S3_SECRET_ACCESS_KEY='...' \
  -e S3_BUCKETS='my-existing-bucket' \   # auto-provision a bucket on startup
  ghcr.io/seahop/bkt
```

`ADMIN_PASSWORD`, `JWT_SECRET`, and `ENCRYPTION_KEY` are read on **first boot** and then stored in the volume (stored values win on later boots). See the full list in **[docs/deployment/configuration.md](docs/deployment/configuration.md)** and the annotated [.env.example](.env.example).

### Deployment options

The same codebase ships three ways. Full details in **[docs/deployment/deployment-options.md](docs/deployment/deployment-options.md)**.

| | Command | Containers | For |
|---|---|---|---|
| **Omnibus** (`docker pull`) | `docker run … ghcr.io/seahop/bkt` | 1 (backend+UI+DB) | single-node self-hosting |
| **Clone + compose** | `git clone …` → `docker compose up` | 3 (db, backend, frontend) | developing on the code (hot-reload) |
| **compose.prod** | pulls `bkt-backend` + Postgres | 2 (backend, external DB) | scale-out / HA |
| **Kubernetes (Helm)** | `helm install bkt charts/bkt` | backend + in-chart Postgres | Kubernetes — see [charts/bkt/README.md](charts/bkt/README.md) |

The Helm chart uses the `bkt-backend` image (not the omnibus image, which bundles its own Postgres) and supports TLS via self-signed certs, an existing secret, or cert-manager, an optional dedicated S3-API ingress, and a Prometheus ServiceMonitor.

All three run the same Go binary with the embedded UI — they are different *packagings* of one codebase (built from the same root `Dockerfile`).

## Features

Full usage details for the data-management features live in the **[Feature guide](docs/guides/features.md)**.

### Storage
- **Multi-backend support** - Store objects on local disk or any S3-compatible service
- **Per-bucket backend selection** - Choose storage location when creating each bucket
- **Object versioning** - Version history with restore and delete markers ([guide](docs/guides/features.md#object-versioning))
- **Lifecycle expiry** - Expire current objects and purge noncurrent versions after N days ([guide](docs/guides/features.md#lifecycle-expiry))
- **Per-bucket quotas** - Cap a bucket's total size; over-quota writes are rejected ([guide](docs/guides/features.md#storage-quotas))
- **WORM retention** - Deletion protection for versioned buckets ([guide](docs/guides/features.md#retention-worm))
- **Replication** - One-way mirroring into another bkt bucket, including one backed by a different S3 provider ([guide](docs/guides/features.md#replication-bucket-mirroring))
- **SSE-S3 pass-through** - Request AES256 server-side encryption on the external S3 backend ([guide](docs/guides/features.md#server-side-encryption))
- **Virtual folder hierarchy** - Organize objects with folder-like paths
- **Large file support** - Async uploads with progress tracking for large files

### Security
- **JWT authentication** - Secure token-based auth with refresh-token rotation and reuse detection
- **SSO** - Google OIDC plus generic OIDC for any standard IdP (validated with Keycloak) - see [SSO Setup](docs/guides/sso-setup.md)
- **Policy-based access control** - IAM-style policies for fine-grained permissions
- **Groups** - Attach policies to groups of users ([guide](docs/guides/features.md#groups))
- **Access keys** - Named S3-compatible credentials with optional expiry and read-only flag
- **Temporary credentials** - Short-lived S3 key pairs via bkt-STS ([guide](docs/guides/features.md#temporary-credentials-bkt-sts))
- **Audit log** - Filterable admin audit trail of logins, key, policy, and bucket operations
- **TLS everywhere** - HTTPS for all services with auto-generated certificates

### Web Interface
- **Dual-pane file browser** - Split view for easier file organization
- **Drag-and-drop** - Move files between folders and panes
- **Presigned share links** - Time-limited download URLs from the Share action ([guide](docs/guides/features.md#presigned-share-links))
- **Version history** - Browse, restore, or permanently delete object versions
- **Bucket settings** - Versioning, lifecycle, quota, retention, webhooks, and replication per bucket
- **Search and filters** - Find files by name, extension, size, date, or folder depth
- **Context menus** - Right-click for quick actions
- **Dark mode** - Modern dark theme throughout

### S3 Compatibility
- **S3 REST API** - Works with AWS SDKs and S3 tools
- **User metadata & object tagging** - `x-amz-meta-*` headers and the `?tagging` subresource ([guide](docs/guides/features.md#user-metadata-and-tags))
- **Versioning & lifecycle** - Standard AWS API shapes (`?versioning`, `?versions`, `?lifecycle`, `?versionId=`)
- **Multipart uploads** - Including ListMultipartUploads and UploadPartCopy
- **Filesystem mounting** - Mount buckets as local drives with s3fs-fuse
- **AWS Signature V4** - Standard S3 authentication, including client-side presigned URLs

### Integrations & Observability
- **Webhook event notifications** - Signed JSON POSTs on object created/removed events ([guide](docs/guides/features.md#event-notifications-webhooks))
- **Prometheus metrics** - `/metrics` endpoint, optionally bearer-token gated

## Tech Stack

- **Backend**: Go with Gin framework
- **Frontend**: React with TypeScript and Tailwind CSS
- **Database**: PostgreSQL
- **Deployment**: Docker, Docker Compose, and Kubernetes (Helm)

## Quick Start

### Prerequisites

- Docker and Docker Compose
- Python 3 (for setup script)

### Installation

1. Clone or navigate to the repository:
```bash
cd bkt
```

2. Run the setup script to generate credentials and SSL certificates:
```bash
python3 setup.py
```

This generates:
- Admin credentials in `.env`
- Database password in `.env`
- JWT secret in `.env`
- SSL/TLS certificates in `certs/`

**Save the admin credentials displayed by the setup script.**

3. Start all services:
```bash
docker compose up --build
```

4. Access the application:
   - Web UI: https://localhost:5173
   - API: https://localhost:9443
   - Database: localhost:5432

The application uses self-signed certificates. Accept browser warnings or trust the CA certificate (see `certs/README.md`).

### Stop Services

```bash
docker compose down
```

### Reset Everything

```bash
docker compose down -v
rm -rf data/
```

## S3 Filesystem Mounting

Mount buckets as local filesystems using s3fs-fuse:

```bash
# Create credentials file (get keys from the web UI Profile page)
echo "YOUR_ACCESS_KEY:YOUR_SECRET_KEY" > ~/.bkt
chmod 600 ~/.bkt

# Mount a bucket
s3fs my-bucket ~/mnt/my-bucket \
  -o url=https://localhost:9000 \
  -o use_path_request_style \
  -o passwd_file=~/.bkt \
  -o no_check_certificate

# Unmount
fusermount -u ~/mnt/my-bucket
```

See [docs/guides/MOUNTING.md](docs/guides/MOUNTING.md) for detailed instructions.

## Project Structure

```
bkt/
├── backend/                 # Go backend API
│   ├── cmd/                 # Application entry point
│   ├── internal/            # Core application code
│   │   ├── api/             # HTTP handlers
│   │   ├── auth/            # Authentication (JWT, OAuth, Vault)
│   │   ├── middleware/      # Request middleware
│   │   ├── models/          # Database models
│   │   └── storage/         # Storage backends (local, S3)
│   └── db/                  # Database migrations
├── frontend/                # React frontend
│   └── src/
│       ├── components/      # Reusable UI components
│       ├── pages/           # Page components
│       ├── services/        # API client
│       └── store/           # State management
├── docs/                    # Documentation
│   ├── api/                 # API reference
│   ├── guides/              # User guides
│   ├── security/            # Security documentation
│   ├── deployment/          # Deployment guides
│   └── examples/            # Code examples
├── certs/                   # SSL certificates (generated)
├── data/                    # Persistent data (generated)
│   ├── postgres/            # Database files
│   └── buckets/             # Local object storage
├── docker/                  # Docker scripts
├── setup.py                 # Setup and certificate generator
└── docker-compose.yml       # Container orchestration
```

## Development

### Backend

The backend uses Air for hot reload:

```bash
docker compose logs -f backend
```

### Frontend

The frontend uses Vite with HMR:

```bash
docker compose logs -f frontend
```

### Database

```bash
docker exec -it bkt-db psql -U objectstore -d objectstore
```

## API Examples

```bash
# Login
curl -k -X POST https://localhost:9443/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"YOUR_PASSWORD"}'

# Save token
export TOKEN="your_jwt_token"

# Create bucket
curl -k -X POST https://localhost:9443/api/buckets \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"my-bucket","storage_backend":"local"}'

# Upload file
curl -k -X POST https://localhost:9443/api/buckets/my-bucket/objects \
  -H "Authorization: Bearer $TOKEN" \
  -F "file=@myfile.txt" \
  -F "key=myfile.txt"

# List objects
curl -k -X GET https://localhost:9443/api/buckets/my-bucket/objects \
  -H "Authorization: Bearer $TOKEN"
```

Use `-k` to accept self-signed certificates in development.

## Documentation

- [Getting Started](docs/guides/getting-started.md)
- [Feature Guide](docs/guides/features.md) - versioning, lifecycle, quotas, retention, share links, webhooks, groups, temporary credentials, replication, encryption
- [Full API Reference](docs/api/API.md)
- [S3fs Mounting Guide](docs/guides/MOUNTING.md)
- [Kubernetes (Helm) Deployment](charts/bkt/README.md)
- [Security Overview](docs/security/security-overview.md)
- [Production Checklist](docs/deployment/production-checklist.md)
- [Backup & Restore](docs/deployment/backup-restore.md)
- [Documentation Index](docs/DOCUMENTATION_INDEX.md)

## License

Apache-2.0
