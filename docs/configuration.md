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

The control-plane admission catalog is the union of current worker repository
registrations. The latest disconnected worker remains registered so work can be
queued while it is offline. To retire a repository, remove it from every worker
that declares it; after those workers poll again (and superseded instances age
out), new submissions are rejected while historical jobs remain visible.

Managed triggers select one command with `command = "audit"`. Model selection remains
available when the executor command includes `{{machinist.model}}`.

## Triggers

Triggers create bounded managed jobs for a fixed command and repository. Cron
triggers use a five-field schedule and an explicit IANA timezone:

```toml
[github.repositories]
my-project = "owner/my-project"

[triggers.cron.nightly-audit]
schedule = "0 2 * * *"
timezone = "America/New_York"
repository = "my-project"
command = "audit"
model = "fast"
prompt = "Audit request validation and persistence for correctness defects."
```

Interval triggers replace `schedule` and `timezone` with `every = "6h"`.
GitHub triggers poll configured repositories for an authorized request label.
All trigger families resolve the command, model, repository mapping, and prompt
at startup; they cannot inject a machine-local path or executable.

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
herdr_agent = "codex"
herdr_args = ["--model={{machinist.model}}", "--sandbox", "danger-full-access"]
models = { fast = "gpt-5.6-luna", deep = "gpt-5.6-sol" }

[profiles.deepseek]
harness = "opencode"
provider = "deepseek"
auth_mode = "api_key"
secret_env = "DEEPSEEK_API_KEY"
command = ["opencode", "run", "--model={{machinist.model}}"]
herdr_agent = "opencode"
herdr_args = ["--model={{machinist.model}}"]
models = { reasoner = "deepseek/deepseek-reasoner" }

[profiles.dgx-deepcode]
harness = "deepcode"
provider = "openai_compatible"
auth_mode = "local"
base_url = "http://127.0.0.1:8000/v1"
base_url_env = "DEEPCODE_BASE_URL"
command = ["/absolute/path/to/machinist/plugins/herdr-machinist/scripts/run-deepcode.sh", "--model={{machinist.model}}"]
herdr_command = ["/absolute/path/to/machinist/plugins/herdr-machinist/scripts/run-deepcode-herdr.sh", "--model={{machinist.model}}"]
models = { coder = "local-coder" }
requires_executables = ["deepcode", "node"]
requires_tags = ["dgx-spark"]
```

Common harness identifiers are `codex`, `claude`, `deepcode`, `opencode`, `pi`,
and `generic`, but `harness` accepts any bounded portable identifier. This lets
a worker register Aider or an organization-specific harness without a
Machinist release; `command` remains the complete argument-array adapter.
Automatic structured-output normalization is used only when Machinist safely
recognizes the command shape. `subscription` profiles use the harness's existing
signed-in session; API profiles name a secret environment variable but never
send its value to the control plane. A non-loopback HTTP endpoint is rejected
unless `allow_insecure_http = true` is explicitly set.

Profile requirements may use `requires_os`, `requires_arch`,
`requires_executables`, and `requires_tags`. Operating system, architecture,
the command adapter, and every required executable are checked locally. Tags
are operator assertions and are the only environment facts that may grant
trust. Workers advertise unavailable profiles with a redacted reason but do
not accept work for them.

`command` is the non-interactive process adapter. The same logical profile may
define either native `herdr_agent`/`herdr_args` or a self-reporting
`herdr_command` as its interactive adapter. The model alias is resolved once
and inserted into both argument arrays. A Herdr worker advertises only profiles
with one of those interactive adapters; this prevents an interactive job from
being claimed by a headless-only profile. Native Herdr agent kinds are
validated again by the installed Herdr binary when the pane starts.

## Ordered routes

Routes make a command portable across subscription, API, and local inference:

```toml
[routes.implementation]
profiles = ["dgx-deepcode", "codex-subscription", "deepseek"]
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
running a job. For example, an idle connected `dgx-deepcode` worker prevents an
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

## Collector

