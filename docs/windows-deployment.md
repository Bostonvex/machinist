# Run Machinist on Windows

Machinist supports native Windows workers and control planes on `amd64` and
`arm64`. The worker advertises Windows path and shell capabilities
automatically, and profiles can require `os = ["windows"]` so POSIX-only
commands are never scheduled there.

## Install and configure

Download the matching `windows_amd64` or `windows_arm64` release archive,
verify it against `checksums.txt`, and put `machinist.exe` on `PATH`. The same
archive format is used on every platform so `machinist update` can verify and
install future releases.

Initialize configuration from PowerShell:

```powershell
machinist.exe init
machinist.exe worker validate
```

The default files live under the current user's profile. Keep the control
plane on a loopback address and use an SSH or VPN tunnel for remote access.
Do not expose the unauthenticated dashboard directly to a network.

Use argument arrays for platform-specific launchers. Machinist does not pass
commands through a shell unless the configured executable is itself a shell:

```toml
[profiles.codex-windows]
harness = "codex"
auth_mode = "subscription"
command = ["codex.exe", "exec", "-"]
requires_os = ["windows"]

[profiles.local-windows]
harness = "opencode"
provider = "local"
auth_mode = "local"
command = ["opencode.exe", "run"]
base_url = "http://127.0.0.1:11434/v1"
requires_os = ["windows"]
```

Use a separate POSIX profile for scripts that depend on `sh`, `bash`, Unix
signals, or forward-slash paths. WSL workers identify themselves as Linux with
`execution = "wsl"`; they should use Linux binaries and paths, not the native
Windows profiles above.

## Process lifecycle

Every native Windows agent process is assigned to a kill-on-close Job Object.
Timeout, service shutdown, and remote task cancellation terminate the complete
child process tree. If the host places Machinist in a job that prevents nested
assignment, Machinist uses Windows `taskkill /T /F` as a bounded fallback.

The dashboard's **Stop task** action marks queued work terminal immediately.
For running work, the control plane returns a cancellation instruction on the
next heartbeat (normally within ten seconds), the worker ends the Job Object,
and a lease fence prevents a late success response from reversing the cancel.

## Run unattended

Run the control plane and worker under a dedicated, unprivileged Windows
account using your existing service manager or Task Scheduler. Authenticate
subscription-backed harnesses interactively once as that same account. Keep
API keys in the environment or secret manager referenced by `secret_env`; do
not put secret values in TOML.

Validate after every configuration change:

```powershell
machinist.exe worker validate
machinist.exe version
```

When a local model server or telemetry collector runs on the same host, bind
it to `127.0.0.1`. Local-provider and telemetry URLs deliberately reject
unexpected remote plaintext endpoints unless the profile explicitly opts into
that risk.
