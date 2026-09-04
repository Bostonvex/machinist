# Factory start

> Ported from `Bostonvex/agent-software-factory/factory/protocol/FACTORY-START.md`.

Canonical procedural contract for starting a software-work run on a GitHub issue.
There is exactly one copy of this workflow: the event source validates the event
and then runs *this*, rather than carrying its own paraphrase of it.

Event-source-owned pieces, and only these: event filtering, actor authorization,
and the GitHub credentials the run executes with.

## Trigger contract

Normalized comment body must be **exactly**:

```text
/factory start
```

Trim leading and trailing whitespace only. Do not treat quoted, embedded,
code-fenced, pull-request-comment, unauthorized, or duplicate invocations as a
start.

## Preconditions — all required, refuse otherwise

1. Target is a **GitHub issue**, not a pull request.
2. Actor is authorized.
3. Issue has label `agent-ready`.
4. Exactly one risk label: `risk-low`, `risk-medium`, or `risk-high`.
5. Acceptance criteria are present and checkable.
6. Issue does **not** have `human-required`.
7. No active run, existing task branch, open linked pull request, or recent
   duplicate start (idempotency).

## Idempotency

Before implementing, search the issue for `FACTORY:RUN` markers, linked open pull
requests, and matching branches. If an active or recent run exists, **do not**
start a second implementation — report the existing run pointer instead.

On start, write or update a marker per
[factory-run-handoff.md](factory-run-handoff.md). Because
[`internal/factoryrun`](../../../internal/factoryrun) renders deterministically
and skips an unchanged republish, re-writing the marker on a retry is safe.

## Workflow

Each numbered role runs on the runtime, model, and environment bound to it in
Machinist configuration ([`internal/config`](../../../internal/config)). The
orchestrator role owns every GitHub write; analysis roles return recommendations
to it.

1. Run the **intake** role (read-only) → recommendations only.
2. Orchestrator applies labels and comments from intake.
3. If a plan is required or risk is `risk-medium` or higher, run the **planner**
   role (read-only). Medium needs human plan approval before implementing; high
   risk disables autonomous implementation.
4. When permitted, run the **implementer** role.
5. Smallest complete change; tests and docs as needed; complete `just check`.
6. Open or update a **draft** pull request with evidence; update the marker; move
   state to `agent-review`.

## Hard stops

- No pull request approval, merge, auto-merge, or dismissal of unresolved human
  findings **unless** a human gave explicit permission for that named action on
  that named pull request.
- No deployment **unless** a human gave explicit permission for the named target
  and procedure.
- No push to `main` (use a pull request merge); no ruleset changes; no CI bypass.
- No production credentials or production network in a default run.
- Protected paths only with explicit issue scope and a human review path.
- Do not claim complete without evidence.
- A start command, green checks, and review recommendations are **not** merge or
  deploy permission.

## Outputs

Task branch, draft pull request URL, validation evidence, updated `FACTORY:RUN`
marker, and the recommended next step — an independent review run, or a
squash-merge that is human-operated **or** performed by an agent after a human
explicitly names that pull request
([policy/branch-protection.md](../policy/branch-protection.md) is the authority
for the method).
