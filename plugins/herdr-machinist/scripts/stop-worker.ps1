$ErrorActionPreference = "Stop"
if (-not $env:HERDR_PLUGIN_STATE_DIR) { throw "HERDR_PLUGIN_STATE_DIR is required" }
$pidFile = Join-Path $env:HERDR_PLUGIN_STATE_DIR "worker.pid"
if (-not (Test-Path -LiteralPath $pidFile)) { exit 0 }
$workerPid = [int](Get-Content -LiteralPath $pidFile -TotalCount 1)
Stop-Process -Id $workerPid -ErrorAction SilentlyContinue
Remove-Item -LiteralPath $pidFile -Force -ErrorAction SilentlyContinue
