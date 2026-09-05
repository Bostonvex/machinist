# Machinist consolidation migration plan

This is the authoritative plan for absorbing the capabilities of three
deprecated repositories into Machinist and then sunsetting them.

## Status

| Phase | State | Issue |
| --- | --- | --- |
| A — ASF roles/policy/guard/review | delivered | #4 |
| B — Gatekeeper merge/deploy native | delivered | #5 |
| C — telemetry collector into Machinist | delivered | #6 |
| D — durable-knowledge / kanban / ops patterns | delivered | #7 |
| E — cutover: shadow, canary, default | delivered | #8 |
| F — sunset | in progress | #9 |

Every phase heading in §4 repeats its state, and
`TestThePlanAgreesWithItselfAboutWhatHasShipped` fails when the two disagree.
The table and the headings are edited by different changes at different times,
and that drift is not hypothetical: this document told every reader for days
that nothing had been executed, while five of its six phases were closed.

**Delivered is not sunset.** Phase F's exit criterion is unmet, so all three
source repositories still exist and one of the three is still orchestrating
live work. Nothing here has been switched off except where this document says
it has.

Source repositories (revision-reviewed in `docs/buzz-asf-comparison.md`):

- `Bostonvex/agent-software-factory` (ASF)
- `Bostonvex/buzz-workspace`
- `Bostonvex/buzz-agent-observability` — **retired 2026-09-05.** Stopped,
  disabled, archived, and documented in [sunset-rollback.md](sunset-rollback.md).

Not a source repository, and in the blast radius anyway:

