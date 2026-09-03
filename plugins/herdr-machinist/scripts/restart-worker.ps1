$ErrorActionPreference = "Stop"
$scriptDirectory = Split-Path -Parent $MyInvocation.MyCommand.Path
& (Join-Path $scriptDirectory "stop-worker.ps1")
& (Join-Path $scriptDirectory "start-worker.ps1")
