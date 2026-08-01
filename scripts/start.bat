@echo off
setlocal enabledelayedexpansion
rem ============================================================
rem  OpenField Server - One-click launcher (Windows)
rem  Starts: gateway(8080) account(8081) storage(8082) chat(8083) posts(8084)
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
echo [1/4] Downloading dependencies...
call go mod download
if errorlevel 1 (
    echo [ERROR] Failed to download dependencies. Check network access.
    pause
    exit /b 1
)

rem ---- Build services ----
echo [2/4] Building services...
if not exist "bin" mkdir "bin"
set "BUILD_FAILED=0"
for %%S in (gateway account storage chat posts) do (
    echo   - building %%S...
    pushd "services\%%S"
    call go build -o "..\..\bin\openfield-%%S.exe" .\cmd
    if errorlevel 1 (
        echo [ERROR] Build failed for %%S.
        set "BUILD_FAILED=1"
    )
    popd
)
if "%BUILD_FAILED%"=="1" (
    pause
    exit /b 1
)

rem ---- Start services ----
echo [3/4] Starting services...
set "OPENFIELD_CONFIG=config\config.local.yaml"
start "openfield-account" bin\openfield-account.exe
start "openfield-storage" bin\openfield-storage.exe
start "openfield-chat" bin\openfield-chat.exe
start "openfield-posts" bin\openfield-posts.exe
start "openfield-gateway" bin\openfield-gateway.exe

echo [4/4] All services started.
echo   gateway:  http://localhost:8080
echo   account:  http://localhost:8081
echo   storage:  http://localhost:8082
echo   chat:     http://localhost:8083
echo   posts:    http://localhost:8084
echo.
echo Close this window to keep the services running (press any key).
pause >nul
