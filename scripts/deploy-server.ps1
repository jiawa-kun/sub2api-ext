# Deploy sub2api-ext by pulling image from GitHub Container Registry (GHCR).
# Flow: upload compose/config -> remote docker pull -> compose up
#
# Usage:
#   .\scripts\deploy-server.ps1 -HostName your-vps -RemoteDir /opt/sub2api-ext
#   .\scripts\deploy-server.ps1 -Image ghcr.io/jiawa-kun/sub2api-ext:latest
#   .\scripts\deploy-server.ps1 -Logs
#   .\scripts\deploy-server.ps1 -Down

param(
    [string]$HostName = "your-vps",
    [int]$SshPort = 22,
    [string]$RemoteDir = "/opt/sub2api-ext",
    # Legacy names only used for one-time migration from old project.
    [string]$LegacyRemoteDir = "/opt/sub2api-checkin",
    [string]$LegacyContainerName = "sub2api-checkin",
    [string]$ContainerName = "sub2api-ext",
    [string]$Image = "ghcr.io/jiawa-kun/sub2api-ext:latest",
    [string]$ServiceName = "ext",
    [int]$WebPort = 8090,
    [string]$IdentityFile = "",
    [switch]$Logs,
    [switch]$Down
)

$ErrorActionPreference = "Stop"
$ProjectRoot = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
Set-Location -LiteralPath $ProjectRoot

function Invoke-SSH([string]$RemoteCommand) {
    $RemoteCommand = $RemoteCommand -replace "`r`n", "`n" -replace "`r", "`n"
    $sshArgs = @("-p", "$SshPort", "-o", "BatchMode=yes", "-o", "ConnectTimeout=20")
    if ($IdentityFile) { $sshArgs += @("-i", $IdentityFile) }
    $sshArgs += @($HostName, $RemoteCommand)
    & ssh @sshArgs
    if ($LASTEXITCODE -ne 0) { throw "ssh failed: $RemoteCommand" }
}

function Invoke-SCP([string[]]$Locals, [string]$Remote) {
    $scpArgs = @("-P", "$SshPort", "-o", "BatchMode=yes")
    if ($IdentityFile) { $scpArgs += @("-i", $IdentityFile) }
    $scpArgs += $Locals
    $scpArgs += "${HostName}:${Remote}"
    & scp @scpArgs
    if ($LASTEXITCODE -ne 0) { throw "scp failed -> $Remote" }
}

if ($Logs) {
    Invoke-SSH "docker logs --tail 200 -f $ContainerName"
    return
}

if ($Down) {
    Invoke-SSH "cd $RemoteDir && docker compose stop $ServiceName || docker stop $ContainerName || true"
    Write-Host "stopped $ContainerName"
    return
}

Write-Host "==> check ssh $HostName"
Invoke-SSH "echo ssh-ok && docker compose version >/dev/null && docker info >/dev/null"

Write-Host "==> ensure remote dir"
Invoke-SSH "mkdir -p $RemoteDir/data $RemoteDir/configs $RemoteDir/deploy"

Write-Host "==> migrate from legacy project name if needed"
# Use single-quoted here-string so bash $vars are not eaten by PowerShell.
# Inject only known PowerShell values via string replace.
$migrate = @'
set -e
NEW_DIR="__REMOTE_DIR__"
OLD_DIR="__LEGACY_DIR__"
OLD_CTN="__LEGACY_CTN__"
if [ -n "$OLD_CTN" ] && docker ps -a --format '{{.Names}}' | grep -qx "$OLD_CTN"; then
  echo "removing legacy container $OLD_CTN"
  docker stop "$OLD_CTN" >/dev/null 2>&1 || true
  docker rm -f "$OLD_CTN" >/dev/null 2>&1 || true
