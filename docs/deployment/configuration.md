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

`/data/secrets.env` is parsed, never executed: generated (hex) values are stored
as `KEY=value`, anything else as `KEY='value'` with embedded quotes written as
`'\''`, so passwords may contain spaces, quotes, `$`, backticks or `#`. It stays
`root`-only (0600); the backend itself runs as the unprivileged `bkt` user
(uid 10001) and receives the values via its environment. The Postgres role and
database are created if missing and the role password is re-synced from
`DB_PASSWORD` on every boot.

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
| `AUTH_RATE_LIMIT` | `5` (omnibus: `60`, Helm: `20`) | Max login/register attempts per minute per IP |
| `AUTH_REFRESH_RATE_LIMIT` | `30` | Max token refreshes per minute per IP (separate budget, so users sharing an egress IP aren't logged out by each other's logins) |

## Proxies, rate limits, audit & metrics

| Variable | Default | Purpose |
|---|---|---|
| `TRUSTED_PROXIES` | _(empty)_ | Comma-separated CIDRs/IPs of reverse proxies whose `X-Forwarded-For` is trusted for rate limiting. Empty = trust none (the socket address is used). Behind a proxy, set it to the proxy's address — otherwise all clients share the proxy's rate-limit bucket |
| `S3_RATE_LIMIT` | `0` | Per-IP requests per minute on the S3 API listener; `0` disables (the console/auth limiter is separate) |
| `AUDIT_RETENTION_DAYS` | `90` | Days to keep audit log rows; older rows are pruned periodically. `<= 0` disables pruning |
| `METRICS_TOKEN` | _(empty)_ | When set, `GET /metrics` requires `Authorization: Bearer <token>`. Leave unset only if the metrics endpoint is network-isolated (it exposes bucket/object/user counts). In production an unset token logs a warning at startup (the endpoint keeps working so existing Prometheus setups don't break) |
| `SWAGGER_ENABLED` | `true` in development, `false` in production | Serve the Swagger UI at `/api/docs/` |
| `HSTS_MAX_AGE` | `31536000` | `Strict-Transport-Security` max-age (seconds) sent by the console when TLS is enabled or terminated upstream; not sent for `localhost`/IP hosts. `0` disables |

Access logs redact credential-bearing query parameters (`X-Amz-Signature`,
`X-Amz-Credential`, `X-Amz-Security-Token`, SigV2 `Signature`/`AWSAccessKeyId`,
and SSO `code`/`state`/`token`), so presigned URLs and authorization codes don't
leak through container logs.

## Webhooks

| Variable | Default | Purpose |
|---|---|---|
| `WEBHOOK_ALLOWED_HOSTS` | _(empty)_ | Comma-separated hostnames, IPs and/or CIDRs that webhook deliveries may reach even though they are internal. By default bucket webhooks can only reach public addresses: loopback, private (RFC 1918 / IPv6 ULA), link-local (incl. `169.254.169.254` cloud metadata), CGNAT `100.64.0.0/10`, multicast, unspecified and reserved ranges — and IPv4-mapped/embedded IPv6 forms of them — are refused, checked on the address actually dialed (so DNS rebinding cannot bypass it). A listed **hostname** may resolve to any address; a listed **IP/CIDR** may be reached under any name. Example: `hooks.internal,10.20.0.0/16` |

Webhook deliveries never follow redirects (a 3xx counts as a failed delivery)
and ignore `HTTP_PROXY`/`HTTPS_PROXY` (a proxy would connect on bkt's behalf,
bypassing the address check), so a receiver must be reachable directly. Any
user with `s3:PutBucketNotification` on a bucket can set its webhook URL, so
keep the allowlist as narrow as possible: every such user can make bkt POST to
the listed hosts.

## Secrets

| Variable | Default | Purpose |
|---|---|---|
| `JWT_SECRET` | _generated_ | Signs JWT access/refresh tokens |
| `ENCRYPTION_KEY` | _generated_ | Encrypts stored S3 credentials — **must stay stable** |
| `DB_PASSWORD` | _generated_ | PostgreSQL password (managed internally in the omnibus) |

In the **multi-container** setup these are required (no auto-generation); `setup.py`
creates them in `.env`. Re-running `setup.py` keeps every existing value and only
adds missing keys. It never rotates `DB_PASSWORD` or a valid `JWT_SECRET` /
`ENCRYPTION_KEY`; an unusable one (empty, placeholder, the old public default,
or too short) is replaced, and the old value is kept as a decrypt-only key
(`ENCRYPTION_KEY_PREVIOUS`, or `ENCRYPTION_LEGACY_JWT_SECRET` for a JWT secret
that encrypted data while no `ENCRYPTION_KEY` was set). A backup of the previous
`.env` is written whenever it changes anything. Existing certificates are kept unless you pass `--regenerate-certs`.

| `ENCRYPTION_KEY_PREVIOUS` | — | Comma-separated **decrypt-only** keys you rotated away from. Never used to encrypt; exempt from the strength rules |
| `ENCRYPTION_LEGACY_JWT_SECRET` | — | **Decrypt-only**: a former `JWT_SECRET` under which credentials were encrypted (installs that ran without `ENCRYPTION_KEY`), e.g. the old public default after you replace it. Exempt from the strength rules |
| `ENCRYPTION_REENCRYPT_ON_STARTUP` | `true` | At startup, re-encrypt (in the background) stored credentials that are only readable through a decrypt-only key or are in the legacy v1 format |

**Secret rules (every environment, not just production):** the backend refuses
to start when `JWT_SECRET` is empty, shorter than 32 characters, the old public
default `dev_jwt_secret_change_in_production`, or still a `<placeholder>` from
`.env.example` (a forgeable JWT secret is an authentication bypass). Generate
values with `openssl rand -hex 32`.

`ENCRYPTION_KEY`, when set, is refused only when it is certainly not a real key:
the public default above, a literal `<placeholder>`, or a template value
containing `generated_by_setup`, `change_me`, `change_in_production`,
`replace_me` or `your_encryption_key` (case, `-`, `_`, `.` and spaces ignored).
A **short** existing key (< 32 characters) or one containing `<`/`>` inside a
real value only logs a **warning**: it already encrypts your stored
credentials, and changing it outright would make them unreadable — rotate it
instead (below).

`ENCRYPTION_KEY` is **required in production**. In development it may be left
empty: stored S3 credentials are then encrypted with a key derived from
`JWT_SECRET` and a warning is logged. Adding an `ENCRYPTION_KEY` later is safe:
credentials written under the `JWT_SECRET` fallback stay readable (as long as
`JWT_SECRET` is unchanged) and are re-encrypted under `ENCRYPTION_KEY` at the
next startup.

Decryption tries, in order: `ENCRYPTION_KEY` (or `JWT_SECRET` when it is
unset), each `ENCRYPTION_KEY_PREVIOUS` entry, the current `JWT_SECRET` (when a
dedicated `ENCRYPTION_KEY` is set), then `ENCRYPTION_LEGACY_JWT_SECRET`.
Encryption always uses the first.

### Rotating `ENCRYPTION_KEY`

1. Generate a new key: `openssl rand -hex 32`.
2. Set `ENCRYPTION_KEY=<new>` and `ENCRYPTION_KEY_PREVIOUS=<old>` (append to
   any existing list, comma-separated) and restart **every** replica.
3. At startup each replica re-encrypts access-key secrets, S3 configuration
   credentials and bucket webhook secrets that only the old key can read
   (one transaction per row; a value is written only after it decrypted and
   its new ciphertext verified; values nothing can decrypt are left alone and
   reported). Watch the log for
   `Stored credential re-encryption: N re-encrypted ...` and then
   `no stored credential needs a decrypt-only key any more`.
4. Once that message appears, remove `ENCRYPTION_KEY_PREVIOUS` and restart. Keep a backup of the old
   key until you have confirmed S3 configurations and access keys still work.

### Replacing the old default `JWT_SECRET`

Releases before 1.4 shipped `JWT_SECRET=dev_jwt_secret_change_in_production`
in `docker-compose.yml`; it is now rejected (anyone can forge tokens with it).
If you ran without `ENCRYPTION_KEY`, your stored credentials were encrypted
under it. To upgrade without losing them:

```bash
JWT_SECRET=$(openssl rand -hex 32)
ENCRYPTION_KEY=$(openssl rand -hex 32)
ENCRYPTION_LEGACY_JWT_SECRET=dev_jwt_secret_change_in_production   # decrypt-only
```

Restart; credentials are re-encrypted under the new `ENCRYPTION_KEY`
(the same log lines as above appear), after which
`ENCRYPTION_LEGACY_JWT_SECRET` can be removed. All existing login sessions end
(tokens were signed with the old secret). The same applies to any other
`JWT_SECRET` you change while stored credentials depend on it.

Set `ENCRYPTION_REENCRYPT_ON_STARTUP=false` to skip the startup pass (each
examined value costs one PBKDF2 derivation; without decrypt-only keys only
legacy-format values are examined).

## TLS & ports

| Variable | Default | Purpose |
|---|---|---|
| `TLS_ENABLED` | `true` (omnibus; bare binary: `false`) | `false` serves plain HTTP (use behind a TLS-terminating proxy — and set `TRUSTED_PROXIES`). Production (`GO_ENV=production`) refuses to start without TLS unless `TLS_TERMINATED_UPSTREAM=true` |
| `TLS_TERMINATED_UPSTREAM` | `false` | Declare that a reverse proxy / ingress terminates TLS in front of bkt. Satisfies the production TLS requirement with `TLS_ENABLED=false` (logged at startup). Only set it when the listeners are reachable solely through that proxy |
| `TLS_CERT_FILE` / `TLS_KEY_FILE` | _auto self-signed_ | Mount your own cert/key to override the generated pair |
| `CONSOLE_PORT` | `9443` | Web UI + REST API listener |
| `S3_API_PORT` | `9000` | S3-compatible API listener |
| `S3_PUBLIC_ENDPOINT` | _(empty)_ | Browser-facing base URL of the S3 listener (e.g. `https://s3.example.com`), embedded in console-generated presigned URLs. Empty = derived from the console request host + the S3 API port — right whenever console and S3 share a hostname |
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
| `S3_BUCKETS` | — | Comma-separated buckets to auto-provision (link/create) at startup. They always use these `.env` S3 settings |
| `S3_SSE` | `false` | Request SSE-S3 (AES256) server-side encryption on every object written through the **external S3 backend**. Local-backend bytes are NOT encrypted by bkt — use disk-level encryption (LUKS/dm-crypt) |
| `CONTENT_TYPE_ENFORCEMENT` | `false` | Opt-in magic-byte content-type detection; rejects "unsafe" types. Off by default because S3's contract treats Content-Type as client-declared metadata |

> You don't have to set the `S3_*` variables at all — you can add S3 configurations
> and create S3-backed buckets from the **admin UI at runtime** instead.

## SSO (optional)

| Variable | Default | Purpose |
|---|---|---|
| `OIDC_ISSUER_URL` / `OIDC_CLIENT_ID` | — | Generic OIDC login (authorization code + PKCE) with any OpenID Connect IdP; setting both enables it |
| `OIDC_CLIENT_SECRET` | — | Optional; makes bkt a confidential client (PKCE is always used) |
| `OIDC_REDIRECT_URL` | `https://localhost:9443/api/auth/oidc/callback` | Backend callback registered at the IdP |
| `OIDC_SCOPES` / `OIDC_PROVIDER_NAME` | `openid profile email` / `SSO` | Scopes to request; login button label |
| `OIDC_USERNAME_CLAIM` / `OIDC_GROUPS_CLAIM` / `OIDC_POLICIES_CLAIM` | — / `groups` / `policies` | Claims mapping (see [SSO guide](../guides/sso-setup.md)) |
| `OIDC_ADMIN_GROUP` / `OIDC_USER_GROUP` | — | Group that grants admin; group required for access (optional) |
| `OIDC_POLICIES_AUTHORITATIVE` | `true` if `OIDC_POLICIES_CLAIM` is set, else `false` | A *missing* policies claim also clears the user's policies (a present claim, even empty, always replaces them) |
| `OIDC_LINK_BY_EMAIL` | `false` | Link new subjects to existing OIDC accounts by verified, IdP-asserted email (exactly one match required) |
| `OIDC_EXPECTED_ISSUER` | — | The issuer the discovery document and ID tokens must carry when it legitimately differs from `OIDC_ISSUER_URL` (e.g. Keycloak reached via an internal hostname while `KC_HOSTNAME` is public). Unset: must equal `OIDC_ISSUER_URL` |
| `GOOGLE_OIDC_ENABLED` | `false` | Enable Google OIDC login |
| `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` | — | Google OAuth credentials |
| `GOOGLE_ALLOWED_DOMAINS` | — | Comma-separated Workspace domains allowed to sign in (`hd` claim and email domain must both match). **Unset: new Google users are only auto-provisioned when `ALLOW_REGISTRATION=true`** |
| `VAULT_JWT_AUDIENCE` | `object-storage` | Required `aud` for Vault JWT login |
| `VAULT_JWT_ISSUER` | — | If set, required `iss` for Vault JWT login |
| `GOOGLE_REDIRECT_URL` | `https://localhost:9443/api/auth/google/callback` | OAuth callback |
| `VAULT_OIDC_ENABLED` | `false` | Enable generic OIDC login (any standard OIDC IdP — Vault, Keycloak, …; endpoints come from the provider's discovery document) |
| `VAULT_OIDC_CLIENT_ID` / `VAULT_OIDC_PROVIDER_URL` / `VAULT_OIDC_REDIRECT_URL` | — | OIDC client ID, provider/issuer URL, and backend callback URL |
| `VAULT_OIDC_SCOPES` | `openid profile` | Space-separated OIDC scopes to request |
| `VAULT_OIDC_EXPECTED_ISSUER` | — | Issuer Vault's discovery document and ID tokens must carry when it differs from `VAULT_OIDC_PROVIDER_URL` (Vault's `api_addr` differs from the URL bkt uses, e.g. dev mode's `http://127.0.0.1:8200/...`). Unset: must equal `VAULT_OIDC_PROVIDER_URL` |
| `VAULT_POLICIES_AUTHORITATIVE` | `false` | Vault JWT/OIDC: `false` = a `policies` claim replaces the user's policies only when it names at least one existing bkt policy (otherwise admin-assigned policies are kept); `true` = a present claim always replaces them (empty/unmatched removes all) |
| `FRONTEND_URL` | the console: `https://localhost:<CONSOLE_PORT>` (`http://` when `TLS_ENABLED=false`) | Base URL SSO flows redirect back to. Set it to the public console URL (e.g. `https://bkt.example.com`). It must be the same origin the user starts the login from — the SSO callback only accepts tokens for a login started in that browser tab |

## Notes for the omnibus

- The database connection (`DB_HOST`, `DB_PORT`, `DB_USER`, `DB_NAME`, `DB_SSL_MODE`)
  is managed internally (PostgreSQL on loopback) — don't set these.
- `GO_ENV` defaults to `production` when TLS is on (which enforces non-default
  secrets + TLS); it falls back to `development` when `TLS_ENABLED=false`.
- `FRONTEND_URL` defaults to `https://localhost:9443` (the console). Set it when
  the console is published under another host or port, or SSO logins land on
  the wrong address.
- The backend runs as the unprivileged `bkt` user (uid 10001). On start the
  entrypoint re-owns the bucket store once if an older (root-run) release
  created it. A TLS cert/key you mount that `bkt` can't read is copied into a
  private runtime directory; other mounted files (e.g.
  `GOOGLE_SERVICE_ACCOUNT_KEY_FILE`) must be readable by uid 10001.

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
