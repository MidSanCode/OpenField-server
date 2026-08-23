# OpenField Server Features

This document is a feature guide for the OpenField server. It complements the
[API reference](API.md) by explaining what each feature does, how it is built,
and where the responsible code lives. See [architecture.md](architecture.md)
for the service layout and [wallet.md](wallet.md) for the wallet in detail.

## Feature Map

| Feature                    | Location                                                     | Notes |
|----------------------------|--------------------------------------------------------------|-------|
| Authentication & OIDC      | `services/account/internal/handler/auth.go`                  | OIDC + password login |
| Profile & users            | `services/account/internal/handler/user.go`                  | edit, avatar/banner, locale |
| Permissions (groups)       | gateway auth + `pkg/repository/permission.go`                | route-level checks |
| Wallet & transfers          | `services/account/internal/handler/wallet.go`, `transfer.go` | cents storage |
| Payment PIN                | `services/account/internal/handler/pin.go`                   | authorizes payments |
| Tasks & experience         | `services/account/internal/handler/task.go`                  | daily login, one-time codes |
| Exp history                | `services/account/internal/handler/task.go`                  | level derivation |
| Membership                 | `services/account/internal/handler/membership.go`            | tiers, multiplier, storage |
| Name styling               | `services/account/internal/handler/user.go`                  | color / gradient / animation |
| Follow & friends           | `services/account/internal/handler/user.go`                  | mutual follows |
| Posts & replies            | `services/posts`                                             | reactions, favorites, views |
| Attachments / storage      | `services/storage`                                           | RustFS, quota |
| Chat & consent             | `services/chat`                                              | E2E, groups, mentions |
| Realtime push              | `services/push`                                              | WebSocket gateway |
| Admin panel                | `admin/app.py`                                               | users, groups, grants |

## Authentication

- **OIDC login** exchanges an authorization code for JWT access + refresh
  tokens. When `oidc.app_redirect_url` is configured, the callback responds
  with a `302` to a deep link (`openfield://oauth/callback?...`) carrying the
  tokens and user info; otherwise it returns JSON.
- **Password login** exists for admin-created local accounts.
- **Self-registration is not allowed.** New OAuth users are created with
  `needs_registration = true` and must call `POST /auth/register` to pick a
  unique username + nickname before using the app.
- **Refresh tokens** are single-use and rotated on every refresh. See
  [token-refresh.md](token-refresh.md).
- **Roles** come from groups; the first OAuth user is granted `admin`. The
  gateway enforces `authPublic` / `authRequired` / `authPermission` per route,
  injecting `X-User-ID` for downstream services.

Endpoint: `POST /auth/login`, `POST /auth/refresh`, `GET /auth/oidc/login`,
`GET|POST /auth/oidc/callback`, `POST /auth/oidc/bind`, `POST /auth/register`.

## Profile & Users

- `GET /users/me` returns the full profile including `storage_quota`,
  `storage_used`, membership fields and name-styling fields.
- `PUT /users/me` updates `username` / `nickname` / `bio` with the same
  uniqueness rules as registration and clears `needs_registration`.
- `POST /users/me/avatar` and `POST /users/me/banner` accept multipart uploads
  and count toward the storage quota.
- `PUT /users/me/locale` stores the region/language used for day boundaries
  and server-pushed notifications.
- `PUT /users/me/e2ee-key` publishes the user's X25519 public key for
  end-to-end-encrypted conversations.
- `GET /users/:user_id` is the public profile lookup; `GET /users/search`
  finds users by username/nickname substring.

## Permissions

Permissions are dot-namespaced keys granted through groups or the `admin`
role. The gateway performs the only check; a `403` is returned before the
request ever reaches an internal service.

| Domain   | Keys |
|----------|------|
| account  | `account.view`, `account.profile.edit`, `account.avatar.edit`, `account.banner.edit`, `account.follow` |
| storage  | `storage.upload`, `storage.list`, `storage.get`, `storage.delete` |
| posts    | `posts.create`, `posts.edit`, `posts.delete`, `posts.reply.create`, `posts.reply.edit`, `posts.reply.delete`, `posts.react`, `posts.favorite` |
| chat     | `chat.view`, `chat.send`, `chat.edit`, `chat.delete`, `chat.request.send`, `chat.request.approve`, `chat.group.create`, `chat.group.invite`, `chat.group.manage`, `chat.group.nickname`, `chat.note.edit` |
| wallet   | `wallet.view`, `wallet.manage` |
| admin    | `user.adjust_exp`, `user.membership.grant` |

Keys are seeded by `seedPermissions()` in `pkg/database/migration.go`. Today
the default `everyone` group holds the full set; tightening group grants is a
follow-up.

`GET /users/me/permissions` returns the caller's effective permissions and
groups without requiring any permission itself (so registration can finish).

## Wallet, Transfers & Currency

