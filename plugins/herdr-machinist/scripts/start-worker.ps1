$ErrorActionPreference = "Stop"

if (-not $env:HERDR_PLUGIN_STATE_DIR) { throw "HERDR_PLUGIN_STATE_DIR is required" }
if (-not $env:HERDR_SOCKET_PATH) { throw "HERDR_SOCKET_PATH is required" }
$sessionName = Split-Path -Leaf (Split-Path -Parent $env:HERDR_SOCKET_PATH)
if ($sessionName -ne "machinist") { exit 0 }

$stateDirectory = $env:HERDR_PLUGIN_STATE_DIR
New-Item -ItemType Directory -Force -Path $stateDirectory | Out-Null
$pidFile = Join-Path $stateDirectory "worker.pid"
if (Test-Path -LiteralPath $pidFile) {
    $oldPid = [int](Get-Content -LiteralPath $pidFile -TotalCount 1)
    if (Get-Process -Id $oldPid -ErrorAction SilentlyContinue) { exit 0 }
}

$binary = $env:MACHINIST_BIN
if (-not $binary -or -not (Test-Path -LiteralPath $binary -PathType Leaf)) {
    $command = Get-Command machinist -ErrorAction SilentlyContinue
    if ($command) { $binary = $command.Source }
}
if (-not $binary) {
    $candidate = Join-Path $HOME ".local\bin\machinist.exe"
    if (Test-Path -LiteralPath $candidate -PathType Leaf) { $binary = $candidate }
}
if (-not $binary) { throw "Machinist is not installed; interactive worker was not started." }

$stdout = Join-Path $stateDirectory "worker.stdout.log"
$stderr = Join-Path $stateDirectory "worker.stderr.log"
$process = Start-Process -FilePath $binary -ArgumentList @("worker", "start", "--transport", "herdr") -WindowStyle Hidden -PassThru -RedirectStandardOutput $stdout -RedirectStandardError $stderr
Set-Content -LiteralPath $pidFile -Value $process.Id
