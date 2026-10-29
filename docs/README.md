# OpenField Server Documentation

## Guides

- [Features](features.md) — feature guide for the whole server (auth,
  membership, wallet, tasks, chat, storage, camps, admin).
- [Daily tasks](daily-tasks.md) — per-day activity milestones (posts 1/5/10/20,
  chat 25/50/100) and how their progress and cycle keys work.
- [Checks](checks.md) — red packets ("checks"): claiming, splitting and the
  integrity rules around them.
- [Architecture](architecture.md) — service layout, ports, routes, the
  permission model and the gateway's auth levels.
- [API](API.md) — REST reference for the core resources (auth, users, wallet,
  posts, attachments, chat, health). Features added later have their own guides
  below rather than being duplicated there.
- [Wallet](wallet.md) — wallet, transfers and currency model.
- [Token refresh](token-refresh.md) — refresh-token rotation flow.
- [OAuth multi-account](multi-account.md) — one identity linked to up to 5
  accounts.
- [Storage buckets](storage-buckets.md) — physical multi-bucket object storage.
- [Chat history search](chat-search.md) — searching message history.
- [Punishments](punishments.md) — moderation actions and ban history.
- [Moderation reports](reports.md) — the 举报 queue for posts, chat messages and
  users.
- [Bots](bots.md) — bot development guide: creating automated accounts,
  authenticating with an `ofb_` token, the permission model, the callable API
  surface, realtime events and the limits that apply.
- [Service health checks](health.md) — per-service `/healthz` probes and the
  gateway's aggregate endpoint.
- [P2P voice/video calls](calls.md) — the call state machine, signalling relay,
  `chat.call` permission, call events and the TURN requirement for production.
- [Security hardening](security-hardening.md) — audit record of the 2026-08
  hardening pass, newest items first.

## Configuration

The server uses YAML configuration files located in the `config/` directory.

### Configuration Files

- `config/config.example.yaml` - Example configuration (committed to git)
- `config/config.local.yaml` - Local configuration (git-ignored, contains your actual settings)

### Setup

1. Copy the example config:
   ```bash
   cp config/config.example.yaml config/config.local.yaml
   ```

2. Edit `config/config.local.yaml` with your actual values:
   ```yaml
   database:
     user: "postgres"
     password: "your_password"
   oidc:
     client_id: "your_client_id"
     client_secret: "your_client_secret"
   jwt:
     secret_key: "your-secret-key"
   ```

### Environment Variables

Only the values below can be overridden by environment variables — the YAML file
remains the source of truth for everything else (upload limits, storage bucket
lists, `oidc.web_redirect_url` and the gateway rate limiter, for example, are
config-file or code constants with no environment override):

| Variable | Description | Default |
|----------|-------------|---------|
| `OPENFIELD_CONFIG` | Path to config file | `config/config.local.yaml` |
| `SERVER_PORT` | Gateway server port | `8080` |
| `ALLOW_ALL_ORIGINS` | Set `true` to allow API calls from any browser origin | - |
| `ALLOWED_ORIGINS` | CSV of allowed browser origins (e.g. `https://openfield.eu.cc,http://localhost:8090`). Local `localhost`/`127.0.0.1` origins are always allowed regardless of this setting | - |
| `ALLOWED_HOSTS` | CSV of `Host` values the gateway accepts; requests with any other Host are rejected. Empty disables the check | empty |
| `TRUSTED_PROXIES` | CSV of proxy CIDRs whose `X-Forwarded-For` is trusted for client IPs and IP bans. Set it only to networks you control | empty (trust none) |
| `DATABASE_URL` | PostgreSQL connection URL | - |
| `DATABASE_MIGRATE` | Set `true` to force a full schema migration at startup | `false` |
| `DATABASE_AUTO_MIGRATE` | Set `false` to disable the automatic schema upgrade each service performs on boot | `true` |
| `REDIS_ADDR` | Redis address | `localhost:6379` |
| `REDIS_PASSWORD` | Redis password | - |
| `OIDC_ISSUER_URL` | OIDC issuer URL | `https://auth.yun/oidc` |
| `OIDC_CLIENT_ID` | OIDC client ID | - |
| `OIDC_CLIENT_SECRET` | OIDC client secret | - |
| `OIDC_REDIRECT_URL` | OIDC redirect URL | `http://localhost:8080/api/v1/auth/oidc/callback` |
| `OIDC_APP_REDIRECT_URL` | Callback URL after login: the web app URL for browser logins (e.g. `https://openfield.eu.cc/`), or a custom-scheme deep link for desktop/mobile (e.g. `openfield://oauth/callback`) | empty (JSON response) |
| `JWT_SECRET_KEY` | JWT signing secret (must be ≥ 32 chars and not a known default, or services refuse to boot) | - |
| `JWT_EXPIRY_HOURS` | Access-token lifetime in hours | `24` |
| `JWT_REFRESH_EXPIRY_DAYS` | Refresh-token lifetime in days | `30` (service default when unset) |
| `OPENFIELD_ALLOW_WEAK_SECRETS` | `true` accepts a weak JWT secret. Throwaway local setups only | `false` |
| `SERVICE_ACCOUNT`, `SERVICE_STORAGE`, `SERVICE_CHAT`, `SERVICE_POSTS`, `SERVICE_PUSH`, `SERVICE_PLUGIN` | Base URL of one internal service, as seen by the gateway | values from `services:` |
| `SERVICE_<NAME>_PORT` | Listen-port override for that service (e.g. `SERVICE_PUSH_PORT`); otherwise derived from its `services:` URL, and finally from `server.port` | derived |
| `STORAGE_ENDPOINT` | S3-compatible endpoint (AWS S3, MinIO, RustFS) | `localhost:9000` |
| `STORAGE_INTERNAL_ENDPOINT` | Private S3 endpoint used for server-side calls; empty reuses `STORAGE_ENDPOINT` | empty |
| `STORAGE_ACCESS_KEY` | S3 access key | `minioadmin` |
| `STORAGE_SECRET_KEY` | S3 secret key | `minioadmin` |
| `STORAGE_BUCKET` | S3 bucket name | `openfield` |
| `STORAGE_REGION` | S3 region (required for AWS S3) | empty |
| `STORAGE_PUBLIC_BASE_URL` | Public prefix that file URLs are built from; empty derives it from the endpoint and bucket | `http://localhost:9000/openfield` |
| `GAME_DAILY_BONUS_EXP` | Exp granted once per server-day on the first authenticated request | `100` |
| `GAME_DAILY_BONUS_CURRENCY` | Currency granted with the daily bonus | `20` |
| `GAME_MAKEUP_COST` | Price of a make-up sign-in (0-value keys fall back to the default) | `100` |
| `GAME_TIMEZONE` | IANA timezone that defines the server-day boundary; invalid values fall back to UTC | `UTC` |