Money is stored as integer cents (`BIGINT`) to avoid float drift. See
[wallet.md](wallet.md) for the transaction model.

- **Wallet**: `GET /wallet` returns balance + recent `wallet_transactions`.
  `POST /wallet/adjust` (permission `wallet.manage`) recharges/deducts a target
  user inside a transaction that locks the wallet row and refuses negative
  balances.
- **Transfers**: a user may transfer coins to any other user. Transfers are
  created with `POST /transfers`, then `accept`ed or `decline`d by the
  recipient using their payment PIN. Pending transfers are listed per user.
- **Payment PIN**: a 6-digit PIN (`POST /users/me/pin`) that authorizes all
  outbound payments. `POST /users/me/pin/verify` validates a candidate PIN
  against the stored hash. It never replaces the login password. Changing an
  existing PIN goes through `PUT /users/me/pin`, which requires the current
  PIN (`old_pin`) and shares the same per-user failure budget as verify.
  Admins can reset any user's PIN directly from the admin panel (bcrypt hash
  written to `users.pin_hash`). (These routes are registered in the gateway
  account block.)

## Tasks & Experience

Experience fuels the account level; every exp grant is routed through
`GrantExp`, which applies the active membership multiplier.

- **Daily login**: `POST /tasks/daily-login/claim` grants daily exp; the
  sign-in calendar is at `GET /tasks/daily-login/calendar`. Missed days can be
  made up with coins via `POST /tasks/daily-login/makeup` (auto) or
  `POST /tasks/daily-login/makeup-date` (per date).
- **One-time tasks**: `POST /tasks/:code/claim` completes a task by its
  achievement code (e.g. profile completion) for a one-time reward.
- **Exp history**: `GET /exp/history` returns a paginated ledger of every exp
  grant with the level applied, which the client uses to render the timeline.
- **Levels**: derived from cumulative total exp with geometric cost growth
  (level 2 costs 100 exp, each level 5% more, capped at level 200). See
  `pkg/model/level.go` for `LevelForExp` / `ExpProgress`.

## Membership

Membership is a purchasable tier that boosts experience and grants perks. It
lives in `pkg/model/membership.go`.

- **Tiers** (fixed catalog):

  | Level | Name   | Price (coins) | Exp multiplier | Storage bonus |
  |-------|--------|---------------|----------------|---------------|
  | 1     | 薄雾   | 200           | x2             | +100 MB       |
  | 2     | 篝火   | 400           | x2.5           | +200 MB       |
  | 3     | 明月   | 800           | x3             | +400 MB       |
  | 4     | 孤星   | 1600          | x3.5           | +400 MB       |

- Membership lasts 30 days (`MemberDurationDays`); the level is kept after
  expiry so a renewal extends from where the user left off (`MembershipActive`
  decides whether the multiplier/bonus applies at a given moment).
- **Purchase**: `POST /membership/purchase` charges the wallet and requires the
  payment PIN. `GET /membership` returns the user's status plus the catalog
  with each tier's multiplier, storage bonus and name-style caps.
- **Admin grant**: `PUT /users/:user_id/membership` (permission
  `user.membership.grant`) assigns a tier directly, or clears it with level 0;
  an optional `days` overrides the default duration.
- **Exp multiplier**: applied inside `GrantExp` while active, so positive
  adjustments scale up automatically.
- **Storage bonus**: upload quotas use `MemberStorageBonusAt`, which adds the
  tier bonus while active and reverts to base the moment membership expires.
  When membership expires above the base quota, further uploads are rejected
  until usage drops back under the base (see `checkQuota` in the storage
  service and `uploadImage` in the account service).

## Name Styling

Members can customize the color of their display name, rendered identically
across posts, replies, chat and profiles via denormalized columns on every
user JOIN.

- Columns on `users`: `name_color`, `name_color_to`, `name_dynamic`
  (animated flag) plus the reserved `avatar_frame`.
- **Tier caps** (`MemberNameStyleAllowed`):
  - Lv.1: fixed preset palette only (`MemberPresetNameColors`).
  - Lv.2: any valid hex color.
  - Lv.3: also allowed a second gradient color (`name_color_to`).
  - Lv.4: also allowed the animated gradient (`name_dynamic`).
- `PUT /users/me/name-style` validates against the caps (presets-only is
  enforced server-side for Lv.1) and persists the styling. The gateway routes
  it with the `account.profile.edit` permission.
- The member tier badge next to the name (tier name shown between display name
  and verified badge) is driven by the denormalized `member_level` /
  `member_active` fields on posts, replies, chat members and messages.

## Follow & Friends

`POST/DELETE /users/:user_id/follow` (permission `account.follow`) track
followers. When two users follow each other they become **friends**; lists are
available at `GET /users/:user_id/followers|following|friends`. Follow
counts and `is_following` / `is_friend` are populated on the user profile.

