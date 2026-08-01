# OpenField Server Documentation

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

You can override any config value using environment variables:

| Variable | Description | Default |
|----------|-------------|---------|
| `OPENFIELD_CONFIG` | Path to config file | `config/config.local.yaml` |
| `SERVER_PORT` | Server port | `8080` |
| `DATABASE_URL` | PostgreSQL connection URL | - |
| `REDIS_ADDR` | Redis address | `localhost:6379` |
| `REDIS_PASSWORD` | Redis password | - |
| `OIDC_ISSUER_URL` | OIDC issuer URL | `https://auth.yun/oidc` |
| `OIDC_CLIENT_ID` | OIDC client ID | - |
| `OIDC_CLIENT_SECRET` | OIDC client secret | - |
| `OIDC_REDIRECT_URL` | OIDC redirect URL | `http://localhost:8080/api/v1/auth/oidc/callback` |
| `OIDC_APP_REDIRECT_URL` | Deep link to return to the desktop/mobile app after login (e.g. `openfield://oauth/callback`) | empty (JSON response) |
| `JWT_SECRET_KEY` | JWT signing secret | - |
| `STORAGE_ENDPOINT` | RustFS (S3) endpoint | `localhost:9000` |
| `STORAGE_ACCESS_KEY` | RustFS access key | `minioadmin` |
| `STORAGE_SECRET_KEY` | RustFS secret key | `minioadmin` |
| `STORAGE_BUCKET` | RustFS bucket name | `openfield` |
| `STORAGE_PUBLIC_BASE_URL` | Public URL prefix for stored objects | `http://localhost:9000/openfield` |

## Database

The server uses PostgreSQL. Set up your database:

```sql
CREATE DATABASE openfield;
```

The server will automatically create tables on first run via migrations.

## Redis

Redis is optional. To enable:

1. Set `redis.enabled: true` in config
2. Ensure Redis is running at the configured address

Redis is used for:
- Caching frequently accessed data
- Session storage
- Rate limiting

## Running

```bash
# Install dependencies
go mod download

# Run with default config
go run ./cmd/main.go

# Run with custom config path
OPENFIELD_CONFIG=/path/to/config.yaml go run ./cmd/main.go
```

## API Documentation

See [API.md](API.md) for full API documentation.