## Features

The full feature guide lives in [features.md](features.md). Highlights:

- Authentication via OIDC plus password login for local accounts.
- JWT access + rotating refresh tokens, permission-checked gateway routes.
- Wallet (integer cents), transfers, payment PIN and membership tiers.
- Experience, levels, daily sign-in calendar and exp history.
- Membership name styling (color / gradient / animated) and storage bonus.
- Posts, replies, reactions, favorites and scoped visibility.
- Camps (贴吧-style communities) with private subgroups: a subgroup inherits the
  parent's roster for adding members, is hidden from public camp listings and
  cascades away with its parent.
- A moderation report queue covering posts, chat messages and users, worked in the
  admin panel.
- Automated accounts ("bots"): owner-managed, driven by a static `ofb_` token, no
  interactive login — see [bots.md](bots.md).
- Red packets (checks) and a plugin registry.
- Service health probes plus a public aggregate endpoint.
- RustFS-backed attachments with per-user storage quotas.
- Consent-based chat with E2E encryption, groups and mentions.
- Peer-to-peer voice/video calls inside one-to-one chats: the server runs the
  call state machine and relays signalling, media goes straight between peers —
  see [calls.md](calls.md).
- A Flask admin panel managing users, groups, permissions and wallets.

## Database

The server uses PostgreSQL. Set up your database:

```sql
CREATE DATABASE openfield;
```

The server will automatically create tables on first run via migrations.

## Redis

Redis is **optional and currently unused**. The config keys exist and
`pkg/database.ConnectRedis` can open a connection, but no service calls it and
nothing reads `database.RedisClient`, so enabling it changes no behaviour today.
The three jobs usually delegated to Redis are handled elsewhere:

- caching and session state: PostgreSQL tables (e.g. `refresh_tokens`),
- login / payment-PIN brute-force budgets: `pkg/ratelimit`, in-process,
- general request limiting: the gateway's per-IP token bucket, in-process.

Treat Redis as reserved wiring for future use, not as a dependency.

## Running

There is no root-level `main.go` — the pre-microservice monolith was removed (see
[security-hardening.md](security-hardening.md)), so every command targets one
service's `cmd` directory.

```bash
# Install dependencies
go mod download

# Build every service (what scripts/start.sh does)
for s in gateway account storage chat posts push; do
  (cd "services/$s" && go build -o "../../bin/openfield-$s" ./cmd)
done

# Run one service against an explicit config
cd services/gateway && OPENFIELD_CONFIG=../../config/config.local.yaml go run ./cmd
```

For a full local stack use the launchers: `scripts/start.sh` (Linux/macOS, starts
six services) or `run.bat` (Windows, also starts the optional plugin service).
Neither starts the Flask admin panel.

## Docker images

One image is built per service from the single root `Dockerfile`; the `SERVICE`
build arg picks which `services/<name>/cmd` to compile.

```bash
docker build --build-arg SERVICE=gateway -t openfield-gateway .
docker build --build-arg SERVICE=account -t openfield-account .

docker run --rm -p 8080:8080 \
  -v "$PWD/config/config.local.yaml:/etc/openfield/config.yaml:ro" \
  openfield-gateway
```

The image reads `$OPENFIELD_CONFIG` (default `/etc/openfield/config.yaml`), so a
config file must be mounted or the container exits on startup. Each service
listens on the port taken from the config (`server.port` for the gateway, the
matching `services.*` URL for everything else) — the defaults are gateway 8080,
account 8081, storage 8082, chat 8083, posts 8084, push 8085, plugin 8086,
translate 8087. `config/config.example.yaml` ships inside the image as a
starting point.

Images run as an unprivileged user (uid 10001). The plugin service stores its
bundles in `/app/data/plugins` (`OPENFIELD_PLUGIN_DIR`); mount `/app/data` if
they should survive a restart.

`.github/workflows/docker.yml` builds and publishes every service image to
Docker Hub on pushes to `master` and on `v*` tags (multi-arch `amd64`/`arm64`,
plus SBOM and provenance), and only validates the build on pull requests. It
discovers services from `services/*/cmd`, so a new service needs no workflow
edit. Configure the `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN` repository
secrets; set the optional `DOCKERHUB_NAMESPACE` variable to publish under a
different org than the account. Images are named
`<namespace>/openfield-<service>`.

## API Documentation

See [API.md](API.md) for the REST reference on the core resources; features added
later are covered by their own guides, listed under [Guides](#guides) above.
