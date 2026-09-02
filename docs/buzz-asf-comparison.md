# Buzz/ASF to Machinist assessment

Assessment date: 2026-09-02

Source revisions reviewed:

- Machinist upstream baseline: `fda16c207da88c7dac32aac6473839fb2b1906b8`
- Buzz Workspace: `b63b379ca1b3e61216fded993819570e55302d60`
- Agent Software Factory (ASF): `1a3a2d8674cf22bed8124f1e89507a40400c1074`
- Buzz telemetry collector integration baseline: `e66ba4ef0f9e99121695c1259ffd3cd413a236ec`

This document distinguishes the reviewed upstream Machinist baseline from the
adaptive-harness release candidate in this branch. It does not treat plans as
shipped features, and it does not claim speed or token savings that have not yet
been measured on comparable work.

## Recommendation

Use Machinist as the queue, execution control plane, run ledger, and operator UI.
Keep the valuable ASF role/policy layer and repository-owned workflows on top of
it. Retain Buzz during a measured pilot for human communications, durable project
knowledge, and operational fallback; do not make Buzz the execution scheduler for
new low-risk work once the canary gates pass.

This is a staged switch, not a replacement of every Buzz/ASF concept:

- **Switch now for a canary** when work has a bounded command, a clean repository,
  machine-checkable validation, no production secret requirement, and a pull
  request or evidence bundle as its terminal output.
- **Keep ASF** for role definitions, independent review, compact context packs,
  protected-path policy, repair budgets, and structured handoffs. Machinist runs
  the selected harness; ASF defines how software work moves between roles.
- **Keep Buzz temporarily** when work depends on channel discussion, Decisions,
  host-qualified agent mentions, or the existing knowledge/work-log layout.
- **Do not switch unattended production changes yet** when a workflow can merge,
  deploy, mutate production data, spend money, or change credentials without an
  explicit repository policy and a tested rollback. The operator has authorised
  this repository's merge and deploy, but that one-time authorisation is not a
  general product policy.

The expected benefit is strongest for execution-heavy coding runs that currently
pay coordination and transcript overhead in Buzz. It is weaker for collaborative
research or decisions whose value is the shared conversation itself.

## Side-by-side feature and functionality matrix

