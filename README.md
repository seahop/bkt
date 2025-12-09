<div align="center">
  <img src="docs/images/logo.png" alt="bkt logo" width="500">
  <h1>bkt</h1>
  <p>A high-performance S3-compatible object storage system with user management, bucket operations, and policy enforcement.</p>
</div>

## Features

- User authentication and management
- Bucket creation and management with per-bucket storage backend selection
- Multiple S3 configuration support (AWS S3, MinIO, DigitalOcean Spaces, etc.)
- Object upload/download/delete operations with virtual folder support
- Policy-based access control (PBAC)
- Access key and secret key generation
- **S3-compatible REST API with filesystem mounting support (s3fs-fuse)**
- Modern dark-mode web interface with file management

## Tech Stack

- **Backend**: Go with Gin framework
- **Frontend**: React with TypeScript and Tailwind CSS
- **Database**: PostgreSQL
- **Deployment**: Docker & Docker Compose

## Quick Start

### Prerequisites

- Docker and Docker Compose installed
- Git (optional)

### Development Setup

1. Clone or navigate to the repository:
```bash
cd bkt
```

2. Run the setup script to generate credentials and SSL certificates:
```bash
python3 setup.py
```

This will generate:
- Admin credentials (username, password) in `.env`
- Database password (randomly generated) in `.env`
- JWT secret (randomly generated) in `.env`
- SSL/TLS certificates for all services in `certs/`
- Certificate documentation in `certs/README.md`

**IMPORTANT**: Save the admin and database credentials displayed by the setup script!

3. Start all services:
```bash
docker compose up --build
```

4. Access the application:
   - Frontend: https://localhost:5173
   - Backend API: https://localhost:9443
   - PostgreSQL: localhost:5432

**Note**: The application uses HTTPS with self-signed certificates. You may need to accept security warnings in your browser or trust the CA certificate (see `certs/README.md`).

### Stop Services

```bash
docker-compose down
```

### Clean Everything (including data)

```bash
docker-compose down -v
rm -rf data/
```

## S3 Filesystem Mounting

Mount your buckets as local filesystems using s3fs-fuse:

### Quick Setup

1. **Create credentials file** (replace with your actual access key and secret key from the web UI):
```bash
echo "YOUR_ACCESS_KEY:YOUR_SECRET_KEY" > ~/.bkt
chmod 600 ~/.bkt
```

2. **Create mount point**:
```bash
mkdir -p ~/bkt-mounts/my-bucket
```

3. **Mount a bucket**:
```bash
s3fs my-bucket ~/bkt-mounts/my-bucket \
  -o url=https://localhost:9443 \
  -o use_path_request_style \
  -o passwd_file=~/.bkt \
  -o ssl_verify_hostname=0 \
  -o no_check_certificate
```

4. **Unmount when done**:
```bash
fusermount -u ~/bkt-mounts/my-bucket
```

For detailed setup instructions, troubleshooting, and advanced options, see [docs/MOUNTING.md](docs/MOUNTING.md).

## Project Structure

```
.
├── backend/              # Go backend API
│   ├── cmd/             # Application entry points
│   ├── internal/        # Private application code
│   ├── db/              # Database schemas and migrations
│   └── Dockerfile       # Backend container config
├── frontend/            # React frontend
│   ├── src/            # Source code
│   ├── public/         # Static assets
│   └── Dockerfile      # Frontend container config
├── certs/               # SSL/TLS certificates (git-ignored)
│   ├── ca/             # Certificate Authority
│   ├── backend/        # Backend certificates
│   ├── frontend/       # Frontend certificates
│   ├── postgres/       # PostgreSQL certificates
│   └── README.md       # Certificate documentation
├── docker/             # Docker-specific scripts
│   └── postgres-init-ssl.sh  # PostgreSQL SSL initialization
├── data/               # Persistent data (git-ignored)
│   ├── postgres/       # Database files
│   └── buckets/        # Object storage
├── setup.py            # Initial setup script
├── docker-compose.yml  # Multi-container orchestration
└── PROJECT_PLAN.md     # Detailed project plan

```

## Development

### Backend Development

The backend uses Air for hot reload. Changes to Go files will automatically restart the server.

```bash
cd backend
# Backend logs
docker-compose logs -f backend
```

### Frontend Development

The frontend uses Vite with HMR (Hot Module Replacement). Changes are reflected instantly.

```bash
cd frontend
# Frontend logs
docker-compose logs -f frontend
```

### Database Access

```bash
# Access PostgreSQL CLI
docker exec -it bkt-db psql -U objectstore -d objectstore
```

## API Documentation
### Quick API Examples

```bash
# Login with admin account (created by setup.py)
# Password is in .env file: grep ADMIN_PASSWORD .env
curl -k -X POST https://localhost:9443/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"testadmin","password":"YOUR_PASSWORD_FROM_ENV"}'

# Save the token
export TOKEN="YOUR_JWT_TOKEN_FROM_RESPONSE"

# Create bucket
curl -k -X POST https://localhost:9443/api/buckets \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"my-bucket","storage_backend":"local"}'

# Admin: Create a new user
curl -k -X POST https://localhost:9443/api/users \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"username":"newuser","email":"user@example.com","password":"securepass123","is_admin":false}'
```

**Note:** Use `-k` flag with curl to accept self-signed certificates in development.

For complete API documentation, see [docs/](docs/).