Machinist serves the collector itself. `[observability]` says where the control
plane *reads* a collector from; `[collector]` says where one is offered, and
`machinist collector start` runs it. A deployment can do either without the
other.

```toml
# config.toml
[collector]
enabled = true
listen = "127.0.0.1:7900"
database = "~/.machinist/collector/telemetry.db"
token_file = "~/.config/buzz-agent-observability/ingest-token"
identity_salt_file = "~/.config/buzz-agent-observability/identity-salt"
retention = "168h"
provider_interval = "10s"

[collector.vllm]
metrics_url = "http://127.0.0.1:18000/metrics"
endpoint_id = "vllm-primary"

[collector.nvidia]
node_id = "local-nvidia"

[collector.nvidia_remote]
node_id = "dgx-spark"
ssh_host = "spark"
```

`listen` must be a literal loopback host. The collector is a live description of
what every agent on this machine is doing; reaching it from elsewhere is an SSH
tunnel, not a line in a config file.

The telemetry database is deliberately not the control plane's. It is a
high-volume append-only record with its own retention, and putting it beside
transactional state invites the two being backed up, copied and truncated as one
thing.

Both secrets are required and are created on first start. Neither is defaulted:
a token this process invented would be a credential nobody knows exists, and the
producers that must present it are configured in `worker.toml`. The identity
salt is never read by the collector — producers hash their identities with it
before they send anything, so it has to exist before the first event does.

Each provider table may appear at most once. Two providers under one name would
share a status row, and an operator reading it could not tell which of them was
failing. `[collector.nvidia]` polls this machine and `[collector.nvidia_remote]`
polls another over strict-host-verified SSH; neither substitutes for the other,
because which machine a reading describes is the whole content of the reading. A
provider that cannot be built stops the collector rather than being skipped: a
poller silently absent is indistinguishable from hardware that is idle.

### Operating a collector

Four verbs, all reading the same `[collector]` section. None of them can be
pointed at a database no configured collector owns, and none run at all when
`enabled` is false.

```
machinist collector start
machinist collector doctor
machinist collector backup --output ~/backups/telemetry-2026-09-05.db
machinist collector purge  --before 2026-08-01T00:00:00Z --confirm-delete-raw-events
```

`doctor` inspects what starting would depend on — both secret files, the
database, and one bounded poll from every configured provider — and prints a
JSON report, exiting non-zero when any check failed. It creates nothing and
repairs nothing. An absent token is the finding, not a step to take: a doctor
that made the file it went looking for would report a healthy deployment it had
just repaired, and would answer for a collector that has never started as though
it had. It runs every check rather than stopping at the first failure, so
repairing three problems takes one invocation rather than three.

`backup` writes a consistent copy with SQLite's `VACUUM INTO`, not a file copy.
The database is written to while a backup runs, so copying the file and its
write-ahead log with `cp` captures the two at different instants and produces an
archive that only fails to open on the day somebody needs it. The copy is staged
beside the destination at mode 0600 and linked into place, so a backup never
overwrites one that already exists — destroying an earlier backup is the one
thing this must never do.

`purge` deletes raw events and infrastructure samples observed before a cutoff.
Agents and turns are kept: they are small, they are what a reader actually asks
about, and deleting them would erase that an agent ever ran rather than the
detail of what it did. The cutoff must carry a timezone, because every
`observed_at` is UTC and reading a zoneless one as local time would, east of
Greenwich, delete hours of events that have not expired — with nothing
afterwards to show it. The confirmation is a flag rather than a prompt: this
runs from launchd and cron at least as often as from a terminal, and a prompt
there is not a question but a command that hangs.

## Migration

The `agents` table was renamed to `commands`. Move `[agents.NAME]` to `[commands.NAME]`
and replace `--agent` with `--command`.

The pipeline feature was removed. Replace a sequential pipeline with one executable script,
configure that script as an approved worker executor, and expose it through one command.
Legacy `[pipelines]` configuration fails with migration guidance. Pre-command databases are
recreated once because this release intentionally consolidates the schema before active use.
