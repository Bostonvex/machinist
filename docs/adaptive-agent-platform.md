# Adaptive agent platform plan

Status: release candidate implemented; measured pilot and production cutover pending

This plan evolves Machinist into the default orchestration layer for unattended
agentic coding while preserving the useful controls from Buzz Workspace and the
Agent Software Factory (ASF). The migration is deliberately additive: current
Machinist command and executor configuration remains valid throughout the
cutover, and every release can be rolled back without rewriting worker secrets
or repositories.

## Decision

Adopt Machinist as the control plane and durable work ledger, but do not perform
a big-bang replacement of Buzz/ASF. First add worker-local execution profiles,
portable environment discovery, safe attempts and observability. Pilot the new
path alongside the existing workflow. Switch the default only after measured
speed, token, repair, and unattended-completion gates pass.

Machinist is a better foundation because its control-plane/worker boundary is
small, credentials and repository authority remain on workers, work is durable,
and any stdin-driven executable can already be used. It is not a complete
replacement today: routing is executor/model only, a job has one run, Windows
is unsupported, and its UI does not expose deep agent, cache, GPU, or retry
telemetry. Those gaps are the scope of this plan.

## Release-candidate implementation status

The current branch implements the execution-platform portion of this plan:

- environment manifests and worker capability health;
- typed multi-harness profiles for generic commands, Codex, Claude Code,
  OpenCode, Pi, and arbitrary portable custom harness identifiers;
- subscription, API-key, DeepSeek, local, and OpenAI-compatible provider
  configurations with worker-local credentials;
- ordered routes, model aliases, fenced attempts, classified fallback, compact
  retry handoff, remote cancellation, and process-tree termination;
- a fail-open read-only bridge to the existing agent/token/cache/DGX collector
  and an Agents & infra dashboard inside the Machinist UI;
- native macOS, Linux, and Windows amd64/arm64 builds, including Windows Job
  Object cancellation and staged self-update;
- schema migrations through version 7 with data-preservation coverage.

The release candidate does not yet contain a native semantic role graph,
GitHub `FACTORY:RUN` writer, independent-review policy engine, production
merge/deploy engine, measured Buzz-vs-Machinist result set, or automatic
telemetry-based capacity routing. These remain ASF/repository workflow
responsibilities or pilot gates. The fail-closed paired-task evaluator and
synthetic format example are in [the cutover benchmark](../benchmarks/README.md).
See also [the source-verified comparison](buzz-asf-comparison.md).

## Side-by-side capability matrix

| Capability | Machinist baseline | Buzz / ASF adaptation | Target implementation |
| --- | --- | --- | --- |
| Durable queue and leases | Built in | Less centralised | Keep Machinist store and fencing |
| Worker-owned credentials | Built in | Subscription sessions on hosts | Keep secrets local; advertise only symbolic capability IDs |
| Arbitrary harness | Generic command | Per-agent harness choice | Typed profiles for Codex, Claude Code, OpenCode, Pi, and generic commands |
| Subscription-backed use | Possible through CLI executors | Core cost control | First-class `auth_mode = "subscription"`; never proxy subscription sessions |
| API providers | Command-specific | Multiple model/provider choices | Provider-neutral profiles with local secret references |
| DeepSeek | Manual executable only | Desired | DeepSeek API through compatible harnesses and optional direct OpenAI-compatible profile |
| Local models | Manual executable only | DGX/local workflows | Ollama and OpenAI-compatible endpoints through OpenCode/Pi/generic profiles |
| Per-agent model selection | Executor alias map | Built in | Route + role + profile + model, resolved into an immutable execution plan |
| Environment awareness | None in scheduling | Host-specific scripts | Detect OS, architecture, WSL/container, shell, tools, accelerators, and operator tags |
| Native Windows | Not built | Required | WSL in pilot; native Windows x64 runner and service in release B |
| Retry/fallback | Lease reclaim repeats the same run | Autonomous repair loops | Explicit attempts, retry classification, budgets, idempotency, fallback routes, and fencing |
| Unattended safety | Human PR handoff | Permission controls | Policy envelope, time/token/cost limits, protected operations, and terminal escalation |
| Token accounting | Optional total tokens | Detailed input/output/cache data | Normalised input/output/reasoning/cached tokens plus provenance and coverage |
| Prompt cache | Not separated | Token/cache dashboard | Report provider prompt-cache reads/writes separately from server KV cache |
| KV cache / GPU | None | DGX Spark views | Reuse the telemetry collector for vLLM and NVIDIA/DGX metrics |
| Agent observability | Run state and retained events | Agent/turn/tool/stall views | Correlated job/run/attempt/agent events with live and historical UI |
| UI | Runs, workers, triggers, commands, basic analytics | Rich DGX/agent dashboard | Add Work, Agents, Models & Infra, Tokens & Cache, Routing, Reliability |
| Deployment | Binary/VM scripts and releases | Local services | Signed artifacts, migrations, health checks, canary, rollback, macOS/Linux/WSL first |

