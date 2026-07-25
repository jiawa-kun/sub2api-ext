# Deploy sub2api-ext via SSH + Docker Compose.
# Flow: cross-compile linux binary locally -> scp sources -> remote docker build (alpine) -> compose up
#
# Usage:
#   .\scripts\deploy-server.ps1
#   .\scripts\deploy-server.ps1 -Logs
#   .\scripts\deploy-server.ps1 -Down

param(
    [string]$HostName = "your-vps",
    [int]$SshPort = 22,
    [string]$RemoteDir = "/opt/sub2api-ext",
    [string]$LegacyRemoteDir = "/opt/sub2api-checkin",
    [string]$LegacyContainerName = "sub2api-checkin",
    [string]$ContainerName = "sub2api-ext",
    [string]$ImageName = "sub2api-ext:local",
    [string]$ServiceName = "ext",
    [int]$WebPort = 8090,
    [string]$IdentityFile = "",
    [switch]$NoCache,
    [switch]$Logs,
    [switch]$Down
)

$ErrorActionPreference = "Stop"
$ProjectRoot = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
Set-Location -LiteralPath $ProjectRoot

function Invoke-SSH([string]$RemoteCommand) {
    # Windows here-strings may contain CRLF; bash rejects CR in scripts.
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

$binPath = Join-Path $ProjectRoot "bin\sub2api-ext-linux-amd64"
Write-Host "==> cross-compile linux binary"
$env:CGO_ENABLED = "0"
$env:GOOS = "linux"
$env:GOARCH = "amd64"
if (-not $env:GOPROXY) { $env:GOPROXY = "https://goproxy.cn,direct" }
& go build -ldflags="-s -w" -o $binPath ./cmd/server
if ($LASTEXITCODE -ne 0) { throw "go build failed" }
if (-not (Test-Path -LiteralPath $binPath)) { throw "binary missing: $binPath" }

Write-Host "==> ensure remote dir"
Invoke-SSH "mkdir -p $RemoteDir/data $RemoteDir/bin $RemoteDir/configs $RemoteDir/deploy"

Write-Host "==> migrate from legacy project name if needed"
Invoke-SSH @"
set -e
NEW_DIR="$RemoteDir"
OLD_DIR="$LegacyRemoteDir"
OLD_CTN="$LegacyContainerName"
NEW_CTN="$ContainerName"
# always free port 8090 from legacy/new leftovers
for ctn in "$OLD_CTN" "$NEW_CTN" sub2api-checkin sub2api-ext; do
  if docker ps -a --format '{{.Names}}' | grep -qx "$ctn"; then
    echo "removing container $ctn"
    docker stop "$ctn" >/dev/null 2>&1 || true
    docker rm -f "$ctn" >/dev/null 2>&1 || true
  fi
done
if [ -d "$OLD_DIR" ]; then
  # prefer legacy runtime env over example-generated env
  if [ -f "$OLD_DIR/.env" ]; then
    if [ ! -f "$NEW_DIR/.env" ] || [ "$OLD_DIR/.env" -nt "$NEW_DIR/.env" ] || ! grep -q 'SUB2API_ADMIN' "$NEW_DIR/.env" 2>/dev/null; then
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
"@



Write-Host "==> upload files"
Invoke-SCP @(
    (Join-Path $ProjectRoot "docker-compose.yml"),
    (Join-Path $ProjectRoot "Dockerfile.runtime"),
    (Join-Path $ProjectRoot ".env.example"),
    (Join-Path $ProjectRoot "configs\config.example.yaml")
) "$RemoteDir/"
Invoke-SCP @((Join-Path $ProjectRoot "deploy\nginx-checkin.snippet.conf")) "$RemoteDir/deploy/"
Invoke-SCP @($binPath) "$RemoteDir/bin/"

# Keep remote .env; create from example if absent
Invoke-SSH @"
set -e
cd $RemoteDir
cp Dockerfile.runtime Dockerfile
mkdir -p bin configs
if [ ! -f .env ]; then
  cp .env.example .env
  echo CREATED_DEFAULT_ENV=1
else
  echo KEEP_EXISTING_ENV=1
fi
if [ ! -f configs/config.example.yaml ] && [ -f config.example.yaml ]; then
  mv config.example.yaml configs/config.example.yaml || true
fi
# config.example may have been uploaded to root
if [ -f config.example.yaml ]; then
  mkdir -p configs
  mv -f config.example.yaml configs/config.example.yaml
fi
ls -la
ls -la bin
"@

Write-Host "==> remote docker build + up"
$cacheFlag = ""
if ($NoCache) { $cacheFlag = "--no-cache" }
Invoke-SSH @"
set -e
cd $RemoteDir
# ensure Dockerfile COPY paths exist
test -f bin/sub2api-ext-linux-amd64
test -f configs/config.example.yaml || test -f config.example.yaml
if [ ! -f configs/config.example.yaml ] && [ -f config.example.yaml ]; then
  mkdir -p configs && cp config.example.yaml configs/config.example.yaml
fi
# volume 需对容器内 nonroot(65532) 可写
mkdir -p data
chmod 777 data || true
docker build $cacheFlag -f Dockerfile -t $ImageName .
docker compose up -d --force-recreate --no-deps $ServiceName
sleep 2
echo '--- health ---'
curl -sS -o /tmp/checkin_health.out -w 'health_http=%{http_code}\n' http://127.0.0.1:$WebPort/checkin/healthz || true
cat /tmp/checkin_health.out 2>/dev/null || true
echo '--- ready ---'
curl -sS -o /tmp/checkin_ready.out -w 'ready_http=%{http_code}\n' http://127.0.0.1:$WebPort/checkin/readyz || true
cat /tmp/checkin_ready.out 2>/dev/null || true
echo '--- metrics ---'
curl -sS http://127.0.0.1:$WebPort/checkin/metrics 2>/dev/null | head -c 400 || true
echo
echo '--- ps ---'
docker ps --filter name=$ContainerName --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}'
echo '--- logs ---'
docker logs --tail 40 $ContainerName || true
"@

Write-Host ""
Write-Host "==> deploy done"
Write-Host "Manual steps if first time:"
Write-Host "  1) ssh $HostName `"nano $RemoteDir/.env`"  # set SUB2API_ADMIN_TOKEN"
Write-Host "  2) ssh $HostName `"cd $RemoteDir && docker compose up -d --force-recreate $ServiceName`""
Write-Host "  3) add nginx location /checkin/ (snippet in $RemoteDir/deploy/) then: sudo nginx -t && sudo systemctl reload nginx"
Write-Host "  4) Sub2API custom menu URL: https://your-sub2api.example.com/checkin/"
