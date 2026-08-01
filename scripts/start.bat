@echo off
setlocal enabledelayedexpansion
rem ============================================================
rem  OpenField Server - One-click launcher (Windows)
rem ============================================================

set "SCRIPT_DIR=%~dp0"
set "SERVER_DIR=%SCRIPT_DIR%.."
cd /d "%SERVER_DIR%"

echo ==========================================
echo   OpenField Server - One-click launcher
echo ==========================================
echo.

rem ---- Check Go toolchain ----
where go >nul 2>nul
if errorlevel 1 (
    echo [ERROR] Go is not installed or not in PATH.
    echo         Install Go 1.24+: https://go.dev/dl/
    pause
    exit /b 1
)

rem ---- Ensure local config exists ----
if not exist "config\config.local.yaml" (
    echo [INFO] config\config.local.yaml not found, copying from example...
    copy "config\config.example.yaml" "config\config.local.yaml" >nul
    echo [WARN] Please review config\config.local.yaml and fill in your settings.
)

rem ---- Download dependencies ----
echo [1/3] Downloading dependencies...
call go mod download
if errorlevel 1 (
    echo [ERROR] Failed to download dependencies. Check network access.
    pause
    exit /b 1
)

rem ---- Build server binary ----
echo [2/3] Building server...
if not exist "bin" mkdir "bin"
call go build -o bin\openfield-server.exe .\cmd
if errorlevel 1 (
    echo [ERROR] Build failed.
    pause
    exit /b 1
)

rem ---- Start server ----
echo [3/3] Starting server...
set "OPENFIELD_CONFIG=config\config.local.yaml"
bin\openfield-server.exe