## Concepts and ownership

The control plane owns portable intent; the worker owns executable details and
credentials. These concepts must remain distinct:

- **role**: planner, implementer, reviewer, verifier, or an operator-defined role.
- **route**: an ordered policy for selecting one or more profiles.
- **profile**: a worker-local harness/provider/auth combination.
- **harness**: the executable agent interface (a built-in such as `codex`,
  `claude`, `opencode`, `pi`, or `generic`, or a portable custom identifier).
- **provider**: where inference happens (`openai`, `anthropic`, `deepseek`,
  `ollama`, `openai_compatible`, or another named provider).
- **model**: an alias exposed by the profile, never an unbounded command value.
- **attempt**: one fenced execution of a run using one immutable plan.
- **environment**: detected worker facts plus trusted operator tags.

The server dispatches only capabilities a worker advertised. The chosen plan is
persisted before execution and includes the exact route, profile, harness,
provider, model, worker environment digest, retry ordinal, and policy digest.
This makes a result explainable and prevents configuration drift mid-attempt.

## Configuration contract

Legacy executors remain valid:

```toml
[executors.codex]
command = ["codex", "exec", "--json", "--model={{machinist.model}}", "-"]
[executors.codex.models]
sol = "gpt-5.6-sol"
```

New profiles are worker-local. Secret values are never sent to the control
plane; `secret_env` names an environment variable that exists on the worker.

```toml
[environment]
detect = true
tags = ["trusted", "dgx-spark"]

[profiles.codex-subscription]
harness = "codex"
auth_mode = "subscription"
command = ["codex", "exec", "--json", "--model={{machinist.model}}", "-"]
[profiles.codex-subscription.models]
fast = "gpt-5.6-luna"
deep = "gpt-5.6-sol"

[profiles.deepseek]
harness = "opencode"
provider = "deepseek"
auth_mode = "api_key"
secret_env = "DEEPSEEK_API_KEY"
command = ["opencode", "run", "--model={{machinist.model}}"]
[profiles.deepseek.models]
reasoner = "deepseek/deepseek-reasoner"

[profiles.dgx-local]
harness = "opencode"
provider = "openai_compatible"
auth_mode = "local"
base_url = "http://127.0.0.1:8000/v1"
command = ["opencode", "run", "--model={{machinist.model}}"]
requires_tags = ["dgx-spark"]
[profiles.dgx-local.models]
coder = "openai/local-coder"
```

Portable routes belong to the control-plane configuration:

```toml
[routes.implementation]
profiles = ["dgx-local", "codex-subscription", "deepseek"]
max_attempts = 3
max_total_tokens = 150000
fallback_on = ["capacity", "rate_limit", "transient", "model_unavailable"]

[commands.implement]
route = "implementation"
role = "implementer"
timeout = "90m"
```

An explicit profile may be selected per command or submission. Raw executable,
base URL, path, or secret overrides are never accepted over the server API.

## Environment discovery

Discovery is on by default and fail-safe. It reports bounded, non-secret facts:

- OS and architecture (`darwin/arm64`, `linux/amd64`, `windows/amd64`).
- execution environment (`native`, `wsl`, or `container`).
- default shell family and path semantics.
- Machinist version and supported protocol features.
- configured profile IDs and model aliases.
- availability/version of configured harness executables.
- optional accelerator facts and trusted operator tags.

It does not report environment variables, filesystem paths, prompts, source,
tool payloads, credentials, or command-line session tokens. Detected facts are
advisory; operator tags are the only facts allowed to confer trust or authority.