- `Bostonvex/auspicia-nextgen` is a **consumer** of ASF, not a thing being
  absorbed by it. None of its code moves into Machinist; what moves is the
  orchestration underneath it. It matters to this plan because Phase F stops
  ASF's ticker, kanban and webhook receiver, and those dispatch its work — 34
  open cards as of 2026-09-05. Its own
  [#919](https://github.com/Bostonvex/auspicia-nextgen/issues/919) makes
  Machinist its control plane of record and leaves the machine-local
  registration to be "handled outside this issue"; #84 is that outside.

  Listing it here is the correction for a real failure: the scope section named
  three repositories, the sunset step would have stopped a fourth, and nothing
  in the document connected the two. That gap was found from the live board, not
  from the plan.

## 1. Decision

Fully consolidate the three source repos' capabilities into Machinist, then
sunset all three. This extends the earlier staged decision in
[`adaptive-agent-platform.md`](adaptive-agent-platform.md) (which kept ASF/Buzz
layers on top of Machinist) to full absorption. Execution is staged and
measured, not a big-bang replacement: release can be rolled back at each phase
until the final Sunset gate.

- Machinist is the control plane, durable run ledger, operator UI, and the
  **Gatekeeper** for merge/deploy.
- GitHub remains the product-work ledger (issues/PRs); Machinist SQLite is the
  execution ledger. No second product planning truth inside Machinist.
- High-volume telemetry samples stay out of the transactional SQLite database.

## 2. Capability matrix

Legend for **Disposition**: **Migrate** (port into Machinist codebase),
**Adapt** (reimplement against Machinist primitives), **Skip** (deliberately
not carried; collaboration/scheduling infra or redundant).

### 2.1 Agent Software Factory (ASF)

| Capability | Source | Target Machinist path | Disposition |
| --- | --- | --- | --- |
| Roles (orchestrator, intake, planner, implementer, ci-investigator, reviewer, gatekeeper, escalation, post-merge-verifier) | `factory/roles/*.md` | `docs/governance/roles/*.md` + role config | Migrate (doc) |
| Role-to-runtime/model binding | `OWN-REPO.md`, factory config | `internal/config` profile/route/role | Migrate |
| Policy (branch-protection, protected-files, pull-requests, security, testing, engineering) | `factory/policy/*.md` | `docs/governance/policy/*.md` | Migrate (doc + enforcement hooks) |
| Guards (audit.py, decide.py) | `factory/guards/*.py` | `internal/policy` / `internal/review` | Migrate |
| Protocol (AGENT-LOOP, CONTEXT-PACKS, ESCALATION, FACTORY-RUN-HANDOFF, FACTORY-START, GOVERNANCE, LABELS, MERGE-TIERS, DOD) | `factory/protocol/*.md` | `docs/governance/protocol/*.md` | Migrate |
| Independent review / reviewer separation | `factory/roles/reviewer.md`, guards | `internal/review` engine | Adapt |
| `FACTORY:RUN` cross-session handoff marker | `factory/protocol/FACTORY-RUN-HANDOFF.md` | GitHub commit/PR evidence writer in Machinist | Adapt |

### 2.2 Buzz Workspace

| Capability | Source | Target Machinist path | Disposition |
| --- | --- | --- | --- |
| Central factory rules | `AGENTS.md`, `HOUSE_RULES.md` | `docs/governance/` (single source) | Migrate |
| Gatekeeper merge/deploy (named-target / Automation / Deploy tier) | `HOUSE_RULES.md §4`, `OUTBOX/FACTORY_AGENT_INSTRUCTIONS/05_GATEKEEPER.md` | `internal/gatekeeper` (native merge/deploy gate) | Migrate |
| Decisions (named-target authorize) | `decisions-state.sh`, Decisions convention | `internal/gatekeeper` decisions intake | Migrate |
| Kanban/ticker ops | `factory-kanban.*`, `factory-ticker.*`, `rsi-ticker.*` | Machinist UI (Work board) / `internal/ops` | Adapt |
| Fleet lease / claim | `fleet-lease.sh`, `factory-claim.sh` | Machinist worker lease (existing) | Adapt (redundant) |
| Merge owed / pr-drain | `factory-merge-owed.sh`, `pr-drain.sh` | `internal/gatekeeper` | Migrate |
| Durable knowledge (GUIDES, PLANS, RESEARCH, WORK_LOGS) | workspace dirs | `docs/`, `notes/` repo artifacts | Adapt |
| Channel / `@mention` dispatch | Buzz collaborators | — | **Skip** (collaboration infra, not execution protocol) |

### 2.3 Buzz Agent Observability

| Capability | Source | Target Machinist path | Disposition |
| --- | --- | --- | --- |
| Collector HTTP server + storage + schema + auth + CLI | `collector/{server,storage,schema,auth,cli}.py` | `internal/telemetry` | Migrate |
| Providers (vLLM, NVIDIA/DGX, generic command, JSON command) | `collector/providers/*.py` | `internal/telemetry/providers` | Migrate |
| OpenAI proxy | `proxy/openai_proxy.py` | `internal/telemetry/proxy` | Migrate |
| Dashboard | `dashboard/{app.js,index.html,styles.css}` | `internal/controlplane/web` Agents & infra | Adapt |
| Event schema v1 | `config/event-schema-v1.json` | `internal/telemetry/schema` | Migrate |
| Agent/turn/tool/stall telemetry + DGX/GPU/KV metrics | collector store | `internal/telemetry` + `internal/controlplane/web` | Migrate |

## 3. Repository of record

- **Execution ledger:** Machinist SQLite (jobs/runs/attempts) — ownership moves
  here.
- **Product-work ledger:** GitHub issues/PRs — unchanged.
- **Telemetry:** `internal/telemetry`, fail-open, metadata-only; high-volume
  agent/GPU samples never absorbed into transactional SQLite.

## 4. Phased task list

### Phase A — ASF roles/policy/guard/review into Machinist _(delivered)_
- Port role definitions, policy, protocol docs into `docs/governance/`.
- Add `internal/review` independent-review engine (+ `FACTORY:RUN` handoff
  writer that emits GitHub PR/commit evidence).
- Enforce author≠reviewer separation via Machinist routes/policy.

Exit: a role-gated software-work run (plan→implement→independent review) runs
end-to-end in Machinist and produces a GitHub draft PR with handoff evidence.

### Phase B — Gatekeeper merge/deploy native _(delivered)_
- Add `internal/gatekeeper`: named-target merge/deploy, Automation tier,
  Deploy tier; reads Decisions/go-ahead.
- Add per-repo wiring: `main` ruleset granting Gatekeeper merge access;
  required status checks for the Gatekeeper gate.
- Deploy via release artifact tagging (Gatekeeper action), never Foreman.

Exit: given a named go-ahead on a ready PR, Gatekeeper merges and (if
authorized) tags/deploys; merge/deploy is never performed by the Foreman.

### Phase C — telemetry collector into Machinist _(delivered)_
- Port collector/provider/proxy/schema into `internal/telemetry`.
- Reuse the existing read-only Agents & infra bridge; fold collector ops into
  Machinist release lifecycle.
- Keep fail-open and metadata-only guarantees; keep high-volume samples out of
  transactional SQLite.

Exit: `buzz-agent-observability` capability served by Machinist with existing
UI parity and fail-open behavior.

### Phase D — durable-knowledge / kanban / ops patterns _(delivered)_
- Port fundamental durable-knowledge patterns (plans/research/work-logs) as
  `docs/`/`notes/` repository artifacts.
- Consolidate kanban/ticker/fleet ops into Machinist work board + leases.
- Skip Buzz channel/`@mention` dispatch (collaboration infra).

Exit: operators have Machinist-native work board/leases; durable knowledge is
repository artifacts.

### Phase E — cutover: shadow, canary, default _(delivered)_
- Shadow representative ASF/Buzz/telemetry workloads; then canary 10/25/50/100%.
- Keep ASF/Buzz/collector as operational fallback during acceptance window.
- Freeze new work in the source repos after a stable window.

Exit: two consecutive weeks meet the Acceptance gates with zero critical
safety or data-loss incidents.

### Phase F — sunset _(in progress)_
- Export final mappings/archive snapshots; remove unused tickers/agents/seats.
- Revoke obsolete credentials and bot permissions.
- Archive or make read-only all three source repos; remove references from
  machinist docs/CI.

Exit: all source repos drained, archived/read-only, and no live dependency on
them; rollback documented.

Where it actually stands, per source repository:

| Repository | Drained | Retired | Archived |
| --- | --- | --- | --- |
| `buzz-agent-observability` | yes, no producer since 2026-09-02 | yes | yes |
| `agent-software-factory` | 0 open cards in the repo itself | **no** — its ticker, kanban and webhook receiver are running | no |
| `buzz-workspace` | **no** — 40 open cards | n/a | no |

ASF's repository being empty is not ASF being drained. What it runs is what
matters, and what it runs serves `buzz-workspace` and `auspicia-nextgen`.
Stopping it is tracked in #84, which also holds the machine-local registration
that has to land first. References were removed from the docs in #83.

## 5. Acceptance gates (measured)

Carried forward from `adaptive-agent-platform.md`, plus consolidation-specific
gates:

1. Median active cycle time ≥30% lower than the ASF/Buzz baseline.
2. Median reported tokens per accepted change ≥40% lower.
3. Median repair/rework attempts per accepted change ≥30% fewer.
4. ≥90% of policy-eligible low-risk work reaches handoff unattended.
5. Zero incompatible dispatches, secret disclosures, stale-completion wins, or
   unbounded duplicate attempts.
6. 100% cancellation/restart recovery in the fault-injection suite.
7. ≥95% token-reporting coverage for harnesses claiming structured usage.
8. Telemetry p95 CPU overhead <2%; bounded memory/disk queues.
9. macOS/Linux/WSL conformance green before pilot; native Windows before
   production use.
10. **Consolidation:** 100% of the migrated capabilities (matrix above) are
    available in Machinist with no functional regression.
11. **Sunset:** all three source repos — `agent-software-factory`,
    `buzz-workspace`, `buzz-agent-observability` — drained and
    archived/read-only; no live dependency remains; documented rollback exists.
    `auspicia-nextgen` is not counted by this gate, because it is not being
    absorbed; what the gate does require is that stopping ASF leaves its work
    dispatchable, which is why it is named in §1.

## 6. Deployment and rollback

- Deploy only from a clean reviewed commit, per the existing
  `adaptive-agent-platform.md § Deployment and rollback` steps (frontend
  build, formatting/vet/race tests, schema upgrade/restore tests, dependency/
  secret scan, versioned artifacts, canary, health checks, controlled expand).
- Rollback: stop new admissions, drain/cancel active attempts, preserve the
  SQLite database and artifacts, restore prior binary/config, route work back,
  and restore pre-deploy DB on non-backward-readable schema changes. Source
  repos remain available as fallback until the Sunset gate closes.

## 7. Ownership note

- **Foreman** plans/builds/reviews and opens PRs **as drafts**. It stops there:
  its own review is not independent, because it wrote the change. The control
  plane takes a change out of draft when an independent reviewer's verdict is
  recorded as `ready-for-human-review`
  ([draft-until-reviewed.md](draft-until-reviewed.md)). The Foreman does **not**
  promote, merge, or deploy.
- **Gatekeeper** (now native in Machinist, Phase B) performs merge/deploy on a
  named go-ahead or under the Automation/Deploy tier.
- Machinist becomes the single owner of the consolidated capability set;
  the three source repos are then sunset.

## References

- `docs/adaptive-agent-platform.md` (release candidate core + existing phases)
- `docs/buzz-asf-comparison.md` (capability assessment + cutover plan)
