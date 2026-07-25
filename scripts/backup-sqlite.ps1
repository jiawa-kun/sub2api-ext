# Backup local or remote checkin SQLite
param(
    [string]$SqlitePath = "",
    [string]$BackupDir = "",
    [string]$HostName = "",
    [string]$RemoteDir = "/opt/sub2api-ext"
)
$ErrorActionPreference = "Stop"
$ProjectRoot = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
if (-not $SqlitePath) { $SqlitePath = Join-Path $ProjectRoot "data\checkin.db" }
if (-not $BackupDir) { $BackupDir = Join-Path $ProjectRoot "data\backups" }

$ts = Get-Date -Format "yyyyMMdd-HHmmss"
if ($HostName) {
    $remoteBak = "$RemoteDir/data/backups"
    ssh -o BatchMode=yes $HostName "mkdir -p $remoteBak && cp -a $RemoteDir/data/checkin.db $remoteBak/checkin-$ts.db && ls -lh $remoteBak/checkin-$ts.db"
    Write-Host "remote backup ok: $remoteBak/checkin-$ts.db"
    return
}

if (-not (Test-Path -LiteralPath $SqlitePath)) { throw "sqlite not found: $SqlitePath" }
New-Item -ItemType Directory -Force -Path $BackupDir | Out-Null
$dest = Join-Path $BackupDir "checkin-$ts.db"
Copy-Item -LiteralPath $SqlitePath -Destination $dest -Force
Write-Host "backup ok: $dest"
