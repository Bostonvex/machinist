$ErrorActionPreference = "Stop"
if ($args.Count -ne 1) { throw "plugin pane entrypoint is required" }
$herdr = if ($env:HERDR_BIN_PATH) { $env:HERDR_BIN_PATH } else { "herdr" }
& $herdr plugin pane open --plugin bostonvex.machinist --entrypoint $args[0]
exit $LASTEXITCODE
