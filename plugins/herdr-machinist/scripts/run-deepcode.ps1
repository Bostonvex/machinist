param(
    [string]$Model = ""
)

$PromptText = [Console]::In.ReadToEnd()
if ([string]::IsNullOrWhiteSpace($PromptText)) {
    Write-Error "DeepCode requires a prompt on standard input."
    exit 2
}

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$DeepCodeBin = if ($env:DEEPCODE_BIN_PATH) {
    $env:DEEPCODE_BIN_PATH
} elseif (Get-Command deepcode -ErrorAction SilentlyContinue) {
    (Get-Command deepcode).Source
} elseif (Test-Path "$HOME/.local/bin/deepcode") {
    "$HOME/.local/bin/deepcode"
} else {
    Write-Error "deepcode was not found; add it to PATH or set DEEPCODE_BIN_PATH"
    exit 127
}
$NodeBin = if ($env:NODE_BIN_PATH) { $env:NODE_BIN_PATH } else { "node" }
$ProjectRoot = (Resolve-Path (Get-Location).Path).Path
$Before = & $NodeBin (Join-Path $ScriptDir "deepcode-session.mjs") snapshot $ProjectRoot

if (-not $env:DEEPCODE_API_KEY) { $env:DEEPCODE_API_KEY = "local" }
if (-not $env:DEEPCODE_BASE_URL) {
    $env:DEEPCODE_BASE_URL = if ($env:MACHINIST_DGX_DEEPCODE_BASE_URL) {
        $env:MACHINIST_DGX_DEEPCODE_BASE_URL
    } else {
        "http://127.0.0.1:18000/v1"
    }
}
if (-not $env:DEEPCODE_TELEMETRY_ENABLED) { $env:DEEPCODE_TELEMETRY_ENABLED = "0" }
if (-not $env:DEEPCODE_THINKING_ENABLED) { $env:DEEPCODE_THINKING_ENABLED = "false" }
if ($Model) { $env:DEEPCODE_MODEL = $Model }

& $DeepCodeBin --exec --prompt $PromptText
$Status = $LASTEXITCODE
& $NodeBin (Join-Path $ScriptDir "deepcode-session.mjs") usage $ProjectRoot $Before
exit $Status
