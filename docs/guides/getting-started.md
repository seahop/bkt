# Getting Started

This guide will help you get up and running with the Object Storage system in minutes.

## Prerequisites

- Docker 20.10+
- Docker Compose 2.0+
- Python 3.8+
- curl or similar HTTP client

## Quick Start

### 1. Clone and Setup

```bash
# Navigate to project directory
cd /path/to/objectstore

# Run the unified setup script (first time only)
python3 setup.py
```

This will:
- Generate admin credentials (username, password, email)
- Generate JWT secret
- Create `.env` file with all configuration
- Generate TLS certificates for all services

**IMPORTANT:** Save the admin credentials displayed by the setup script!

### 2. Start Services

```bash
# Start all services
docker compose up -d

# Verify services are running
docker compose ps
```

You should see:
- `objectstore-db` (PostgreSQL)
- `objectstore-backend` (Go API server)
- `objectstore-frontend` (React app)

### 3. Login as Admin

```bash
# Use the credentials from setup.py output
curl -k -X POST https://localhost:9443/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{
    "username": "testadmin",
    "password": "YOUR_PASSWORD_FROM_SETUP"
  }'
```

Save the `token` from the response - you'll need it for authentication.

### 4. Create a Bucket

```bash
# Replace YOUR_TOKEN with the token from step 3
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

### 5. Upload a File

```bash
# Create a test file
echo "Hello, Object Storage!" > test.txt

# Upload it
curl -k -X POST https://localhost:9443/api/buckets/my-first-bucket/objects \
  -H "Authorization: Bearer $TOKEN" \
  -F "key=test.txt" \
  -F "file=@test.txt"
```

### 6. Download the File

```bash
curl -k -X GET https://localhost:9443/api/buckets/my-first-bucket/objects/test.txt \
  -H "Authorization: Bearer $TOKEN" \
  -o downloaded.txt

cat downloaded.txt
# Output: Hello, Object Storage!
```

Congratulations! 🎉 You've successfully:
- ✅ Set up the object storage system with security
- ✅ Logged in as admin
- ✅ Created a bucket
- ✅ Uploaded and downloaded a file

## Next Steps

### For Users
- [User Guide](user-guide.md) - Learn about access keys, policies, and more
- [API Documentation](../api/README.md) - Explore all available endpoints

### For Administrators
- [Admin Guide](admin-guide.md) - User management, policies, and system configuration
- [Security Best Practices](../security/security-overview.md)

### For Developers
- [Developer Guide](developer-guide.md) - Integrate the API into your applications
- [Code Examples](../examples/code-examples.md) - SDKs and code samples

## Common Tasks

### Using the Web Interface

Open your browser and navigate to:
- Frontend: https://localhost:5173

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

JWT tokens expire after 24 hours. Login again to get a new token:

```bash
curl -k -X POST https://localhost:9443/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{
    "username": "testadmin",
    "password": "YOUR_PASSWORD"
  }'
```

## Environment Overview

### Services

- **Backend API**: https://localhost:9443
- **Frontend**: https://localhost:5173 (HTTPS enabled)
- **PostgreSQL**: localhost:5432

### Data Storage

- **Database**: `./data/postgres`
- **Object Files**: `./data/buckets`
- **Certificates**: `./certs`

### Configuration

Configuration is managed via the `.env` file generated by `setup.py`.

See the main README.md for environment variable details.

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
- **Username**: `testadmin`
- **Password**: Generated randomly (displayed during setup)
- **Email**: `admin@example.com`

**IMPORTANT:** Change the admin password after first login!

## Getting Help

- **Documentation**: See [docs/README.md](../README.md)
- **API Reference**: See [docs/api/](../api/)
- **Examples**: See [docs/examples/](../examples/)

## What's Next?

Now that you have the basics, explore more features:

- **Policies**: Create fine-grained access control policies
- **Public Buckets**: Share files publicly
- **Metadata**: Attach custom metadata to objects
- **Web Interface**: Use the React frontend at https://localhost:5173
- **Storage Backends**: Use local or S3 storage per bucket
- **Folder Organization**: Create virtual folders to organize files
- **S3 Compatibility**: Use AWS S3 SDKs (coming soon)

Happy storing! 🚀