Platform execution uses argument arrays rather than shell interpolation. The
release candidate supports native macOS/Linux plus native Windows amd64/arm64.
The Windows runner uses Job Objects for process-tree cancellation and includes
Windows path handling, PowerShell-aware service installation, and release
artifacts. Legacy 32-bit x86 Windows is not targeted.

## Harness adapters

Adapters provide validation and normalisation around the existing process
runner; they do not embed provider SDKs into the worker.

Each adapter implements:

1. configuration validation and executable probe;
2. an argv/environment execution plan with stdin prompt handling;
3. cancellation and timeout behaviour;
4. structured event and usage parsing;
5. error classification without retaining prompt or response content.

The generic adapter preserves current behaviour. Codex and Claude adapters
preserve subscription-backed CLI sessions. OpenCode and Pi enable DeepSeek and
local/OpenAI-compatible providers. Provider environment is allow-listed per
adapter and added only to the child process.

## Attempts, autonomy, and safety

Lease expiry must not silently rerun side effects. Release A retains the current
single-run model but records the execution plan. Release B introduces attempts:

- A monotonic attempt ordinal and unique attempt ID.
- A lease token bound to the attempt; stale completions cannot win.
- Heartbeat, cancellation request, cancellation acknowledgement, and deadline.
- Error classes: configuration, authentication, policy, rate limit, capacity,
  transient transport, harness crash, timeout, test failure, and unknown.
- Retry/fallback only for configured classes and within attempt, elapsed-time,
  token, and optional cost budgets.
- Repository-level concurrency keys and an idempotency key for external actions.
- No automatic merge, deploy, secret mutation, destructive data operation, or
  protection-rule bypass unless an explicit policy grants it for that repo and
  environment.
- Terminal escalation with a concise evidence bundle when the policy is
  exhausted or a human-only gate is reached.

For this repository build, merge and deployment are authorised only after the
test, migration, security, health, and rollback gates below pass.

## Observability and UI

Keep `buzz-agent-observability` as a separately versioned, fail-open telemetry
service. Machinist's transactional SQLite database must not absorb high-volume
agent/GPU samples. A provider-neutral transport sends bounded metadata through
the control plane to the collector for remote workers, or directly to a
loopback collector on a single host.

Correlation fields are `job_id`, `run_id`, `attempt_id`, `command`, `role`,
`route`, `profile`, `harness`, `provider`, `model`, `worker_instance`, and an
environment digest. Events cover attempt lifecycle, agent state, turns, tool
spans, first-action/first-valid-token latency, stalls, outcomes, token samples,
route decisions, retries, cache metrics, and infrastructure samples.

Privacy defaults remain metadata-only: no prompts, responses, reasoning,
source, filesystem paths, arbitrary environment values, or tool payloads.
Telemetry failure must never fail work. Buffers are bounded and dropped-event
counts are visible.

The existing UI remains the operator entry point and gains:

- **Work**: queue, attempt timeline, live state, retry/fallback reason, budgets,
  cancel, and safe requeue.
- **Agents**: active harness/model/role, turn/tool progress, elapsed time,
  stalls, and last heartbeat.
- **Models & Infra**: endpoint health, vLLM running/waiting, throughput, TTFT,
  GPU utilisation/memory/temperature/power, and DGX Spark status.
- **Tokens & Cache**: input/output/reasoning/cached tokens, coverage, token rate,
  prompt-cache reads/writes, and server KV cache utilisation kept explicitly
  separate.
- **Routing**: candidate profiles, decision reason, environment compatibility,
  fallback chain, and capacity advisory.
- **Reliability**: completion rate, repair/retry rate, lease recovery,
  cancellation latency, duplicate-prevention evidence, and telemetry loss.

Shared GPU metrics are never attributed to a specific agent unless request-level
correlation proves the attribution. Capacity-aware routing starts advisory;
automatic routing follows only after stale-data rules, thresholds, hysteresis,
and a deterministic fallback are validated.

## Delivery plan

### Phase 0 — baseline and contracts

- Pin the upstream revision and record green backend/frontend baselines.
- Add this decision record, protocol compatibility rules, threat model, and
  benchmark fixture.
- Define success metrics and capture a Buzz/ASF comparison baseline.

Exit: current configuration and tests remain green; metrics are reproducible.

### Phase 1 — worker facts and execution profiles

