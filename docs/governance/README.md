# Machinist governance

Governance material consolidated from the Agent Software Factory (ASF)
`factory/` tree into Machinist. Faithful port, kept in these files so Machinist
owns software-work governance end to end (migration-plan Phase A).

## Provenance

Ported from `Bostonvex/agent-software-factory` (`factory/`), which is being
absorbed into Machinist per [docs/migration-plan.md](../migration-plan.md).
These documents are guidance/contracts; execution enforcement is implemented in
Machinist code (`internal/review`, `internal/gatekeeper`, `internal/telemetry`).

## Roles

Port of `factory/roles/*.md`:

- [orchestrator](roles/orchestrator.md)
- [intake](roles/intake.md)
- [planner](roles/planner.md)
- [implementer](roles/implementer.md)
- [ci-investigator](roles/ci-investigator.md)
- [reviewer](roles/reviewer.md)
- [gatekeeper](roles/gatekeeper.md)
- [escalation](roles/escalation.md)
- [post-merge-verifier](roles/post-merge-verifier.md)

## Policy

Port of `factory/policy/*.md`, adapted to Machinist's toolchain, branch, and
paths:

- [branch-protection](policy/branch-protection.md) — the ruleset shape for
  `main`, and why the required-approval count is deliberately zero
- [protected-files](policy/protected-files.md) — paths that need explicit issue
  scope and human review
- [pull-requests](policy/pull-requests.md) — draft-first, ask for merge, never
  self-merge, clean up the worktree
- [security](policy/security.md) — credentials, bypass, and what escalates
  instead of being refused
- [testing](policy/testing.md) — the definition of done is the complete check
- [engineering](policy/engineering.md) — scope discipline and restrictions

## Protocol

Port of `factory/protocol/*.md`:

- [agent-loop](protocol/agent-loop.md) — roles, state transitions, and
  pull-based claiming
- [factory-start](protocol/factory-start.md) — the one copy of the start
  procedure, its preconditions and hard stops
- [factory-run-handoff](protocol/factory-run-handoff.md) — the `FACTORY:RUN`
  marker schema, as `internal/factoryrun` implements it
- [governance](protocol/governance.md) — which controls the forge actually
  refuses, and how to verify by refusal rather than by reading
- [escalation](protocol/escalation.md) — the bounded list, and the contract owed
  to the human who holds it
- [merge-tiers](protocol/merge-tiers.md) — the two opt-in tiers, documented and
  not yet enforced
- [context-packs](protocol/context-packs.md) — bounded role prompts: hard limits
  inline, prose behind pointers
- [definition-of-done](protocol/definition-of-done.md)
- [labels](protocol/labels.md) — the run-state partition, and the parallel
  `machinist:` vocabulary in this repository

## Enforcement in code

- [`internal/review`](../../internal/review) implements the reviewer verdict
  contract from [roles/reviewer.md](roles/reviewer.md), and enforces that the
  author of a change is never its reviewer. It decides; it does not write to
  GitHub. It also carries the protected-path list from
  [policy/protected-files.md](policy/protected-files.md) as
  `review.DefaultProtectedPaths` — the two copies must be changed together.
  The control plane calls it: a reviewer run submits its output block against
  the run it judged, and the route reads both identities from the runs
  themselves and the changed paths from the pull request's own diff. An agent
  cannot review its own work by describing itself differently, and a review
  that is refused records no verdict at all.
- [`internal/factoryrun`](../../internal/factoryrun) renders and parses the
  `FACTORY:RUN` marker described in
  [protocol/factory-run-handoff.md](protocol/factory-run-handoff.md). The
  control plane publishes it: every GitHub-triggered run has its stage, and the
  verdict recorded against it, written to the issue it was requested on. What it
  cannot report is what it does not know — branch and checks are not part of a
  run's recorded state, so the published marker carries identity, stage, and
  what was decided.

## Not yet enforced

These documents are contracts the roles are written against. Where a rule is
still only prose, this file says so rather than implying enforcement that does
not exist:

- **Merge and deploy gating** is a promise agents keep, not something the forge
  refuses. `internal/gatekeeper` is
  [migration-plan Phase B](../migration-plan.md).
- **The merge tiers** in [protocol/merge-tiers.md](protocol/merge-tiers.md) are
  documented, not enforced, and enable nothing until a repository is named.
- **The two label vocabularies** in [protocol/labels.md](protocol/labels.md) are
  not yet reconciled.
- **Nobody is assigned to review.** The control plane decides and records a
  review when a reviewer run submits one, but it does not pair an implementer's
  work with a reviewer, and does not chase work that was never reviewed. An
  unreviewed run simply carries no verdict, which is what its marker says.
