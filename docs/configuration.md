# Configuration

Commands use an executor, optional prompt template, and timeout:

```toml
[commands.audit]
executor = "codex"
prompt_file = "prompts/audit.md"
timeout = "30m"

[commands.custom-workflow]
executor = "custom-workflow-script"
timeout = "2h"
```

Without `prompt_file`, the input prompt is sent unchanged. With a template, include
`{{machinist.prompt}}`. Executors and repositories remain worker-owned:

```toml
[executors.custom-workflow-script]
command = ["./scripts/custom-workflow.sh"]

[repositories.my-project]
path = "/absolute/path/to/my-project"
```

Managed triggers select one command with `command = "audit"`. Model selection remains
available when the executor command includes `{{machinist.model}}`.

## Typed execution profiles

Profiles add harness, provider, authentication, endpoint, and environment
requirements while keeping credentials and machine-local details on workers.
Legacy executors remain supported.

```toml
[environment]
detect = true
tags = ["trusted", "dgx-spark"]

[profiles.codex-subscription]
harness = "codex"
auth_mode = "subscription"
command = ["codex", "exec", "--json", "--model={{machinist.model}}", "-"]
models = { fast = "gpt-5.6-luna", deep = "gpt-5.6-sol" }

[profiles.deepseek]
harness = "opencode"
provider = "deepseek"
auth_mode = "api_key"
secret_env = "DEEPSEEK_API_KEY"
command = ["opencode", "run", "--model={{machinist.model}}"]
models = { reasoner = "deepseek/deepseek-reasoner" }

[profiles.dgx-local]
harness = "opencode"
provider = "openai_compatible"
auth_mode = "local"
base_url = "http://127.0.0.1:8000/v1"
base_url_env = "OPENAI_BASE_URL"
command = ["opencode", "run", "--model={{machinist.model}}"]
models = { coder = "openai/local-coder" }
requires_tags = ["dgx-spark"]
```

Built-in harness identifiers are `codex`, `claude`, `opencode`, `pi`, and
`generic`, but `harness` accepts any bounded portable identifier. This lets a
worker register a `deepseek`, `aider`, or organization-specific harness without
a Machinist release; `command` remains the complete argument-array adapter.
DeepSeek is normally a provider used through OpenCode or Pi, but a real
DeepSeek-specific CLI can therefore advertise `harness = "deepseek"` directly.
Automatic structured-output normalization is used only when Machinist safely
recognizes the command shape. `subscription` profiles use the harness's existing
signed-in session; API profiles name a secret environment variable but never
send its value to the control plane. A non-loopback HTTP endpoint is rejected
unless `allow_insecure_http = true` is explicitly set.

Profile requirements may use `requires_os`, `requires_arch`, and
`requires_tags`. Operating system and architecture are detected. Tags are
operator assertions and are the only environment facts that may grant trust.
Workers advertise unavailable profiles with a redacted reason but do not accept
work for them.

## Ordered routes

Routes make a command portable across subscription, API, and local inference:

```toml
[routes.implementation]
profiles = ["dgx-local", "codex-subscription", "deepseek"]
max_attempts = 3
max_total_tokens = 150000
fallback_on = ["capacity", "rate_limit", "transient"]

[commands.implement]
route = "implementation"
role = "implementer"
timeout = "90m"
```

The control plane chooses the first compatible candidate across workers that
have advertised the repository within the last 15 seconds and are not already
running a job. For example, an idle connected `dgx-local` worker prevents an
API-only worker from taking the same routed job. If the local worker is busy or
no longer connected, the next compatible profile can claim it. A worker that
has not yet advertised itself cannot be considered, so start persistent
preferred workers before enabling unattended routes.

The selected route and exact profile, harness, provider, authentication mode,
and role are persisted. Route candidates are profile names only; commands,
endpoints, credentials, and paths cannot be overridden through the API.
When an API-key profile runs, environment variables declared as `secret_env` by
other profiles are removed from that child process. The selected profile's key,
subscription session, and ordinary repository credentials remain worker-local.

Each execution is a durable attempt with its own ID and lease fence. A failed
attempt is retried only when its normalized error class appears in `fallback_on`
and `max_attempts` has not been reached. The next attempt rotates to the next
compatible route candidate. Stale attempt or lease completions are rejected.
On attempts after the first, the worker appends a compact handoff containing only
the attempt budget and previous error class. It also exposes these as
`MACHINIST_ATTEMPT_NUMBER`, `MACHINIST_MAX_ATTEMPTS`, and
`MACHINIST_PREVIOUS_ERROR_CLASS`. A configured token ceiling is exposed as
`MACHINIST_MAX_TOTAL_TOKENS`. It does not replay the previous harness
transcript, which limits context growth and avoids leaking error text into a new
provider.

`max_total_tokens` is an optional aggregate ceiling for a route's attempts. The
control plane sums reported usage across completed attempts. Once the ceiling is
reached it stops instead of dispatching another fallback. When a token ceiling is
configured, missing usage is treated conservatively: the retry is stopped because
the remaining budget cannot be proven. The original failure and the budget-stop
reason remain visible in the run detail. Successful and terminal run summaries
report aggregate duration and tokens across all attempts, not just the final one.

Lease-loss recovery remains compatible with legacy executors while recording an
abandoned attempt, so interrupted work is visible rather than silently erased.
Legacy commands without a route receive a bounded two-attempt lease-recovery
budget: one initial execution and one redispatch after a lost worker lease. A
second lost lease fails durably instead of looping forever. Routed commands use
their configured `max_attempts` value.

Current worker classifications are `configuration`, `harness_crash`, `timeout`,
`cancelled`, and `test_failure`. Adapters may additionally report `rate_limit`,
`capacity`, `transient`, `transport`, `authentication`, `policy`, or
`model_unavailable`. Unknown or unlisted failures terminate the run.

## Observability

Worker-side telemetry and the control-plane dashboard bridge are independently
optional. This preserves fail-open execution and permits the collector to keep
its high-frequency data in a separate database.

```toml
# worker.toml
[telemetry]
enabled = true
url = "http://127.0.0.1:7900/api/v1/events"
token_file = "~/.config/buzz-agent-observability/ingest-token"
identity_salt_file = "~/.config/buzz-agent-observability/identity-salt"
endpoint_id = "dgx-primary"

# config.toml
[observability]
enabled = true
url = "http://127.0.0.1:7900"
```

See [Observability bridge](observability.md) for the privacy boundary,
compatibility aliases, dashboard metrics, and collector topology.

## Migration

The `agents` table was renamed to `commands`. Move `[agents.NAME]` to `[commands.NAME]`
and replace `--agent` with `--command`.

The pipeline feature was removed. Replace a sequential pipeline with one executable script,
configure that script as an approved worker executor, and expose it through one command.
Legacy `[pipelines]` configuration fails with migration guidance. Pre-command databases are
recreated once because this release intentionally consolidates the schema before active use.
