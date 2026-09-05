# Deploy Machinist on macOS with DGX Spark inference

This deployment runs the Machinist control plane, dashboard, and coding
harnesses as user LaunchAgents on a Mac. One or more DGX Sparks remain dedicated
inference nodes. The model API is forwarded to Mac loopback through verified
SSH, while harness subscriptions and repository credentials remain on the Mac.

For a two-Spark Ray/vLLM cluster, the shape is:

```text
Mac: Machinist control plane + worker + dashboard + DeepCode/other harnesses
  127.0.0.1:7331  Machinist UI/API
  127.0.0.1:7900  Buzz observability collector
  127.0.0.1:18000 --verified SSH--> Spark head 127.0.0.1:8000
                                         |
                                         +-- Ray worker on second Spark
```

The Spark head must expose an OpenAI-compatible Chat Completions API; keeping
the Responses API enabled also preserves Codex as an optional fallback. Confirm
the model ID through `GET /v1/models` and bounded requests before configuring a
harness.

## 1. Verify prerequisites

Run as the interactive macOS user, never root:

```sh
uname -sm
command -v codex
command -v node
node --version
ssh -o BatchMode=yes -o StrictHostKeyChecking=yes <SPARK_HEAD_ALIAS> true
curl -fsS http://<SPARK_HEAD_PRIVATE_ADDRESS>:8000/v1/models
```

Verify the Spark host key out of band before the first SSH connection. The
LaunchAgent refuses unknown or changed keys and never uses `accept-new` or
disables host verification.

## 2. Build and install the canary

From a clean reviewed Machinist checkout:

```sh
mkdir -p bin
go build -trimpath -ldflags='-X main.version=VERSION' \
  -o bin/machinist ./cmd/machinist

MACHINIST_BINARY="$PWD/bin/machinist" \
MACHINIST_NODE_ROLE=all \
MACHINIST_DGX_SSH_HOST=<SPARK_HEAD_ALIAS> \
MACHINIST_DGX_LOCAL_PORT=18000 \
MACHINIST_DGX_REMOTE_PORT=8000 \
bash scripts/setup-macos.sh
```

For a published release, omit `MACHINIST_BINARY` and set both
`MACHINIST_VERSION` and `MACHINIST_REPOSITORY`. The installer verifies release
checksums. The script:

- installs the binary under `~/.local/bin`;
- initializes `~/.machinist` without overwriting existing files;
- installs separate control-plane, worker, and DGX-tunnel LaunchAgents;
- backs up a changed plist before replacing it;
- starts the worker only when `machinist worker validate` succeeds;
- links the bundled Herdr plugin when Herdr is installed and the plugin source
  is present beside the installer;
- installs the bounded second-node NVIDIA adapter under
  `~/.local/libexec/machinist`.

## 3. Install and configure DeepCode

DeepCode requires Node.js 22 or newer. Pin the tested package version on the
Mac mini:

```sh
npm install --global @vegamo/deepcode-cli@0.3.1
deepcode --version
```

