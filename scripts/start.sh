#!/usr/bin/env bash
# ============================================================
#  OpenField Server - One-click launcher (Linux/macOS)
#  Starts: gateway(8080) account(8081) storage(8082) chat(8083) posts(8084)
# ============================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVER_DIR="$(dirname "$SCRIPT_DIR")"
cd "$SERVER_DIR"

echo "=========================================="
echo "  OpenField Server - One-click launcher"
echo "=========================================="
echo

# ---- Check Go toolchain ----
if ! command -v go &>/dev/null; then
    echo "[ERROR] Go is not installed or not in PATH."
    echo "        Install Go 1.24+: https://go.dev/dl/"
    exit 1
fi

# ---- Ensure local config exists ----
if [ ! -f "config/config.local.yaml" ]; then
    echo "[INFO] config/config.local.yaml not found, copying from example..."
    cp config/config.example.yaml config/config.local.yaml
    echo "[WARN] Please review config/config.local.yaml and fill in your settings."
fi

# ---- Download dependencies ----
echo "[1/4] Downloading dependencies..."
go mod download

# ---- Build services ----
echo "[2/4] Building services..."
mkdir -p bin
for S in gateway account storage chat posts; do
    echo "  - building $S..."
    (cd "services/$S" && go build -o "../../bin/openfield-$S" ./cmd)
done

# ---- Start services ----
echo "[3/4] Starting services..."
export OPENFIELD_CONFIG="config/config.local.yaml"
pids=""
for S in account storage chat posts gateway; do
    "bin/openfield-$S" &
    pids="$pids $!"
done

echo "[4/4] All services started."
echo "  gateway: http://localhost:8080"
echo "  account: http://localhost:8081"
echo "  storage: http://localhost:8082"
echo "  chat:    http://localhost:8083"
echo "  posts:   http://localhost:8084"
echo

# trap to kill all services on exit
trap 'kill $pids 2>/dev/null' EXIT INT TERM
wait
