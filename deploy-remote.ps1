$ErrorActionPreference = "Stop"
$ProjectRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$log = Join-Path $ProjectRoot "deploy-remote.log"
& powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $ProjectRoot "scripts\deploy-server.ps1") *>&1 |
  Tee-Object -FilePath $log
