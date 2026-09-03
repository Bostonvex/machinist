# Deploy Machinist on macOS with DGX Spark inference

This deployment runs the Machinist control plane, dashboard, and coding
harnesses as user LaunchAgents on a Mac. One or more DGX Sparks remain dedicated
inference nodes. The model API is forwarded to Mac loopback through verified
SSH, while harness subscriptions and repository credentials remain on the Mac.

For a two-Spark Ray/vLLM cluster, the shape is:

```text
Mac: Machinist control plane + worker + dashboard + Codex/other harnesses
  127.0.0.1:7331  Machinist UI/API
  127.0.0.1:7900  Buzz observability collector
  127.0.0.1:18000 --verified SSH--> Spark head 127.0.0.1:8000
                                         |
                                         +-- Ray worker on second Spark
```

The Spark head must expose an OpenAI-compatible Responses API. Confirm the model
ID through `GET /v1/models` and a bounded test request before configuring an
agent harness. The shipped Codex profile follows the official
[Codex configuration reference](https://developers.openai.com/codex/config-reference/):
custom providers use a machine-local `base_url`, and `responses` is the
supported custom-provider wire protocol.

## 1. Verify prerequisites

Run as the interactive macOS user, never root:

```sh
uname -sm
command -v codex
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
- installs the bounded second-node NVIDIA adapter under
  `~/.local/libexec/machinist`.

## 3. Add the local Codex provider

Copy `deploy/macos/dgx-spark.config.toml` to
`~/.codex/dgx-spark.config.toml`. Keep it machine-local. It selects the tunneled
Responses API without an API key and uses `--ephemeral` execution when invoked
by the worker. Change the model ID and context window only after querying the
live server.

Verify the tunnel and harness without allowing writes:

```sh
curl -fsS http://127.0.0.1:18000/v1/models
printf '%s\n' 'Respond with exactly LOCAL_OK and do not call tools.' | \
  codex exec --ephemeral --json --sandbox read-only \
  --profile dgx-spark --model ds-0731 -
```

Codex may warn when a private model is absent from its catalog, but it must
complete the Responses API call and report usage. Treat a missing final message
or usage record as a failed compatibility test.

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
token_file = "~/.config/buzz-agent-observability/ingest-token"
identity_salt_file = "~/.config/buzz-agent-observability/identity-salt"
endpoint_id = "mac-mini"

[profiles.dgx-codex]
harness = "codex"
provider = "openai_compatible"
auth_mode = "local"
base_url = "http://127.0.0.1:18000/v1"
base_url_env = "DGX_SPARK_BASE_URL"
command = ["codex", "exec", "--ephemeral", "--json", "--profile", "dgx-spark", "--model={{machinist.model}}", "--sandbox", "danger-full-access", "-"]
models = { local = "ds-0731" }
requires_os = ["darwin"]
requires_arch = ["arm64"]
requires_tags = ["mac-mini", "dgx-client"]

[profiles.dgx-codex-readonly]
harness = "codex"
provider = "openai_compatible"
auth_mode = "local"
base_url = "http://127.0.0.1:18000/v1"
base_url_env = "DGX_SPARK_BASE_URL"
command = ["codex", "exec", "--ephemeral", "--json", "--profile", "dgx-spark", "--model={{machinist.model}}", "--sandbox", "read-only", "-"]
models = { local = "ds-0731" }
requires_os = ["darwin"]
requires_arch = ["arm64"]
requires_tags = ["mac-mini", "dgx-client"]

[profiles.codex-subscription]
harness = "codex"
provider = "openai"
auth_mode = "subscription"
command = ["codex", "exec", "--ephemeral", "--json", "--model={{machinist.model}}", "--sandbox", "danger-full-access", "-"]
models = { luna = "gpt-5.6-luna", terra = "gpt-5.6-terra", sol = "gpt-5.6-sol" }
```

Register only approved absolute repository paths. In
`~/.machinist/config.toml`, enable the collector and use an ordered route:

```toml
[observability]
enabled = true
url = "http://127.0.0.1:7900"

[routes.implementation]
profiles = ["dgx-codex", "codex-subscription"]
max_attempts = 2
max_total_tokens = 200000
fallback_on = ["capacity", "rate_limit", "transient", "model_unavailable", "harness_crash", "timeout"]

[commands.implement]
route = "implementation"
role = "implementer"
prompt_file = "prompts/foreman.md"
timeout = "120m"

[commands.local-check]
profile = "dgx-codex-readonly"
role = "diagnostic"
timeout = "5m"
```

The worker checks a configured local endpoint before every poll. When the SSH
tunnel is down, it does not advertise `dgx-codex`, so new work selects the next
route without consuming an attempt. Existing legacy executors remain available.

## 5. Add both Spark GPUs to observability

Run the collector on Mac loopback. Its vLLM provider should read
`http://127.0.0.1:18000/metrics`; use the built-in strict remote NVIDIA provider
for the head Spark. The collector supports one provider of each type, so use the
installed JSON adapter for the second Spark.

Create a mode-0600 provider file such as
`~/.config/buzz-agent-observability/spark-worker.json`:

```json
{
  "schema_version": 1,
  "scope": "hardware",
  "provider_id": "nvidia-smi",
  "node_id": "spark-worker",
  "endpoint_id": null,
  "argv": ["/Users/USER/.local/libexec/machinist/nvidia-smi-json-provider", "--host", "SPARK_WORKER_ALIAS"],
  "allowed_metrics": [
    "gpu.0.utilization_percent",
    "gpu.0.memory_used_mib",
    "gpu.0.memory_total_mib",
    "gpu.0.temperature_celsius",
    "gpu.0.power_watts"
  ],
  "timeout_seconds": 6
}
```

Add these fixed arguments to the collector service:

```text
--vllm-metrics-url http://127.0.0.1:18000/metrics
--vllm-endpoint-id dgx-spark-cluster
--nvidia-ssh-host <SPARK_HEAD_ALIAS>
--nvidia-ssh-node-id <SPARK_HEAD_NODE_ID>
--json-provider-config /absolute/path/to/spark-worker.json
```

Run `buzz-observability doctor` with the same provider arguments before
restarting its LaunchAgent. The Machinist dashboard keeps the two hardware
`node_id` values and the model `endpoint_id` separate and marks data stale after
45 seconds.

## 6. Verify the deployment

```sh
machinist worker validate
launchctl print gui/$(id -u)/sh.machinist.dgx-tunnel
launchctl print gui/$(id -u)/sh.machinist.control-plane
launchctl print gui/$(id -u)/sh.machinist.worker
curl -fsS http://127.0.0.1:7331/api/v1/status
curl -fsS http://127.0.0.1:7331/api/v1/observability
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