- Add bounded environment discovery and protocol feature negotiation.
- Persist/display worker platform and capability health.
- Add profile schema, validation, adapters, immutable plan rendering, and legacy
  executor translation.
- Ship Codex, Claude, generic, OpenCode, and Pi adapters.

Exit: legacy configs are byte-for-byte valid; incompatible jobs are never
dispatched; secrets do not cross the worker boundary.

### Phase 2 — DeepSeek and local inference

- Add DeepSeek examples/probes and usage normalisation.
- Add Ollama/OpenAI-compatible local examples, endpoint health, and explicit
  network policy.
- Validate subscription, API, and local routes independently.

Exit: the same command can be routed among subscription, API, and local profiles
without changing its prompt or exposing credentials.

### Phase 3 — durable attempts and unattended policies

- Migrate one-run jobs to fenced attempts without losing history.
- Add retry/fallback classification, budgets, cancellation, idempotency, and
  recovery tests for worker/control-plane/network failure.
- Add repository and environment policy gates for autonomous merge/deploy.

Exit: fault-injection tests show no stale completion or uncontrolled duplicate;
budget exhaustion produces an actionable terminal escalation.

### Phase 4 — telemetry and dashboard

- Add provider-neutral event transport and collector compatibility aliases.
- Add the six dashboard views and event-stream updates.
- Measure telemetry overhead, loss, retention, and redaction.

Exit: p95 worker overhead is below 2%, the collector can be unavailable without
affecting jobs, and every metric shows provenance/coverage.

### Phase 5 — platform, pilot, and cutover

- Produce macOS arm64, Linux amd64/arm64, and WSL pilot artifacts.
- Add native Windows amd64 runner/service and cross-platform conformance tests.
- Shadow representative Buzz/ASF work, then canary 10%, 25%, 50%, and 100%.
- Freeze new Buzz/ASF work only after the acceptance window passes; retain a
  read-only archive and export mappings.

Exit: two consecutive weeks meet the acceptance gates with no critical safety
or data-loss incident.

## Acceptance gates

Measured against the current Buzz/ASF baseline on comparable tasks:

- at least 30% lower median active cycle time;
- at least 40% lower median reported token usage per accepted change;
- at least 30% fewer repair/rework attempts per accepted change;
- at least 90% of policy-eligible low-risk work reaches handoff unattended;
- zero incompatible dispatches, secret disclosures, stale completion wins, or
  unbounded duplicate attempts;
- 100% cancellation and restart recovery in the fault-injection suite;
- at least 95% token-reporting coverage for harnesses claiming structured usage;
- telemetry p95 CPU overhead below 2% and bounded memory/disk queues;
- macOS/Linux/WSL conformance is green before pilot, native Windows conformance
  before Windows production use.

If token savings come only from a quality regression, the gate fails: accepted
change and repair-rate metrics are evaluated together.

## Deployment and rollback

Deployment proceeds only from a clean reviewed commit:

1. run frontend tests/build and verify embedded assets are current;
2. run formatting, vet, Go tests with race detection, and binary smoke tests;
3. test schema upgrade and restore from a copied production-like database;
4. scan dependencies and the diff for credential material;
5. build versioned artifacts and record checksums;
6. deploy a canary control plane and one worker with telemetry fail-open;
7. verify health, poll/lease/complete, cancellation, UI, and collector status;
8. expand only while error, queue, token, and latency guardrails remain healthy.

Rollback stops new admissions, drains or safely cancels active attempts, restores
the prior binary and configuration, and (when a schema change is not backward
readable) restores the pre-deploy database copy. Worker profiles and secrets are
kept compatible with the previous release until the acceptance window closes.
Buzz/ASF remains available as the operational fallback during the pilot.

## Indicative schedule

With two engineers, the complete target is roughly 8–11 weeks: 3–5 days for
baselines/contracts, 1–2 weeks for profiles/adapters, one week for DeepSeek/local,
1–2 weeks for discovery/scheduling, two weeks for attempts/reliability, 1–2
weeks for Windows/platform work, one week for telemetry/security, and two weeks
for pilot/cutover. One engineer should plan for roughly 12–16 weeks. Release A
(macOS/Linux/WSL, multi-harness, DeepSeek/local, environment-aware static
routing) lands earlier; Release B adds attempts/fallback, native Windows, and
the complete observability surface.
