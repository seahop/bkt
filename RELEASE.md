# bkt Release & Deployment Reference

This document covers testing, transferring the repo to the bkt-storage org, publishing releases, and how users deploy the full stack.

---

## Running the Smoke Tests

The smoke test script covers every feature end-to-end: auth, S3 API compatibility, multipart, presigned URLs, pagination, bulk delete, Prometheus metrics, and policy enforcement. It creates its own test data and cleans up after itself.

```bash
# Make sure bkt is running first
docker compose up -d

# Run against localhost (reads ADMIN_PASSWORD from .env automatically)
./tests/smoke.sh

# Run against a specific instance
./tests/smoke.sh --endpoint https://bkt.example.com:9443 --username admin --password yourpass

# Keep test data around after run (for manual inspection)
CLEANUP=false ./tests/smoke.sh
```

Example output:
```
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  bkt Smoke Tests
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  Endpoint:  https://localhost:9443
  User:      admin
  Bucket:    bkt-smoke-1748123456

▸ Checking prerequisites
  ✔ curl found
  ✔ aws found
  ...
▸ S3 API — Multipart upload
  ✔ 10MB test file generated
  ✔ Multipart upload — 10MB file uploaded
  ✔ Multipart upload — HeadObject confirms correct size
  ✔ Multipart upload — downloaded content MD5 matches original

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  Smoke Test Results
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  Passed:  42
  Failed:  0
  Skipped: 0

All tests passed.
```

### Testing with AWS S3 as the storage backend

The smoke tests are backend-agnostic — the same script works whether bkt stores data locally or on AWS S3. To test with real AWS S3:

1. **Add credentials to `.env`** (never paste them in chat or commit to git):
   ```bash
   # In your .env file
   STORAGE_BACKEND=s3
   S3_ENABLED=true
   S3_ENDPOINT=s3.amazonaws.com
   S3_REGION=us-east-1
   S3_ACCESS_KEY_ID=AKIA...
   S3_SECRET_ACCESS_KEY=...
   ```

2. **Restart the stack** to pick up the new env vars:
   ```bash
   docker compose down && docker compose up -d
   ```

3. **Run the same smoke tests** — they'll now exercise the AWS S3 storage path:
   ```bash
   ./tests/smoke.sh
   ```

Everything uploaded through bkt will land in your AWS S3 account. The smoke test's cleanup step removes all test data when it finishes.

---

## Pre-Transfer Testing Checklist

Work through this before transferring the repo. All commands assume you're running locally via `docker compose up`.

### Security

- [ ] **Login locks out before bcrypt** — lock a user account via admin panel, attempt login with correct password, confirm 403 before bcrypt runs
- [ ] **Logout blacklists access token** — log in, copy the token, log out, attempt `GET /api/users/me` with the old token → expect 401
- [ ] **Logout blacklists refresh token** — log in, send `POST /api/auth/logout` with `{ "refresh_token": "..." }` in body, attempt `POST /api/auth/refresh` with that token → expect 401
- [ ] **Policy tri-state** — attach a policy that covers bucket A only, attempt access to bucket B → should deny without treating "no match" as explicit deny

### S3 API Compatibility

Test these with the AWS CLI pointed at your local instance:
```bash
export AWS_ACCESS_KEY_ID=<your-bkt-access-key>
export AWS_SECRET_ACCESS_KEY=<your-bkt-secret-key>
alias s3="aws s3 --endpoint-url https://localhost:9443 --no-verify-ssl"
alias s3api="aws s3api --endpoint-url https://localhost:9443 --no-verify-ssl"
```

- [ ] **ListObjectsV1 pagination** — upload >1000 objects, run `s3api list-objects --bucket my-bucket --max-keys 100`, confirm `IsTruncated: true` and `NextMarker` is present
- [ ] **ListObjectsV2** — `s3api list-objects-v2 --bucket my-bucket --max-keys 10`, confirm `KeyCount`, `NextContinuationToken`, `IsTruncated`
- [ ] **CopyObject** — `s3 cp s3://bucket/file.txt s3://bucket/copy-of-file.txt`, confirm copy exists
- [ ] **DeleteObjects (bulk)** — `s3 rm s3://bucket --recursive`, confirm all objects removed in one operation
- [ ] **Multipart upload** — `s3 cp large-file.bin s3://bucket/large-file.bin` (file >8MB, AWS SDK auto-uses multipart), confirm upload completes and object appears
- [ ] **Presigned URL (download)** — `s3 presign s3://bucket/file.txt --expires-in 3600`, copy the URL, `curl` it without any AWS credentials → expect file contents
- [ ] **Presigned URL (expired)** — wait past expiry or manually shorten `--expires-in`, confirm 403 after expiry

