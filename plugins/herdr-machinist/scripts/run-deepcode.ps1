param(
    [string]$Model = ""
)

$PromptText = [Console]::In.ReadToEnd()
if ([string]::IsNullOrWhiteSpace($PromptText)) {
    Write-Error "DeepCode requires a prompt on standard input."
    exit 2
}

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$DeepCodeBin = if ($env:DEEPCODE_BIN_PATH) { $env:DEEPCODE_BIN_PATH } else { "deepcode" }
$NodeBin = if ($env:NODE_BIN_PATH) { $env:NODE_BIN_PATH } else { "node" }
$ProjectRoot = (Resolve-Path (Get-Location).Path).Path
$Before = & $NodeBin (Join-Path $ScriptDir "deepcode-session.mjs") snapshot $ProjectRoot

if (-not $env:DEEPCODE_API_KEY) { $env:DEEPCODE_API_KEY = "local" }
if (-not $env:DEEPCODE_TELEMETRY_ENABLED) { $env:DEEPCODE_TELEMETRY_ENABLED = "0" }
if (-not $env:DEEPCODE_THINKING_ENABLED) { $env:DEEPCODE_THINKING_ENABLED = "false" }
if ($Model) { $env:DEEPCODE_MODEL = $Model }

& $DeepCodeBin --exec --prompt $PromptText
$Status = $LASTEXITCODE
& $NodeBin (Join-Path $ScriptDir "deepcode-session.mjs") usage $ProjectRoot $Before
exit $Status
