# OpenField Server Architecture

## Overview

OpenField is a microservices monorepo with a single API gateway in front of six
internal services plus a Flask admin panel. The Flutter client only ever talks to
the gateway (`:8080`).

```
                  ┌────────────────────┐
 Flutter App ────►│  Gateway  (:8080)  │
                  │  auth + routing    │
                  └─────────┬──────────┘
                            │ reverse proxy
      ┌──────────┬──────────┼──────────┬───────────┐
      ▼          ▼          ▼          ▼           ▼
   Account    Storage      Chat      Posts        Push
    :8081      :8082      :8083      :8084       :8085
    JWT/       attach-    conver-   posts/     tickets +
    profile/    ments/     sations/  replies/   WS hub
    bots        usage      E2EE      camps

   Plugin :8086  (optional; not started by scripts/start.sh)
   Flask Admin :5001  (users, groups, permissions, reports)
   RustFS :9000  (S3-compatible object storage)
```

`plugin` is the odd one out: the gateway route table and the aggregate health
check both reference it, `run.bat` starts it, and its address is configured in
`config/config.example.yaml` — but `scripts/start.sh` neither builds nor starts
it, so on a Linux/macOS one-click stack the plugin routes have no backend.
See [Configuration](#configuration) — a service whose URL is missing does **not**
get skipped cleanly.

## Ports

| Component            | Port  | Protocol | Notes |
|----------------------|-------|----------|-------|
| Gateway              | 8080  | HTTP     | the only externally reachable port |
| Account service      | 8081  | HTTP     | |
| Storage service      | 8082  | HTTP     | |
| Chat service         | 8083  | HTTP     | |
| Posts service        | 8084  | HTTP     | |
| Push service         | 8085  | HTTP + WS | issues tickets, holds the WebSocket hub |
| Plugin service       | 8086  | HTTP     | optional; not built or started by `scripts/start.sh` |
| Flask admin panel    | 5001  | HTTP     | |
| RustFS (S3)          | 9000  | S3 API   | |

Every internal service derives its listen port from its own entry in
`services:` (see `Config.ServicePort`), overridable per service with
`SERVICE_<NAME>_PORT`. Gateway and admin are the only components with their own
port settings. All internal services bind `127.0.0.1`.

## Request Flow

1. The client calls `http://localhost:8080/api/v1/<endpoint>` with an
   `Authorization: Bearer <jwt>` header when authenticated.
2. The gateway matches the method + path against its route table.
3. Depending on the route's auth level, the gateway:

   - `authPublic` — forwards without *requiring* a token. A valid Bearer **JWT**
     is resolved and injected as `X-User-ID` so public reads can be personalized
     (e.g. `is_following`, `my_role`); a **bot token** (`ofb_…`) is accepted here
     too, via a second branch (`main.go:493`). An absent or invalid token simply
     leaves the request anonymous.
   - `authRequired` — validates the credential and injects `X-User-ID`. Either a
     JWT or a static bot token is accepted (`main.go:530`).
   - `authPermission` — as above, then checks the route's permission key. Bot
     tokens carry the bot's own grants, so permissions apply unchanged.
   - `authTicket` — WebSocket upgrade only (`GET /api/v1/ws`): any
     client-supplied identity header is deleted and the request is forwarded, so
     the one-time ticket is the sole credential. A bare Bearer token is
     deliberately *not* accepted on this path.

4. If the permission check fails, the gateway returns `403` and never contacts
   the upstream service. A banned account is stopped at the same choke point
   (`main.go:567`) with `403 {"error":"account is banned"}` — except a banned
   **bot**, which fails token resolution earlier and sees `401`.
5. The gateway forwards the request to the target service via `httputil.ReverseProxy`,
   preserving the full `/api/v1` path.

Two paths never reach the route table, so nothing above applies to them:

- `GET /api/v1/health` — registered explicitly on the gateway, public, fans out
  to every backend's `/healthz` (see [health.md](health.md)).
- `GET /api/v1/files/...` — handled in the gateway's `NoRoute` because object keys
  span multiple path segments (`main.go:456`). It resolves **JWTs only** and
  proxies to storage; visibility is decided by the storage service. A bot token
  here is treated as anonymous.

Internal services run `GatewayAuthMiddleware()`, which trusts the `X-User-ID`
header set by the gateway. Internal services are **not exposed directly**: they
bind to `127.0.0.1` only, so the sole externally reachable entry point is the
gateway on `:8080`.

The gateway also applies, for every request it serves: security headers + HSTS,
a host allowlist (`server.allowed_hosts`), CORS, and a coarse per-IP token-bucket
rate limiter (120 req/min, burst 240 — `services/gateway/cmd/gateway_middleware.go`).

> **Public routes that personalize.** `middleware.GetUserID(c)` only reads the
> gin context, which `GatewayAuthMiddleware` populates — so on a public route
> it always returns `0`/false. A public handler that needs the caller's
> identity must use the package's `requesterID(c)` helper (context first, then
> the raw `X-User-ID` header). Getting this wrong is silent: authenticated
> users are treated as anonymous, which surfaces as spurious `401`s
> (`GET /camps?mine=1`), missing `my_role`/`is_member` fields, or an owner
> being locked out of their own resource by a `403`.

## Route Matching Rules

The gateway route table uses gin-style `:param` segments (see `matchRoute` in
`services/gateway/cmd/main.go`):

