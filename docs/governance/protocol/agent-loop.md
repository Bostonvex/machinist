# Agent loop protocol

> Ported from `Bostonvex/agent-software-factory/factory/protocol/AGENT-LOOP-PROTOCOL.md`.

Event-driven workflow. GitHub is the durable system of record for product work;
the Machinist database is the execution ledger
([migration-plan §3](../../migration-plan.md)).

## Roles

| Role | Owns |
|------|------|
| Orchestrator | Orchestration, GitHub writes, implement when permitted |
| Intake / planner / reviewer / post-merge verifier | Recommendations only (read-only) |
| Implementer / CI investigator | Task-branch code and draft pull request updates |
| Human | Squash-merge and deploy, or explicit permission for an agent to execute either; rulesets, secrets, high-risk auth |

Full contracts in [`../roles/`](../roles/).

## Labels

See [labels.md](labels.md), including the note on this repository's parallel
`machinist:` vocabulary.

## Transitions

```text
(no state) --human--> agent-ready
agent-ready --start accepted--> agent-running
agent-running --draft PR + evidence--> agent-review
agent-review --squash-merge, human-operated OR by an agent after a human explicitly names that PR--> done
* --escalation / repeated fail--> agent-blocked | human-required
```

`squash` is the only merge method the ruleset permits, and it permits it to
nobody in particular:
[policy/branch-protection.md](../policy/branch-protection.md) is the authority
for **how** a merge happens, for a human and an agent alike. This diagram and the
Human row above say only **who** may perform one. Naming the method here rather
than pointing at it is what let the two drift in ASF.

## Trigger

An issue comment whose normalized body is exactly `/factory start`. The event
source validates the event and then runs
[factory-start.md](factory-start.md) — it does not carry its own paraphrase of
that procedure.

## Marker

Canonical schema: [factory-run-handoff.md](factory-run-handoff.md), implemented
in [`internal/factoryrun`](../../../internal/factoryrun). The orchestrator writes
the marker on the issue; other roles return the payloads documented there and in
their role files.

## Work is pulled, not pushed

Agents take work off the board themselves. Nobody is dispatched an `agent-ready`
issue.

**`agent-ready` means:** assessed, risk-labelled, and nobody needs to do anything
before an agent starts. Issues that are not ready get `human-required`, or a risk
label with no state label.

### Claiming is a comment, not an assignee

Claiming is **`agent-running` plus a comment naming the agent** — not an assignee
field.

```sh
gh issue edit <n> --repo <owner>/<repo> --add-label agent-running --remove-label agent-ready
gh issue comment <n> --repo <owner>/<repo> --body "CLAIMED: <agent>, acting as <role>."
```

**The state labels are a partition — exactly one at a time.**
`--add-label agent-running` on its own leaves `agent-ready` on a claimed issue
and the board advertises it as free. Every move removes the previous state label.

**Read a claim from that comment. Never from the assignee.** Every agent may
share one machine account, so an assignee reads the same login on every claimed
issue — it says *some* agent claimed the work and cannot say *which*. Under
pull-based claiming, the comment is the whole mechanism.

An `agent-running` label **with no claim comment is treated as claimed, not as
free.** Ask whose it is.

Read claim state adjacent to the claim write — a gap between read and write is
the window a second session claims in.

### Rule A — closeout refill

When a seat frees on merge, or a run parks with `human-required` while other work
waits, the orchestrator **pulls the next `agent-ready` issue** rather than
waiting to be dispatched.

After any closeout in the same wake:

1. Scan for fillable work: `agent-ready` issues, wrongly `agent-blocked` issues
   whose dependencies landed, quiet drafts needing a chase.
2. Fill idle seats up to configured parallelism — one claim per seat.
3. Post what started, or why nothing could start.

**"No open escalation ask" is never a stop condition.** Parked `human-required`
items stay parked; everything else keeps moving. This is the same rule the
[execution-discipline](../../execution-discipline.md) guard enforces in code.