### Observability

- [ ] **Prometheus metrics** — `curl -k https://localhost:9443/metrics | grep bkt_` → confirm `bkt_http_requests_total`, `bkt_storage_buckets_total`, etc. are present
- [ ] **Storage gauges update** — create a bucket and upload a file, wait up to 60s, check `bkt_storage_buckets_total` and `bkt_storage_objects_total` have incremented

### API Documentation

- [ ] **Swagger UI loads** — open `https://localhost:9443/api/docs/` in browser, confirm all endpoints are listed
- [ ] **Try it out** — use the Swagger UI to authenticate (Authorize button, paste Bearer token) and call `GET /api/users/me`

### Container Builds (production target)

```bash
# Build production images locally before pushing
docker build --target production -t bkt-backend-test ./backend
docker build --target production -t bkt-frontend-test ./frontend
```

- [ ] Backend production image builds without errors
- [ ] Frontend production image builds without errors
- [ ] `docker run --rm bkt-backend-test ./main --help` exits cleanly (binary exists)

---

## Transferring the Repo

1. Go to the repo → **Settings → Danger Zone → Transfer ownership**
2. Enter `bkt-storage` as the destination organization
3. Confirm by typing the repo name

After transfer, the repo lives at `github.com/seahop/bkt`. All existing clone URLs redirect automatically for 30 days — update your local remote:

```bash
git remote set-url origin https://github.com/seahop/bkt.git
```

---

## Publishing the First Release

### 1. Make packages public (one-time setup)
After the first workflow run creates the packages:
- Go to `github.com/orgs/bkt-storage/packages`
- Click **bkt-backend** → Package Settings → Change visibility → **Public**
- Repeat for **bkt-frontend**

Or set the org default: **Org Settings → Packages → Default package visibility → Public**

### 2. Tag a release
```bash
git tag v1.0.0
git push origin v1.0.0
```

This triggers the GitHub Actions workflow (`docker-publish.yml`) which builds and pushes:

| Tag pushed | Images published |
|------------|-----------------|
| `v1.0.0` | `:1.0.0`, `:1.0`, `:1`, `:latest` |
| push to `main` | `:latest`, `:sha-<shortsha>` |

Monitor the build at: `github.com/seahop/bkt/actions`

### 3. Verify images are available
```bash
docker pull ghcr.io/seahop/bkt-backend:1.0.0
docker pull ghcr.io/seahop/bkt-frontend:1.0.0
```

---

## How Users Deploy

### Single container (simplest — recommended for most users)

The omnibus image bundles the API, web UI, and PostgreSQL. Zero config: local
storage, self-signed TLS, and an auto-generated admin password printed to logs.

```bash
# 9443 = web console + REST API,  9000 = S3-compatible API
docker run -d --name bkt \
  -p 9443:9443 \
  -p 9000:9000 \
  -v bkt-data:/data \
  ghcr.io/seahop/bkt

# First-boot admin password:
docker logs bkt | grep -A2 "admin credentials"
```

Open `https://localhost:9443`. Override the admin password with `-e ADMIN_PASSWORD=…`,
enable AWS S3 with the `S3_*` env vars, and mount real certs over `/data/certs` to
replace the self-signed pair. All state lives in the `bkt-data` volume; back it up
with `docker exec bkt pg_dump -U objectstore objectstore` plus the `buckets/` dir.

Pin a version: `ghcr.io/seahop/bkt:1.0.0`.

### Docker Compose (separate Postgres — for scale-out / external DB)