- Each route has a method and a pattern, e.g. `PUT /api/v1/posts/:id/replies/:reply_id`.
- A pattern segment starting with `:` matches any single path segment.
- A literal segment matches only the identical path segment.
- The number of segments must match exactly.
- When several routes match, the one with the **most literal segments** wins
  (ties broken by first definition). This ensures parameterized routes such as
  `/conversations/start` do not get captured by `/conversations/:id`.

Because `:param` matches exactly one segment, patterns cannot express variable
paths above one segment: `/api/v1/files/<bucket>/<key…>` is therefore special-cased
in `NoRoute` rather than tabled (see above).

## Permission Model

The gateway checks permissions command-style via
`permFactory.Permit(userID, permissionKey)`:

- Every user belongs to zero or more groups; groups grant permissions.
- The system `admin` role grants all permissions.
- Permission keys are dot-namespaced by domain. The complete set lives in
  `pkg/permission/permission.go`:

| Domain   | Keys                                                                  |
|----------|-----------------------------------------------------------------------|
| account  | `account.view`, `account.profile.edit`, `account.avatar.edit`, `account.banner.edit`, `account.verify`¹, `account.follow` |
| user     | `user.adjust_exp`¹, `user.membership.grant`¹, `user.punish`¹           |
| storage  | `storage.upload`, `storage.list`, `storage.get`, `storage.delete`     |
| posts    | `posts.view`, `posts.create`, `posts.edit`, `posts.delete`, `posts.reply.create`, `posts.reply.edit`, `posts.reply.delete`, `posts.react`, `posts.favorite` |
| chat     | `chat.view`, `chat.send`, `chat.edit`, `chat.delete`, `chat.request.send`, `chat.request.approve`, `chat.group.create`, `chat.group.invite`, `chat.group.manage`, `chat.group.nickname`, `chat.note.edit` |
| wallet   | `wallet.view`, `wallet.manage`¹                                        |
| plugin   | `plugin.manage`¹                                                      |
| app      | `app.announcements.manage`¹                                           |

¹ **Administrative keys**: listed in `AdminPermissions()` and never granted to the
default group. They let the holder act on other accounts or on server-wide state
(verify an account, change exp or membership, punish, mint currency, manage
plugins and announcements). `DefaultGroupPermissions()` is `All()` minus these
seven, and the default group 「所有人」 receives exactly that — which is why a
newly registered user (and a new bot) can use every ordinary feature but has no
authority over others.

The `POST /auth/register` route and `GET /users/me/permissions` require only a
valid JWT (so a user can finish registration and inspect their own grants without
needing any pre-existing permission).

## Services

### Gateway (`services/gateway`)
- Owns the single route table and all auth/permission logic.
- Also serves the aggregate health endpoint, the object-storage path proxy, the
  per-IP rate limiter and the ban check (`gateway_ban.go`).
- Depends on `pkg/middleware` (JWT parse, bot-token resolution, `X-User-ID`,
  permission factory) and `pkg/config`.

### Account (`services/account`)
- JWT issuance/refresh, password login, OIDC login, profile updates, avatar/banner
  upload, user search, permissions lookup.
- Owns brute-force budgets (`pkg/ratelimit`): login failures and payment PIN.
- Owns **bots**: creation, listing, token regeneration, deletion
  (`internal/handler/bot.go`), and **moderation reports** (`internal/handler/report.go`).
- Owns app announcements, membership and tasks/experience.

### Posts (`services/posts`)
- Posts CRUD, replies CRUD, per-user post listing. Attachments are referenced by ID
  and rendered from the Storage service's URLs.
- Owns **camps** (贴吧-style communities) and their private **subgroups**
  (`camps.parent_camp_id`), plus the reportable content on that side.

### Storage (`services/storage`)
- Attachment upload/list/get/delete and storage usage. Stores bytes in RustFS.
- Serves the gateway's `/api/v1/files/` proxy; decides public vs owner-only
  visibility per object.

### Chat (`services/chat`)
- Consent-request based conversations: private chat start, group invite, accept/decline.
- Group membership (owner/admin/member), notes, group nicknames, read state.
- Messages with quote-replies (`reply_to_id`), edit (`edited_at`) and soft-delete (`deleted_at`).
- E2EE group-key envelopes; encrypted conversations store ciphertext only.

### Push (`services/push`)
- Issues single-use WebSocket tickets (`POST /api/v1/ws`) and owns the hub that
  holds live connections.
- Receives events from the other services and fans them out; delivery is
  best-effort, there is no replay buffer.

### Plugin (`services/plugin`)
- Plugin registry and management surface behind `plugin.manage`.
- Optional: not started by `scripts/start.sh` and not configured in
  `config/config.example.yaml`.

## Admin Panel

The Flask admin panel (`admin/`) manages users, groups, group memberships, and
permission grants. It is the source of truth for role/permission changes. It also
holds operator queues such as the moderation report inbox (`reports.manage`).

## Configuration

The gateway reads a YAML config (default `config/config.local.yaml`, overridable
via the `OPENFIELD_CONFIG` env var). Service base URLs and the JWT secret must be
consistent across the gateway and all internal services.

Each service's listen port is derived from its own `services:` entry, so an
address is needed for every service the gateway can route to. **A missing entry
is not a no-op**: `url.Parse("")` succeeds, so the gateway still builds a proxy
target for it and the affected routes fail at request time, while the service
itself falls back to `server.port` and collides with the gateway. If a service is
meant to be absent, remove its routes (or accept failing requests) deliberately
rather than relying on an empty URL.
