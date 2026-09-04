param(
    [string]$Model = ""
)

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$DeepCodeBin = if ($env:DEEPCODE_BIN_PATH) { $env:DEEPCODE_BIN_PATH } else { "deepcode" }
$NodeBin = if ($env:NODE_BIN_PATH) { $env:NODE_BIN_PATH } else { "node" }
$ProjectRoot = (Resolve-Path (Get-Location).Path).Path

if (-not $env:DEEPCODE_API_KEY) { $env:DEEPCODE_API_KEY = "local" }
if (-not $env:DEEPCODE_TELEMETRY_ENABLED) { $env:DEEPCODE_TELEMETRY_ENABLED = "0" }
if (-not $env:DEEPCODE_THINKING_ENABLED) { $env:DEEPCODE_THINKING_ENABLED = "false" }
if ($Model) { $env:DEEPCODE_MODEL = $Model }

$Observer = Start-Process -FilePath $NodeBin -ArgumentList @(
    (Join-Path $ScriptDir "deepcode-session.mjs"),
    "observe",
    $ProjectRoot
) -PassThru -NoNewWindow

try {
    & $DeepCodeBin
    exit $LASTEXITCODE
}
finally {
    if (-not $Observer.HasExited) {
        Stop-Process -Id $Observer.Id -ErrorAction SilentlyContinue
        $Observer.WaitForExit(2000) | Out-Null
    }
}
