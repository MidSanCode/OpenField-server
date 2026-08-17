# Multi-Bucket Storage

OpenField supports **physical multi-bucket object storage**: every logical
storage bucket maps to its own S3-compatible bucket (AWS S3, MinIO, RustFS).
Users are assigned to one logical bucket whose files are then stored in that
bucket's physical S3 bucket.

## Concepts

- **Logical bucket id** — a short string stored on `users.storage_bucket`. It
  is the only value the client sees and sends. The default is `default`.
- **Physical S3 bucket** — the actual bucket name objects are written into.
- **Default quota** — each bucket grants its own storage quota (bytes) to users
  who are on it. Switching buckets resets the user's quota to the new bucket's
  default.
- **Membership gate** — a bucket may require a minimum active membership level
  (`min_member_level`). Users below that level cannot switch to it, and uploads
  are denied while the user is on a bucket above their level (e.g. after their
  membership expires).
- **Membership bonus** — active members get a storage bonus on top of their
  base quota, but **only on the default bucket**. On non-default buckets the
  bonus is not applied, matching the quota enforcement on the server and the
  client.

## Configuration

Multi-bucket mode is entered by listing `storage.buckets`. When the list is
absent, a single default bucket is synthesized from the legacy top-level
`bucket` / `public_base_url` fields, preserving single-bucket behaviour:

```yaml
storage:
  endpoint: "localhost:9000"
  access_key: "minioadmin"
  secret_key: "minioadmin"
  region: ""
  use_ssl: false
  public_base_url: "http://localhost:9000/openfield"
  max_upload_bytes: 10485760
  max_attachments_per_post: 9
  buckets:
    - name: "default"           # logical id stored on users.storage_bucket
      bucket: "openfield"       # physical S3 bucket
      label: "标准"
      default_quota: 104857600  # 100 MB
      min_member_level: 0       # 0 = everyone may use it
      is_default: true
    - name: "star"              # Lv.4 (孤星) members only
      bucket: "openfield-star"
      label: "孤星"
      default_quota: 5368709120 # 5 GB
      min_member_level: 4
      is_default: false
```

Rules:

1. Exactly one bucket should be marked `is_default: true`. When none is, the
   first bucket is treated as the default.
2. `min_member_level` uses the membership tier (`1`=薄雾, `2`=篝火, `3`=明月,
   `4`=孤星). `0` means everyone.
3. The membership storage bonus only applies while the user is on the **default
   bucket**. Bonus tiers: Lv.1 +100MB, Lv.2 +200MB, Lv.3 +400MB, **Lv.4
   +800MB**.
4. The `public_base_url` on a bucket overrides the shared one for that bucket.

## Behaviour

- **New users** land on the default bucket (`storage_bucket = 'default'`) with
  the default quota (100 MB baseline).
- **Switching buckets** (`PUT /api/v1/users/me/storage-bucket`) validates the
  target's membership gate and sets the user's quota to the bucket's
  `default_quota`. Existing attachments are **not moved**; their records keep
  the bucket they were uploaded to, so deletes resolve to the correct physical
  store.
- **Uploads & chunked uploads** are routed to the store backing the user's
  current bucket, and re-check the membership gate and quota at request time
  (defence in depth against a downgraded/expired membership).

## Database

Migration **v8** adds:

- `users.storage_bucket VARCHAR(50) NOT NULL DEFAULT 'default'`
- `attachments.bucket VARCHAR(50) NOT NULL DEFAULT 'default'`

## API

- `GET /api/v1/users/storage-buckets` (auth required) — list buckets with
  `name`, `label`, `default_quota`, `min_member_level`, `is_default`, `locked`
  (whether the requesting user may use it), plus the user's `current` bucket.
- `PUT /api/v1/users/me/storage-bucket` (auth required) — body
  `{"bucket": "<logical id>"}`. Returns the updated user. Rejects unknown
  buckets (`400`) and membership-gated buckets without an adequate active
  membership (`403`).

## Client

The Flutter client mirrors the effective quota in `User.effectiveStorageQuota`
(the membership bonus is only added on the default bucket) and shows bucket
labelling/locking on the storage bucket settings page
(`lib/pages/account/storage_bucket_page.dart`), reachable from account
settings.