```bash
# 1. Get the production compose file
curl -O https://raw.githubusercontent.com/bkt-storage/bkt/main/docker-compose.prod.yml

# 2. Generate secrets and TLS certificates
python3 <(curl -s https://raw.githubusercontent.com/bkt-storage/bkt/main/setup.py)

# 3. Pull images (backend + postgres)
docker compose -f docker-compose.prod.yml pull

# 4. Start the stack
docker compose -f docker-compose.prod.yml up -d
```

Pin a specific version instead of latest:
```bash
BKT_VERSION=1.0.0 docker compose -f docker-compose.prod.yml pull
BKT_VERSION=1.0.0 docker compose -f docker-compose.prod.yml up -d
```

### Updating to a new version
```bash
BKT_VERSION=1.1.0 docker compose -f docker-compose.prod.yml pull
BKT_VERSION=1.1.0 docker compose -f docker-compose.prod.yml up -d
# Only containers whose image changed are recreated
```

### Kubernetes / Helm

```bash
# Add bitnami repo (for bundled PostgreSQL)
helm repo add bitnami https://charts.bitnami.com/bitnami
helm repo update

# Install from local chart
helm dependency update ./charts/bkt
helm install bkt ./charts/bkt \
  --set backend.env.JWT_SECRET=<secret> \
  --set backend.env.ENCRYPTION_KEY=<key> \
  --set backend.env.ADMIN_PASSWORD=<password> \
  --set postgresql.auth.password=<db-password>

# With AWS S3 as the storage backend
helm install bkt ./charts/bkt \
  --set backend.env.JWT_SECRET=<secret> \
  --set backend.env.ENCRYPTION_KEY=<key> \
  --set backend.env.ADMIN_PASSWORD=<password> \
  --set postgresql.auth.password=<db-password> \
  --set backend.env.STORAGE_BACKEND=s3 \
  --set backend.env.S3_ENABLED=true \
  --set backend.env.S3_REGION=us-east-1 \
  --set backend.env.S3_ACCESS_KEY_ID=<access-key> \
  --set backend.env.S3_SECRET_ACCESS_KEY=<secret-key>
```

**Better practice for Helm secrets** — use a gitignored values file:
```yaml
# values-secrets.yaml  ← add to .gitignore
backend:
  env:
    JWT_SECRET: "..."
    ENCRYPTION_KEY: "..."
    ADMIN_PASSWORD: "..."
    S3_ACCESS_KEY_ID: "..."
    S3_SECRET_ACCESS_KEY: "..."
postgresql:
  auth:
    password: "..."
```
```bash
helm install bkt ./charts/bkt -f values-prod.yaml -f values-secrets.yaml
```

### S3 Client Configuration (after deployment)

```bash
# Generate an access key from the bkt web UI (Settings page)
# then configure the AWS CLI to use bkt as an endpoint:

aws configure --profile bkt
# AWS Access Key ID: <bkt-access-key>
# AWS Secret Access Key: <bkt-secret-key>
# Default region: us-east-1
# Default output format: json

# Use it
aws s3 ls --endpoint-url https://your-server:9443 --profile bkt
aws s3 cp myfile.txt s3://my-bucket/ --endpoint-url https://your-server:9443 --profile bkt
```

---

## Key URLs (once running)

| URL | Description |
|-----|-------------|
| `https://<host>:9443/api/docs/` | Swagger UI — full API reference |
| `https://<host>:9443/metrics` | Prometheus metrics scrape endpoint |
| `https://<host>:9443/health` | Health check (DB connectivity) |
| `https://<host>:9443/ready` | Readiness probe |
| `https://<host>:9443/live` | Liveness probe |
| `https://<host>` | Web UI |

---

## What Each Image Contains

| Image | Contents |
|-------|----------|
| `ghcr.io/seahop/bkt-backend` | Go binary, statically compiled, Alpine base. Runs the REST API + S3-compatible API + Prometheus metrics. |
| `ghcr.io/seahop/bkt-frontend` | Nginx serving the compiled React/TypeScript SPA. No Node.js at runtime. |
| `postgres:16-alpine` | Standard upstream Postgres image — not published by bkt-storage. |

Each image is multi-arch (`linux/amd64` + `linux/arm64`), so it runs on standard x86 servers and ARM instances (AWS Graviton, Raspberry Pi, Apple Silicon via Rosetta).