Keep endpoint and model selection in the worker profile. Put only local
behavior policy in `~/.deepcode/settings.json`; the recommended trusted-worker
policy is shown in the [Herdr guide](herdr.md#dgx-spark-local-model-through-deepcode).

Verify the tunnel and harness without allowing writes:

```sh
curl -fsS http://127.0.0.1:18000/v1/models
DEEPCODE_BASE_URL=http://127.0.0.1:18000/v1 \
DEEPCODE_API_KEY=local \
DEEPCODE_MODEL=ds-0731 \
DEEPCODE_THINKING_ENABLED=false \
DEEPCODE_TELEMETRY_ENABLED=0 \
deepcode --exec --prompt 'Respond with exactly LOCAL_OK and do not call tools.'
```

The command must print `LOCAL_OK` and leave a completed session with usage in
`~/.deepcode/projects/*/sessions-index.json`. Treat a missing final message or
usage record as a failed compatibility test. The optional
`deploy/macos/dgx-spark.config.toml` remains available for a Codex fallback.

## 4. Configure Machinist routing

Add a trusted Mac environment and typed profiles to `~/.machinist/worker.toml`:

```toml
name = "mac-mini"

[environment]
detect = true
tags = ["mac-mini", "dgx-client", "trusted"]

[telemetry]
enabled = true
url = "http://127.0.0.1:7900/api/v1/events"
token_file = "~/.machinist/collector/ingest-token"
identity_salt_file = "~/.machinist/collector/identity-salt"
endpoint_id = "mac-mini"

[profiles.dgx-deepcode]
harness = "deepcode"
provider = "openai_compatible"
auth_mode = "local"
base_url = "http://127.0.0.1:18000/v1"
base_url_env = "DEEPCODE_BASE_URL"
command = ["/absolute/path/to/machinist/plugins/herdr-machinist/scripts/run-deepcode.sh", "--model={{machinist.model}}"]
herdr_command = ["/absolute/path/to/machinist/plugins/herdr-machinist/scripts/run-deepcode-herdr.sh", "--model={{machinist.model}}"]
models = { local = "ds-0731" }
requires_executables = ["deepcode", "node"]
requires_os = ["darwin"]
requires_arch = ["arm64"]
requires_tags = ["mac-mini", "dgx-client"]

[profiles.codex-subscription]
harness = "codex"
provider = "openai"
auth_mode = "subscription"
command = ["codex", "exec", "--ephemeral", "--json", "--model={{machinist.model}}", "--sandbox", "danger-full-access", "-"]
herdr_agent = "codex"
herdr_args = ["--model={{machinist.model}}", "--sandbox", "danger-full-access"]
models = { luna = "gpt-5.6-luna", terra = "gpt-5.6-terra", sol = "gpt-5.6-sol" }
```

Register only approved absolute repository paths. In
`~/.machinist/config.toml`, enable the collector and use an ordered route:

```toml
[observability]
enabled = true
url = "http://127.0.0.1:7900"

[routes.implementation]
profiles = ["dgx-deepcode", "codex-subscription"]
max_attempts = 2
max_total_tokens = 200000
fallback_on = ["capacity", "rate_limit", "transient", "model_unavailable", "harness_crash", "timeout"]

[commands.implement]
route = "implementation"
role = "implementer"
prompt_file = "prompts/foreman.md"
timeout = "120m"

[commands.local-check]
profile = "dgx-deepcode"
role = "diagnostic"
timeout = "5m"
```

The worker checks a configured local endpoint before every poll. When the SSH
tunnel is down, it does not advertise `dgx-deepcode`, so new work selects the next
route without consuming an attempt. Existing legacy executors remain available.

## 5. Add both Spark GPUs to observability

Machinist serves the collector. Run it on Mac loopback with `machinist collector
start` and configure it under `[collector]` in the Machinist config — there is no
separate collector binary and no provider flags on the service.

Each GPU node gets its own `[[collector.nvidia_remote]]` table. The table repeats
because a deployment reaches the number of nodes it reaches, and a node nobody
polls is indistinguishable from a node that is idle:

```toml
[collector]
enabled = true
listen = "127.0.0.1:7900"
database = "~/.machinist/collector/telemetry.db"
token_file = "~/.machinist/collector/ingest-token"
identity_salt_file = "~/.machinist/collector/identity-salt"

[collector.vllm]
metrics_url = "http://127.0.0.1:18000/metrics"
endpoint_id = "vllm-primary"

[[collector.nvidia_remote]]
node_id = "spark-0e9f"
ssh_host = "spark-0e9f"

[[collector.nvidia_remote]]
node_id = "spark-27c2"
ssh_host = "spark-27c2"
```

`node_id` is what names each node, and the config refuses two that share one: two
Sparks under one name would share a status row, and an operator reading a failure
could not tell which machine had stopped answering. Past one remote node the name
must be given explicitly rather than defaulted. Each appears in `/healthz` as
`nvidia-smi-remote:<node_id>`.

Reaching a Spark is strict-host-verified SSH; the alias must already resolve and
verify from this account without a prompt, or the provider fails and says so.

Check the collector before trusting it:

```sh
machinist collector doctor
curl -fsS http://127.0.0.1:7900/healthz
```

The Machinist dashboard keeps the two hardware `node_id` values and the model
`endpoint_id` separate, and marks data stale after 45 seconds rather than showing
it as a healthy zero.

## 6. Verify the deployment

```sh
machinist worker validate
launchctl print gui/$(id -u)/sh.machinist.dgx-tunnel
launchctl print gui/$(id -u)/sh.machinist.control-plane
launchctl print gui/$(id -u)/sh.machinist.worker
curl -fsS http://127.0.0.1:7331/api/v1/status
curl -fsS http://127.0.0.1:7331/api/v1/observability
herdr plugin action list --plugin bostonvex.machinist
herdr --session machinist
```

Open `http://127.0.0.1:7331`, submit a narrow canary, and verify the selected
profile, reported token total, attempt fence, environment digest, cancellation,
and all three DGX/model identities. Do not expose ports 7331, 7900, or 18000.

## Roll back

LaunchAgent replacements are backed up beside their plists. Stop Machinist with:

```sh
launchctl bootout gui/$(id -u)/sh.machinist.worker
launchctl bootout gui/$(id -u)/sh.machinist.control-plane
launchctl bootout gui/$(id -u)/sh.machinist.dgx-tunnel
```

Restore the previous plist/configuration and binary, then bootstrap the restored
plist. Back up `~/.machinist/server/machinist.db` before an upgrade and preserve
the prior binary until the canary and paired Buzz/Machinist gates pass.