fi
if [ -d "$OLD_DIR" ]; then
  if [ -f "$OLD_DIR/.env" ]; then
    if [ ! -f "$NEW_DIR/.env" ] || ! grep -q 'SUB2API_ADMIN' "$NEW_DIR/.env" 2>/dev/null; then
      cp -a "$OLD_DIR/.env" "$NEW_DIR/.env"
      echo MIGRATED_ENV=1
    fi
  fi
  if [ -d "$OLD_DIR/data" ]; then
    mkdir -p "$NEW_DIR/data"
    if [ ! -f "$NEW_DIR/data/checkin.db" ] && [ -f "$OLD_DIR/data/checkin.db" ]; then
      cp -a "$OLD_DIR/data/." "$NEW_DIR/data/"
      echo MIGRATED_DATA=1
    fi
  fi
fi
'@
$migrate = $migrate.Replace("__REMOTE_DIR__", $RemoteDir).Replace("__LEGACY_DIR__", $LegacyRemoteDir).Replace("__LEGACY_CTN__", $LegacyContainerName)
Invoke-SSH $migrate

Write-Host "==> upload compose/config"
Invoke-SCP @(
    (Join-Path $ProjectRoot "docker-compose.yml"),
    (Join-Path $ProjectRoot ".env.example")
) "$RemoteDir/"
$snippet = Join-Path $ProjectRoot "deploy\nginx-ext.snippet.conf"
if (Test-Path $snippet) {
    Invoke-SCP @($snippet) "$RemoteDir/deploy/"
}

Write-Host "==> prepare remote .env and pin IMAGE=$Image"
$prep = @'
set -e
cd "__REMOTE_DIR__"
if [ ! -f .env ]; then
  cp .env.example .env
  echo CREATED_DEFAULT_ENV=1
else
  echo KEEP_EXISTING_ENV=1
fi
if grep -q '^IMAGE=' .env 2>/dev/null; then
  sed -i "s|^IMAGE=.*|IMAGE=__IMAGE__|" .env
else
  printf "\nIMAGE=__IMAGE__\n" >> .env
fi
mkdir -p data
chmod 777 data || true
'@
$prep = $prep.Replace("__REMOTE_DIR__", $RemoteDir).Replace("__IMAGE__", $Image)
Invoke-SSH $prep

Write-Host "==> remote pull image and recreate: $Image"
$up = @'
set -e
cd "__REMOTE_DIR__"
export IMAGE="__IMAGE__"
echo "pulling $IMAGE"
docker pull "$IMAGE"
docker compose up -d --force-recreate --no-deps __SERVICE__
sleep 2
set +e
echo "--- health ---"
curl -sS -w "\nhealth_http=%{http_code}\n" "http://127.0.0.1:__WEBPORT__/ext/healthz"
echo "--- ready ---"
curl -sS -w "\nready_http=%{http_code}\n" "http://127.0.0.1:__WEBPORT__/ext/readyz"
echo "--- home ---"
curl -sS -o /dev/null -w "home_http=%{http_code}\n" "http://127.0.0.1:__WEBPORT__/ext/home.html"
echo "--- ps ---"
docker ps --filter "name=__CONTAINER__"
echo "--- logs ---"
docker logs --tail 40 "__CONTAINER__" || true
'@
$up = $up.Replace("__REMOTE_DIR__", $RemoteDir).Replace("__IMAGE__", $Image).Replace("__SERVICE__", $ServiceName).Replace("__WEBPORT__", "$WebPort").Replace("__CONTAINER__", $ContainerName)
Invoke-SSH $up

Write-Host ""
Write-Host "==> deploy done (image pull mode)"
Write-Host "Image: $Image"
Write-Host "Tips:"
Write-Host "  1) ensure GHCR package is public, or docker login ghcr.io on server"
Write-Host "  2) first time: edit remote .env (SUB2API_ADMIN_API_KEY / SUB2API_PUBLIC_HOST)"
Write-Host "  3) custom menu URL example: https://your-sub2api.example.com/ext/"
