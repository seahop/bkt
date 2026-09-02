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
  a `kubernetes.io/tls` secret (e.g. from cert-manager), or set
  `backend.tls.enabled=false` and terminate TLS at the ingress
  (then also set `TRUSTED_PROXIES` so per-IP rate limiting sees real client IPs).

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
    TRUSTED_PROXIES: 10.0.0.0/8                      # your ingress pod CIDR
```

When `backend.tls.enabled=true` (default) the upstream speaks HTTPS — add
`nginx.ingress.kubernetes.io/backend-protocol: "HTTPS"` to both ingresses.

## Monitoring

`serviceMonitor.enabled=true` creates a Prometheus Operator ServiceMonitor for
`/metrics`. If you set `backend.env.METRICS_TOKEN`, give the scraper the same
token via `serviceMonitor.bearerTokenSecret`.

## Values worth knowing

| Value | Default | Meaning |
|---|---|---|
| `backend.env.JWT_SECRET` / `ENCRYPTION_KEY` / `ADMIN_PASSWORD` | — | **Required.** Stored in a Secret. |
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
