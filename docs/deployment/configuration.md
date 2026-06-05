# Configuration (Environment Variables)

bkt is configured entirely through environment variables. There is **no build-time
configuration** — you never bake secrets into the image. Instead:

- **Single container (omnibus):** pass variables to `docker run -e VAR=value …`.
  Anything you don't set is given a safe default; secrets are auto-generated on
  first boot and stored in the `/data` volume.
- **Docker Compose / Helm:** set the same variables in `.env` (compose) or the
  chart's values/secret (Helm).

> ⚠️ **Never pass secrets as Docker build args.** Build args are baked into image
> layers and are readable by anyone who pulls the image. Always pass secrets at
> **run time** (`-e`) or via a compose/Helm secret.

## How secrets behave in the omnibus

`ADMIN_PASSWORD`, `JWT_SECRET`, `ENCRYPTION_KEY`, and `DB_PASSWORD` are read from the
environment on **first boot**; if unset they are generated. They are then persisted
to `/data/secrets.env`, and on later boots the **stored values take precedence** (so
the instance is stable across restarts). To rotate them, edit `/data/secrets.env`
or start from a fresh volume.

If `ADMIN_PASSWORD` is generated, it is printed to the container logs once:

```bash
docker logs bkt | grep -A2 "admin credentials"
```

## Common variables

| Variable | Default | Purpose |
|---|---|---|
| `ADMIN_USERNAME` | `admin` | Initial admin username |
| `ADMIN_PASSWORD` | _generated_ | Initial admin password (set it to avoid the random one) |
| `ADMIN_EMAIL` | `admin@localhost` | Initial admin email |
| `ALLOW_REGISTRATION` | `false` | Allow users to self-register |
| `AUTH_RATE_LIMIT` | `5` (omnibus: `60`) | Max login attempts per minute per IP |

## Secrets

| Variable | Default | Purpose |
|---|---|---|
| `JWT_SECRET` | _generated_ | Signs JWT access/refresh tokens |
| `ENCRYPTION_KEY` | _generated_ | Encrypts stored S3 credentials — **must stay stable** |
| `DB_PASSWORD` | _generated_ | PostgreSQL password (managed internally in the omnibus) |

In the **multi-container** setup these are required (no auto-generation); `setup.py`
creates them in `.env`.

## TLS & ports

| Variable | Default | Purpose |
|---|---|---|
| `TLS_ENABLED` | `true` | `false` serves plain HTTP (use behind a TLS-terminating proxy) |
| `TLS_CERT_FILE` / `TLS_KEY_FILE` | _auto self-signed_ | Mount your own cert/key to override the generated pair |
| `CONSOLE_PORT` | `9443` | Web UI + REST API listener |
| `S3_API_PORT` | `9000` | S3-compatible API listener |
| `CORS_ALLOWED_ORIGINS` | localhost dev origins | Comma-separated browser origins allowed to call the API |

## Storage backend

| Variable | Default | Purpose |
|---|---|---|
| `STORAGE_BACKEND` | `local` | `local` or `s3` — default backend for new buckets |
| `STORAGE_ROOT` | `/data/buckets` | Where local objects are stored |
| `S3_ENABLED` | `false` | Enable the AWS S3 backend |
| `S3_ENDPOINT` | `s3.amazonaws.com` | S3 endpoint (set for MinIO/Spaces/etc.) |
| `S3_REGION` | `us-east-1` | S3 region |
| `S3_ACCESS_KEY_ID` / `S3_SECRET_ACCESS_KEY` | — | AWS credentials |
| `S3_BUCKET_PREFIX` | _(empty)_ | Optional prefix for the real S3 bucket names |
| `S3_USE_SSL` | `true` | Use HTTPS to the S3 endpoint |
| `S3_FORCE_PATH_STYLE` | `false` | `true` for MinIO and other non-AWS S3 |
| `S3_BUCKETS` | — | Comma-separated buckets to auto-provision (link/create) at startup |

> You don't have to set the `S3_*` variables at all — you can add S3 configurations
> and create S3-backed buckets from the **admin UI at runtime** instead.

## SSO (optional)

| Variable | Default | Purpose |
|---|---|---|
| `GOOGLE_OIDC_ENABLED` | `false` | Enable Google OIDC login |
| `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` | — | Google OAuth credentials |
| `GOOGLE_REDIRECT_URL` | `https://localhost:9443/api/auth/google/callback` | OAuth callback |
| `VAULT_OIDC_ENABLED` | `false` | Enable Vault OIDC login |
| `VAULT_OIDC_CLIENT_ID` / `VAULT_OIDC_PROVIDER_URL` / `VAULT_OIDC_REDIRECT_URL` | — | Vault OIDC settings |
| `FRONTEND_URL` | `https://localhost` | Base URL SSO flows redirect back to |

## Notes for the omnibus

- The database connection (`DB_HOST`, `DB_PORT`, `DB_USER`, `DB_NAME`, `DB_SSL_MODE`)
  is managed internally (PostgreSQL on loopback) — don't set these.
- `GO_ENV` defaults to `production` when TLS is on (which enforces non-default
  secrets + TLS); it falls back to `development` when `TLS_ENABLED=false`.

## Example

```bash
docker run -d --name bkt \
  -p 9443:9443 -p 9000:9000 \
  -v bkt-data:/data \
  -e ADMIN_PASSWORD='choose-a-strong-password' \
  -e CORS_ALLOWED_ORIGINS='https://bkt.example.com' \
  -e S3_ENABLED=true \
  -e S3_ACCESS_KEY_ID='AKIA...' \
  -e S3_SECRET_ACCESS_KEY='...' \
  -e S3_BUCKETS='my-existing-bucket' \
  ghcr.io/seahop/bkt
```

See [.env.example](../../.env.example) for the full annotated list.
