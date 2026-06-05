# Deployment Options

bkt has **one codebase** that is delivered in two forms: prebuilt images you pull
from a registry, and the source repository you clone. CI takes the repo and bakes it
into the published images.

> Think of it as a recipe vs. a finished meal: the git repo is the recipe and the dev
> kitchen; the published image is the finished meal you just reheat.

There are three ways to run bkt.

## 1. Single container — `docker pull` (recommended for most users)

```bash
docker run -d --name bkt \
  -p 9443:9443 \
  -p 9000:9000 \
  -v bkt-data:/data \
  ghcr.io/seahop/bkt

# first-boot admin password (or set your own with -e ADMIN_PASSWORD=...)
docker logs bkt | grep -A2 "admin credentials"
```

- **One image, one container.** `ghcr.io/seahop/bkt` (the **omnibus**) bundles the Go
  backend *with the UI compiled in* **+ PostgreSQL** + an entrypoint that runs both.
- **No repo, no `.env`, no `setup.py`.** On first boot it auto-generates secrets, a
  self-signed TLS cert, and the admin password into the `/data` volume.
- The web UI is on the console port **`9443`**; the S3-compatible API is on **`9000`**.
- Best for single-node self-hosting. It **cannot be horizontally replicated** (each
  replica would run its own Postgres) — use option 3 for that.

Configure it with `-e` flags at run time — see [configuration.md](configuration.md).

## 2. Clone the repo — `docker compose up` (for developers)

```bash
git clone https://github.com/seahop/bkt && cd bkt
python3 setup.py          # generates .env + TLS certificates
docker compose up
```

- Uses `docker-compose.yml`, which **builds images from the cloned source** and runs
  **three** containers:
  - `bkt-db` — PostgreSQL
  - `bkt-backend` — `development` target (`go run`, source mounted, **hot-reload**)
  - `bkt-frontend` — Vite dev server with **hot-reload**, serving the UI on `5173`/`8443`
- This is the **development environment**: live-reloading, separate containers you can
  rebuild individually. It needs `setup.py`/`.env` because nothing is auto-generated.
- **UI port note:** in this dev setup the backend's `9443` serves only a *placeholder*
  UI — the real, hot-reloading UI is the frontend container on **`8443`/`5173`**. (In the
  omnibus, `9443` serves the real UI because it's compiled into the binary.)

The cloned repo can also build the omnibus directly:

```bash
docker build --target omnibus -t bkt .
docker run -d -p 9443:9443 -p 9000:9000 -v bkt-data:/data bkt
```

## 3. Multi-container, external Postgres — compose.prod / Helm (for scale-out)

```bash
curl -O https://raw.githubusercontent.com/seahop/bkt/main/docker-compose.prod.yml
python3 <(curl -s https://raw.githubusercontent.com/seahop/bkt/main/setup.py)
docker compose -f docker-compose.prod.yml up -d
```

- **Pulls** the prebuilt `ghcr.io/seahop/bkt-backend` image (UI-inclusive backend, **no**
  DB) and runs it against a **separate PostgreSQL** container — two containers, external
  database. The Helm chart in [`charts/bkt`](../../charts/bkt) does the same on Kubernetes.
- Use this when you need to scale the app independently of the database, run HA, or point
  at a managed Postgres. The frontend is embedded in the backend image — there is no
  separate frontend container.

## At a glance

| | 1. Omnibus (`docker pull`) | 2. Clone + compose | 3. compose.prod / Helm |
|---|---|---|---|
| Audience | end user running it | developer changing code | ops / scale-out |
| Source code | not present (baked) | the whole repo | not present (baked) |
| Containers | **1** (backend+UI+DB) | **3** (db, backend, frontend) | **2** (backend, postgres) |
| Image(s) | `ghcr.io/seahop/bkt` | built locally from source | `ghcr.io/seahop/bkt-backend` + `postgres` |
| Config | auto-generated, zero-config | `setup.py` → `.env` | `setup.py`/`.env` or Helm values |
| Web UI | embedded, on `9443` | Vite container on `8443`/`5173` | embedded, on `9443` |
| Code changes | rebuild image | hot-reload, instantly | rebuild/republish image |
| Scales out? | no (single node) | n/a (dev) | yes |

All three run the **same Go binary with the embedded UI** — they are different
*packagings* of one codebase, built from the same root `Dockerfile` (`--target omnibus`
and `--target backend`).
