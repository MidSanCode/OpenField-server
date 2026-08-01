#!/usr/bin/env bash
# ============================================================
#  OpenField Server - One-click launcher (Linux/macOS)
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
echo "[1/3] Downloading dependencies..."
go mod download

# ---- Build server binary ----
echo "[2/3] Building server..."
mkdir -p bin
go build -o bin/openfield-server ./cmd

# ---- Start server ----
echo "[3/3] Starting server..."
export OPENFIELD_CONFIG="config/config.local.yaml"
exec bin/openfield-server
