$ErrorActionPreference = "Stop"

$binary = $env:MACHINIST_BIN
if (-not $binary -or -not (Test-Path -LiteralPath $binary -PathType Leaf)) {
    $command = Get-Command machinist -ErrorAction SilentlyContinue
    if ($command) { $binary = $command.Source }
}
if (-not $binary) {
    $candidate = Join-Path $HOME ".local\bin\machinist.exe"
    if (Test-Path -LiteralPath $candidate -PathType Leaf) { $binary = $candidate }
}
if (-not $binary) { throw "Machinist is not installed or available on PATH." }
& $binary @args
exit $LASTEXITCODE