| Area | Machinist upstream baseline | Buzz Workspace | Agent Software Factory | Adaptive Machinist release candidate | Port decision |
| --- | --- | --- | --- | --- | --- |
| Primary purpose | Local/remote command runner with durable jobs | Multi-agent workspace, channels, knowledge, host fleet | Portable role, policy, and handoff kit | Durable, environment-aware multi-harness control plane | Use Machinist for execution; retain ASF policy and selected Buzz knowledge patterns |
| System of record | SQLite jobs/runs/events | GitHub plus workspace files and channels | GitHub issues, PRs, labels, comments | SQLite execution ledger; GitHub remains product-work ledger | Do not create a second product-planning truth inside Machinist |
| Work tracking UI | Runs, workers, triggers, commands, basic analytics | Canvas/kanban scripts and channels | GitHub issue/PR state | Built-in work board, run detail, attempt timeline, workers, triggers, commands, analytics, agents/infra | Switch execution tracking to Machinist UI; keep GitHub for issue/PR lifecycle |
| Agent/infra UI | No deep model or GPU view | Custom observability and DGX dashboard | No hosted dashboard | Read-only collector bridge for agents, turns, tokens, prompt cache, server KV cache, vLLM, GPU/DGX | Reuse the collector; keep high-volume telemetry out of transactional SQLite |
| Harness support | Any stdin executable, manually configured | Harness and model selected per agent | Runtime adapters for Codex, Claude Code, Cursor, Buzz | Typed `generic`, `codex`, `claude`, `opencode`, and `pi` profiles | Ported as a configurable profile abstraction, not hard-coded vendors |
| Per-role harness/model | Executor/model alias, no role type | Yes, per agent | Yes, role-to-runtime/model/environment binding | Command role plus ordered profile route and model alias | Ported; independent-author/reviewer enforcement remains an ASF concern |
| Subscription-backed models | Possible through a configured signed-in CLI | Important cost-control path | Depends on bound CLI/runtime | First-class `auth_mode = "subscription"`; CLI session remains worker-local | Ported without relaying session credentials or pretending subscription use is API use |
| API providers | Per-executor manual setup | Configurable per agent/harness | Runtime-specific | `auth_mode = "api_key"` with a worker-local `secret_env` reference | Ported; secret values never cross the worker boundary |
| DeepSeek | Manual generic executor | Requested additional harness/provider | Can be bound through a capable runtime | DeepSeek provider through OpenCode, Pi, or another compatible command | Ported as provider configuration; no invented proprietary DeepSeek CLI contract |
| Local models | Manual generic executor | DGX/local workflows | Environment binding, no inference server | Loopback/OpenAI-compatible or Ollama-capable CLI profiles; DGX tags and telemetry | Ported; model server lifecycle stays outside Machinist |
| Environment awareness | No scheduling facts | Separate Mac/Windows fleets and host-qualified identities | Role environment binding | OS, architecture, native/WSL/container, shell/path style, features, tags, digest | Ported as bounded non-secret facts; only operator tags grant trust |
| Windows | Unsupported | Win32-specific agents and PowerShell tooling | Generated runtime instructions vary by adapter | Native Windows amd64/arm64 worker, Job Object cancellation, updater, CI and release archives | Ported. Legacy 32-bit x86 Windows is deliberately not targeted |
| Routing/fallback | Executor and model matching | Human/orchestrator dispatch | Orchestrator role and graph handoffs | Ordered compatible profiles, classified retry allowlist, fallback rotation | Ported for execution failures; semantic role graph stays in ASF/workflow scripts |
| Retry/repair budget | Lease reclaim can repeat a run | Agent repair loops | `automatic_task_retry` and per-root-cause repair cap | Fenced attempts, `max_attempts`, `fallback_on`, compact retry handoff | Ported; root-cause-aware semantic repair remains workflow-level |
| Duplicate/stale protection | Lease token | Claims and GitHub state discipline | Idempotent start/claim markers | Attempt IDs, lease fencing, stale-completion rejection, expired-attempt record | Strengthened in Machinist |
| Remote stop | Timeout/local cancellation | Human messaging/process management | Escalation protocol | Authenticated job cancellation delivered by heartbeat; process-tree termination | Ported and tested |
| Context efficiency | Prompt file or submitted prompt | Token habits, written artifacts, avoid repeated context | Generated context packs under about 8 KB; hard limits inline, prose by pointer | Prompt files/pointers plus a small retry-only failure-class handoff | Adopt ASF packs; do not stream Buzz transcript history into every run |
| Durable handoff | Retained run events/result | Work logs, outbox, messages | Compact `FACTORY:RUN` marker and PR evidence | Run/attempt IDs and artifacts; no native GitHub handoff marker writer | Keep ASF/GitHub marker; correlate it to Machinist job/run IDs |
| Role separation | Not modelled | Writer/checker convention | Explicit roles; cross-vendor reviewer encouraged; enforcement varies by runtime | Roles are tagged and independently routable but separation is not policy-enforced | Retain ASF rules and branch protection; add core enforcement only after pilot evidence |
| Command safety | Named commands and repositories | House rules and agent permissions | Portable guard decisions plus runtime-specific enforcement | Named profiles/routes, no remote raw paths/commands/secrets; worker owns authority | Keep both layers; guards are defense in depth, not a substitute for OS isolation |
| Merge/deploy | Intentionally hands back a PR | Tiered explicit authorisation | Human merge default; deploy always explicit | Can run an authorised repository-owned command, but has no native PR/deploy policy engine | Retain ASF gates and require named authorisation/target |
| Unattended operation | Managed workers and triggers | Tickers, leases, agents in channels | Issue event source and orchestrator protocol | Durable workers/triggers, retry/fallback, cancellation, telemetry, UI | Prefer Machinist for bounded low-risk unattended runs |
| Updates/releases | Cross-platform binary release flow | Repository-specific scripts | Kit version/render checks | Six macOS/Linux/Windows archives, checksums, updater and release verification | Ported and extended |

## Analysis against the stated goals

### Increase speed

Machinist removes several coordination steps from the hot path: a job is admitted
once, a compatible worker polls it directly, and the worker invokes the harness
without channel dispatch or repeated roster/context discovery. Local inference can
also remove provider queue/network latency when the local model is suitable.

It cannot make Codex, Claude, DeepSeek, or a local model reason faster. Repository
setup, tests, build time, and model latency will still dominate many runs. Speed
must therefore be measured from admission to accepted handoff, with failed or
repaired changes included rather than comparing only successful first attempts.

### Reduce token consumption

The largest reliable savings should come from changing the information shape:

- send a bounded role context pack and pointers, not a Buzz transcript;
- persist evidence in GitHub and repository artifacts so the next role reads only
  what it needs;
- use cheap/local/subscription profiles for appropriate roles and reserve deeper
  models for planning or independent review;
- pass only the previous failure class on fallback instead of replaying the prior
  model conversation;
- measure input, output, reasoning, and prompt-cache tokens where the harness
  exposes them, while keeping server KV cache as a separate infrastructure metric.

Machinist cannot force a third-party CLI to expose complete usage data. The UI must
show missing coverage rather than treating it as zero. Subscription use can reduce
incremental API spend, but it does not mean the underlying work consumed no tokens.

