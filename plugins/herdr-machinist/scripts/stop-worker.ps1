$ErrorActionPreference = "Stop"
if (-not $env:HERDR_PLUGIN_STATE_DIR) { throw "HERDR_PLUGIN_STATE_DIR is required" }
if (-not $env:HERDR_SOCKET_PATH) { throw "HERDR_SOCKET_PATH is required" }
$sessionName = Split-Path -Leaf (Split-Path -Parent $env:HERDR_SOCKET_PATH)
if ($sessionName -notmatch '^[A-Za-z0-9._-]+$') { exit 0 }
$stateDirectory = Join-Path (Join-Path $env:HERDR_PLUGIN_STATE_DIR "sessions") $sessionName
$pidFile = Join-Path $stateDirectory "worker.pid"
if (-not (Test-Path -LiteralPath $pidFile)) { exit 0 }
$workerPid = [int](Get-Content -LiteralPath $pidFile -TotalCount 1)
Stop-Process -Id $workerPid -ErrorAction SilentlyContinue
Remove-Item -LiteralPath $pidFile -Force -ErrorAction SilentlyContinue