Users may opt out of sharing all three lists via `PUT /users/me/privacy`
(`{"hide_follow_lists": true}`). While enabled, the lists return 403 to
everyone but the account owner, and `follower_count` / `following_count` are
suppressed from other users' profile reads.

## Posts & Replies

Posts support optional attachments, visibility scopes (public / private /
friends), reactions, favorites and read-tracking.

- **CRUD**: `GET|POST /posts`, `PUT|DELETE /posts/:id` (owner only), plus
  per-user listing `GET /users/:user_id/posts`.
- **Replies**: `GET|POST /posts/:id/replies`, edit/delete per reply, and
  per-reply favorites (`POST .../replies/:reply_id/favorite`).
- **Reactions**: `PUT/DELETE /posts/:id/reactions` with a reaction key;
  aggregates included in the post payload.
- **Favorites**: `POST/DELETE /posts/:id/favorite`; the caller's favorite
  lists are `GET /users/:user_id/favorites/posts|replies`.
- **Reading**: post payloads include `reply_count`, `favorite_count`, reaction
  map and the author's denormalized membership/name-style fields. Anonymous
  viewers only see public posts (`visibilityCondition`).

## Attachments & Storage

Files are stored in RustFS (an S3-compatible store); `pkg/storage` wraps
upload/delete/URL building.

- **Quota**: each user has `storage_quota` (MB default; adjustable by admin or
  via membership bonus). `checkQuota` in the storage service and
  `uploadImage` in the account service both enforce the *effective* quota
  (base + active member bonus). Exceeding it returns `413 storage quota
  exceeded`; avatar/banner uploads count toward the same quota.
- **Chunked uploads**: large files go through `POST /attachments/chunk/init`,
  per-chunk `POST .../chunk/:upload_id/:index`, and
  `POST .../chunk/:upload_id/complete`.
- **Usage & management**: `GET /storage/usage` reports used bytes; attachments
  are listed via `GET /attachments` and fetched/deleted via
  `GET|DELETE /attachments/:id`.

## Chat

Chat uses a **consent-request** model: starting a private chat or inviting to
a group creates a request the target must accept or decline.

- **Private chats**: `POST /conversations/start` (sends a request),
  `PUT /conversations/:id/note` (private remark).
- **Groups**: `POST /conversations` creates a group; `POST /conversations/:id/invite`
  sends invites; membership management covers owner/admin/member roles, mute,
  titles, `group_nickname` and `leave` / `remove` / `join`.
- **Settings**: `PUT /conversations/:id/settings|title|avatar` and
  `POST/DELETE .../mute-all`.
- **Messages**: `GET|POST /conversations/:id/messages` (backward pagination via
  `before`), edit (`PUT`), soft-delete (`DELETE`), quote-replies
  (`reply_to_id`), `@mentions` (including `@everyone`), and read-state
  (`POST .../read`).
- **E2E encryption**: conversations may be `encrypted`; group keys are
  distributed as per-member envelopes (`GET|POST /conversations/:id/e2ee-keys`).
  Uncrypted system messages carry kinds like `system.join` / `system.leave`
  for localized rendering.
- **Realtime**: the chat service emits events pushed through the push service;
  the WebSocket endpoint is `GET /api/v1/ws`.

## Realtime Push

The push service (`services/push`) holds WebSocket connections (authed via
`GET /api/v1/ws`) and fans out chat events, typing indicators and notification
messages to connected clients. It is the single bidirectional channel; all
other traffic is plain REST.

## Admin Panel

The Flask admin panel (`admin/`) is the operational console:

- **Users**: list/search, edit role, verify users (writes `is_verified`,
  `verified_by`, `verified_note`; writing verification detail now implies
  `is_verified = true` so the badge renders), reset passwords, adjust exp,
  grant membership.
- **Groups & permissions**: manage groups, group memberships and permission
  grants; also manages verifier admins.
- **Wallet**: adjusts any user's balance through the same transactional
  `wallets` tables.

The panel shares the PostgreSQL database with the services and runs on its own
port (default `5001`).

## Database & Migrations

- Schema is created and upgraded by **versioned migrations** in
  `pkg/database/migration.go`, applied on boot inside a transaction protected
  by an advisory lock (see commit `89c0c2c` for the initial versioned
  framework).
- New feature columns (e.g. membership, name styling) are additive
  `ADD COLUMN IF NOT EXISTS` steps, so the server is safe to roll forward
  against older databases.

## Known Operation Notes

- New permission/gateway routes must be added in BOTH the internal service
  router and the gateway route table (`services/gateway/cmd/main.go`);
  forgetting the gateway half silently yields `404` for the route (this is
  exactly what happened with the payment-PIN routes, fixed in `3f03453`).
- Internal services bind to `127.0.0.1` and trust `X-User-ID`; they must not
  be exposed publicly.