### Reduce rework

Fenced attempts prevent late workers from overwriting newer results. Classified
fallback prevents every failure from becoming a blind retry. Environment matching
prevents work reaching a worker that lacks the required OS, architecture, harness,
model alias, credential mode, or trusted tag. The retry handoff tells the next
harness what class of failure occurred and reminds it to inspect existing state.

Quality rework is different from infrastructure retry. Preserve ASF's planner,
implementer, reviewer, and CI-investigator contracts; route reviewer work to a
different harness/model when practical. Machinist records the executions but does
not, by itself, prove that author and reviewer are independent.

### Increase unattended autonomy

The release candidate can stay useful while the operator is away because queue
state, attempts, leases, retries, cancellations, and worker capabilities are
durable. Its UI is the primary place to see what is queued, running, retried,
stalled at the collector, failed, or complete.

Autonomy remains bounded. A repository-owned orchestration command may create a
branch, run tests, and open a draft PR. Merge or deployment should occur only when
the named repository/PR/target has explicit authority, required checks are green,
the working tree and head SHA are revalidated, and rollback is known.

## Useful adaptations retained

From Buzz Workspace:

- local credentials and subscription sessions stay on the execution host;
- environment and fleet differences are explicit rather than hidden in prompts;
- project knowledge, research, plans, work logs, and shareable outputs remain
  repository artifacts instead of recurring transcript payload;
- operational dashboards distinguish stale/offline data from healthy zero values;
- DGX, agent, token, prompt-cache, KV-cache, and GPU telemetry remain visible.

From ASF:

- roles are runtime-neutral and can be bound to different harnesses/models;
- context packs keep hard limits inline and link to detailed procedures;
- retry/repair work is budgeted;
- the implementer and reviewer should use independent seats when possible;
- `FACTORY:RUN` and PR evidence remain the compact cross-session handoff;
- protected operations and paths use policy plus technical enforcement;
- merge/deploy require explicit authority rather than being inferred from a coding
  task.

## Adaptations intentionally not copied into Machinist core

- Buzz channels and `@mention` dispatch are collaboration infrastructure, not an
  execution protocol.
- Host-qualified agent display names are replaced by detected worker capabilities
  and stable worker instance IDs.
- GitHub labels/comments are not duplicated into SQLite as a second planning state;
  only execution correlation belongs in Machinist.
- Machinist does not invent a workflow DAG for opaque scripts. ASF or a
  repository-owned orchestrator remains responsible for role sequencing and
  checkpoints.
- High-volume collector samples stay in the telemetry service, not the control
  plane database.
- A provider-specific DeepSeek adapter is unnecessary while supported CLIs can use
  its API through an OpenAI-compatible/provider configuration.

## Cutover plan

1. **Freeze the comparison baseline.** Record the revisions above and capture at
   least 20 representative accepted Buzz/ASF changes with cycle time, cloud
   billable tokens, repair attempts, operator touches, and outcome quality.
2. **Deploy the control plane as a canary.** Back up its database, deploy one
   control plane and one worker, connect the collector read-only, and verify health,
   UI, lease/complete, cancellation, restart, and fail-open telemetry.
3. **Register worker-local profiles.** Start with one subscription profile, one API
   fallback, and one local/DGX profile. Advertise aliases only after executable,
   session/secret, endpoint, OS/architecture, and tag probes pass.
4. **Wrap the ASF workflow.** Make one approved repository command consume a compact
   context pack, follow ASF role/guard/repair rules, produce GitHub handoff evidence,
   and end at a draft PR. Keep merge/deploy outside the generic coding command.
5. **Shadow, then canary.** Replay or shadow tasks without merging; then send 10%,
   25%, 50%, and 100% of policy-eligible low-risk work through Machinist. Keep Buzz
   available throughout the acceptance window.
6. **Promote only on measured gates.** Require the acceptance gates in
   `adaptive-agent-platform.md`, including quality-adjusted token/rework results,
   cancellation/restart success, zero stale wins or secret disclosures, and
   platform conformance.
7. **Switch the default execution path.** Stop creating new Buzz execution runs,
   but retain the repository knowledge and read-only history. Continue using ASF
   contracts until equivalent policy is intentionally implemented elsewhere.
8. **Retire deliberately.** After two stable weeks, export final mappings, remove
   unused tickers/agent seats, revoke obsolete credentials, and document rollback.

## Rollback

Stop new admission, cancel or drain active attempts, preserve the SQLite database
and run artifacts, restore the prior binary/configuration, and route new work back
through Buzz/ASF. Do not downgrade across a non-backward-readable schema without a
pre-deploy database copy. Worker secrets and legacy executors remain compatible
during the pilot specifically to keep this rollback inexpensive.
