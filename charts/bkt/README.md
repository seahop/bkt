# bkt Helm chart

Deploys [bkt](https://bkt.tips) — the self-hosted S3-compatible object storage
gateway — plus a small in-chart PostgreSQL (official `postgres:16-alpine`
image; no third-party subcharts).

## Quick start

```bash
helm install bkt ./charts/bkt \
  --set backend.env.JWT_SECRET="$(openssl rand -hex 32)" \
  --set backend.env.ENCRYPTION_KEY="$(openssl rand -hex 32)" \
  --set backend.env.ADMIN_PASSWORD="$(openssl rand -base64 18)" \
  --set postgresql.auth.password="$(openssl rand -hex 16)"
```

Then follow the printed NOTES (port-forward the console on 9443, log in as
`admin`). **Record JWT_SECRET / ENCRYPTION_KEY / the passwords somewhere safe —
they must stay stable for the life of the deployment**, and a database backup
without ENCRYPTION_KEY cannot be fully restored.

## Images

| Value | Image | Notes |
|---|---|---|
| `backend.image.repository` | `ghcr.io/seahop/bkt-backend` | Console UI + REST + S3 API, **no bundled Postgres** (built from the repo's `Dockerfile --target backend`) |

Do **not** point the chart at the omnibus image (`ghcr.io/seahop/bkt`): it
bundles its own Postgres inside the container and will silently ignore the
chart's database, storing metadata ephemerally.

## Architecture choices

- **Database**: in-chart single-replica StatefulSet by default. Set
  `postgresql.enabled=false` and fill in `externalDatabase.*` for a managed DB
  (RDS/CloudSQL/etc.) — recommended for production.
- **Object storage**:
  - `STORAGE_BACKEND=local` (default): bytes live on the backend PVC
    (objects, archived versions under `.versions/`, multipart staging).
    **replicaCount must stay 1** unless `backend.persistence.accessMode` is
    `ReadWriteMany`; the chart refuses to render otherwise.
  - `STORAGE_BACKEND=s3`: pods are stateless (bytes on external S3);
    `replicaCount` can be raised freely — migrations are advisory-locked.
- **TLS**: enabled by default with a chart-generated self-signed cert (stable
  across upgrades). For real certificates set `backend.tls.existingSecret` to
  a `kubernetes.io/tls` secret (e.g. from cert-manager), or terminate TLS at
  the ingress with **both** `backend.tls.enabled=false` and
  `backend.tls.terminatedUpstream=true` (sets `TLS_TERMINATED_UPSTREAM=true`;
  production mode refuses plain HTTP without it, and the chart fails to render
  rather than letting the pod crash-loop). Only do this when the backend
  Service is reachable solely through the ingress.
- **Client IPs / rate limiting**: behind an ingress every request comes from
  the ingress controller. When `ingress.enabled=true` and
  `backend.env.TRUSTED_PROXIES` is empty, the chart trusts the private ranges
  (`10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,100.64.0.0/10,fc00::/7`) so each
  user gets their own bucket — narrow it to your ingress controller's pod CIDR
  (anything inside a trusted range can spoof `X-Forwarded-For`). Login is
  limited to `AUTH_RATE_LIMIT` (default 20/min per IP here) and token refresh
  has its own budget (`AUTH_REFRESH_RATE_LIMIT`, default 30/min).
- **Pod security**: the backend runs as uid/gid 10001 with a read-only root
  filesystem, all capabilities dropped and `RuntimeDefault` seccomp (PSS
  "restricted"). `fsGroup: 10001` with `fsGroupChangePolicy: OnRootMismatch`
  re-owns a PVC written by an older root-run release once. Volume types that
  ignore `fsGroup` (NFS, hostPath) must be chowned to `10001:10001` by hand
  (or by an init container running as root). The backend probes the storage
  root at startup and exits with that instruction if it cannot write there or
  read existing objects, instead of starting and failing every upload.
  `/tmp` is an `emptyDir` (upload staging) — size it with
  `backend.tmpDir.sizeLimit`.
- **Database TLS**: `externalDatabase.sslMode` (default `require`; use
  `verify-full` where the CA is trusted) is passed as `DB_SSL_MODE`. The
  **in-chart Postgres is not TLS-enabled** — the backend connects with
  `sslmode=disable` over the cluster network. Protect it with a
  NetworkPolicy, or use an external database for encrypted DB traffic.

## Ingress

Two hostnames are the intended shape:

```yaml
ingress:
  enabled: true
  className: nginx
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
  hosts:
    - host: bkt.example.com          # console UI + REST API
      paths: [{path: /, pathType: Prefix}]
  tls:
    - secretName: bkt-tls
      hosts: [bkt.example.com]
  s3:
    enabled: true
    host: s3.bkt.example.com         # S3-compatible API (path-style)
    annotations:
      nginx.ingress.kubernetes.io/proxy-body-size: "0"   # allow large uploads
    tls:
      - secretName: bkt-s3-tls
        hosts: [s3.bkt.example.com]

backend:
  env:
    S3_PUBLIC_ENDPOINT: https://s3.bkt.example.com   # presigned URLs embed this
    FRONTEND_URL: https://bkt.example.com            # SSO redirects
    CORS_ALLOWED_ORIGINS: https://bkt.example.com
    TRUSTED_PROXIES: 10.42.0.0/16                    # your ingress controller pod CIDR
```

When `backend.tls.enabled=true` (default) the upstream speaks HTTPS — add
`nginx.ingress.kubernetes.io/backend-protocol: "HTTPS"` to both ingresses.
To terminate TLS at the ingress instead, set `backend.tls.enabled=false` and
`backend.tls.terminatedUpstream=true`.

## Monitoring

`serviceMonitor.enabled=true` creates a Prometheus Operator ServiceMonitor for
`/metrics`. Set `backend.env.METRICS_TOKEN` (the backend logs a warning in
production when it is unset — the endpoint exposes bucket/object/user counts)
and give the scraper the same token via `serviceMonitor.bearerTokenSecret`.

The Swagger UI (`/api/docs/`) is off in production; set
`backend.env.SWAGGER_ENABLED=true` to serve it.

## Values worth knowing

| Value | Default | Meaning |
|---|---|---|
| `backend.env.JWT_SECRET` / `ENCRYPTION_KEY` / `ADMIN_PASSWORD` | — | **Required.** Stored in a Secret. `JWT_SECRET`/`ENCRYPTION_KEY` must be ≥ 32 chars (`openssl rand -hex 32`) |
| `backend.env.ENCRYPTION_KEY_PREVIOUS` / `ENCRYPTION_LEGACY_JWT_SECRET` | `""` | Decrypt-only keys for rotating `ENCRYPTION_KEY` (comma-separated retired keys) or a retired `JWT_SECRET` that encrypted credentials before `ENCRYPTION_KEY` existed. Stored in the Secret; no strength checks. Credentials are re-encrypted at startup — remove once the log says they are unused |
| `backend.env.WEBHOOK_ALLOWED_HOSTS` | `""` | Hostnames/IPs/CIDRs webhooks may reach despite being private (SSRF guard) |
| `backend.env.OIDC_EXPECTED_ISSUER` / `VAULT_OIDC_EXPECTED_ISSUER` | `""` | Issuer the IdP/Vault advertises when it differs from the URL bkt reaches it at |
| `backend.env.VAULT_POLICIES_AUTHORITATIVE` | `""` (false) | Vault `policies` claim always replaces bkt policies |
| `backend.tls.enabled` / `backend.tls.terminatedUpstream` | `true` / `false` | Backend TLS; set `false`/`true` to terminate TLS at the ingress |
| `backend.env.TRUSTED_PROXIES` | auto with ingress | CIDRs whose `X-Forwarded-For` is trusted (see above) |
| `backend.env.AUTH_RATE_LIMIT` / `AUTH_REFRESH_RATE_LIMIT` | `20` / `30` | Per-IP per-minute login / token-refresh budgets |
| `backend.env.SWAGGER_ENABLED` | `""` (off in production) | Serve Swagger UI at `/api/docs/` |
| `externalDatabase.sslMode` | `require` | `DB_SSL_MODE` for an external database |
| `postgresql.auth.password` | — | **Required** with the in-chart DB. |
| `backend.env.STORAGE_BACKEND` | `local` | `local` (PVC) or `s3` (external S3, stateless pods) |
| `backend.env.S3_SSE` | `false` | Request SSE-S3 (AES256) on writes through the S3 backend |
| `backend.env.S3_PUBLIC_ENDPOINT` | `""` | Browser-facing S3 URL for presigned links |
| `backend.env.AUDIT_RETENTION_DAYS` | `90` | Audit log retention |
| `backend.env.METRICS_TOKEN` | `""` | Bearer-gate `/metrics` |
| `backend.persistence.size` | `50Gi` | Object storage PVC |
| `backend.replicaCount` | `1` | See scaling note above |

## Upgrades

`helm upgrade` rolls the backend automatically when config/secrets change
(checksum annotations). The self-signed TLS secret and the PVCs persist across
upgrades; PVCs also survive `helm uninstall` (delete them explicitly to
destroy data).
