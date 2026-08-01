# OpenField Server API Documentation

## Base URL
```
http://localhost:8080/api/v1
```

## Authentication

OpenField uses OIDC (OpenID Connect) for authentication, plus password login for
admin-created local accounts. **Self-registration is not allowed.** New OAuth users
are created with `needs_registration = true` and must call `POST /auth/register`
to set a unique username and a nickname before using the app.

### Roles

- `user` - regular user (default)
- `admin` - administrator; the **first** user created in the system (via OAuth) is
  automatically granted the `admin` role. The Flask admin panel can promote/demote users.

### Get Available Providers
```
GET /auth/providers
```

Response:
```json
{
  "providers": ["oidc", "password"]
}
```

### Password Login (local accounts created by admins)
```
POST /auth/login
Body: { "username": "alice", "password": "secret" }
```

### Register (complete OAuth signup)
```
POST /auth/register
Authorization: Bearer <access_token>
Body: { "username": "alice", "nickname": "Alice" }
```

### OIDC Login Flow
1. Get the authorization URL:
```
GET /auth/oidc/login
```

Response:
```json
{
  "auth_url": "https://auth.yun/oidc/auth?...",
  "provider": "oidc"
}
```

2. Redirect user to the auth_url.

3. After authorization, the provider redirects to the callback URL with an authorization code.

4. The server exchanges the code for tokens:

```
GET /auth/oidc/callback?code=...
```

When `oidc.app_redirect_url` is configured (e.g. `openfield://oauth/callback`), the server responds with a
`302 Found` redirect to that deep link carrying the access token and user info, so desktop/mobile apps can
complete the login via the OS URI handler:

```
openfield://oauth/callback?access_token=eyJ...&username=openfield&email=user@example.com&avatar_url=...
```

Otherwise (e.g. API-only or web clients) it returns JSON:

```json
{
  "access_token": "eyJ...",
  "refresh_token": "...",
  "token_type": "Bearer",
  "expires_in": 86400,
  "user": {
    "id": 1,
    "username": "openfield",
    "email": "user@example.com",
    "avatar_url": "https://..."
  }
}
```

### Refresh Token
```
POST /auth/refresh
Body: { "refresh_token": "..." }
```

## Posts

### List Posts (Public)
```
GET /posts?page=1&limit=20
```

### Get Single Post (Public)
```
GET /posts/:id
```

### Create Post (Authenticated)
```
POST /posts
Authorization: Bearer <access_token>
Body: { "content": "Hello World!", "attachment_ids": [1, 2] }
```
`attachment_ids` is optional (max 9 per post). Attachments must be uploaded first via
`POST /attachments`.

### Delete Post (Authenticated, Owner Only)
```
DELETE /posts/:id
Authorization: Bearer <access_token>
```

### Update Post (Authenticated, Owner Only)
```
PUT /posts/:id
Authorization: Bearer <access_token>
Body: { "content": "Updated text!", "attachment_ids": [1, 3] }
```
`attachment_ids` is optional (max 9 per post) and **replaces** the post's attachments.

## Users

### Get Current User (Authenticated)
```
GET /users/me
Authorization: Bearer <access_token>
```
Response includes the current storage usage and quota:
```json
{
  "id": 1,
  "username": "alice",
  "nickname": "Alice",
  "avatar_url": "https://...",
  "storage_quota": 104857600,
  "storage_used": 2048000
}
```

### Update Profile (Authenticated)
```
PUT /users/me
Authorization: Bearer <access_token>
Body: { "username": "alice", "nickname": "Alice" }
```
Both fields optional; at least one required. Re-uses the same uniqueness rules as
registration. Clears `needs_registration`.

### Upload Avatar (Authenticated)
```
POST /users/me/avatar
Authorization: Bearer <access_token>
Content-Type: multipart/form-data
Form field: file=<image>
```

### Upload Banner / Header Image (Authenticated)
```
POST /users/me/banner
Authorization: Bearer <access_token>
Content-Type: multipart/form-data
Form field: file=<image>
```

### Get User by ID (Public)
```
GET /users/:id
```

## Attachments

Files are stored in the local RustFS (S3-compatible) object storage.

Each user has a total storage quota (default 100 MB). Uploads that would exceed
the quota are rejected with `413 storage quota exceeded`. Admins can adjust a
user's quota from the Flask admin panel. Avatar and banner uploads also count
toward the quota.

### Upload Attachment (Authenticated)
```
POST /attachments
Authorization: Bearer <access_token>
Content-Type: multipart/form-data
Form field: file=<file>
```
Response:
```json
{
  "id": 1,
  "user_id": 1,
  "original_name": "photo.jpg",
  "mime_type": "image/jpeg",
  "size_bytes": 12345,
  "url": "http://localhost:9000/openfield/2026/01/01/uuid.jpg",
  "created_at": "..."
}
```

### List My Attachments (Authenticated)
```
GET /attachments?limit=50
Authorization: Bearer <access_token>
```

### Get Attachment (Authenticated)
```
GET /attachments/:id
Authorization: Bearer <access_token>
```

### Delete Attachment (Authenticated, Owner Only)
```
DELETE /attachments/:id
Authorization: Bearer <access_token>
```

## Messages

### Send Message (Authenticated)
```
POST /messages
Authorization: Bearer <access_token>
Body: { "receiver_id": 2, "content": "Hello!" }
```

### Get Conversation (Authenticated)
```
GET /messages/:user_id?limit=50
Authorization: Bearer <access_token>
```

## Health Check
```
GET /health
```
Response: `{"status": "ok"}`
