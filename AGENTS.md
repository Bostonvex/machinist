# Machinist agent rules

These rules bind every agent (and orchestration layer) operating under Machinist,
in every repository Machinist oversees. A full breakdown is in
[docs/execution-discipline.md](docs/execution-discipline.md).

## Execution discipline (hard rule)

Do not narrate intent in place of acting.

1. **One clean action per turn.** Emit a tool call as the first thing in the
   turn. Do not open with "Let me run X", "I'll just run X", "Running it now",
   or any intent-narration.
2. **No intent-narration.** If you have not yet emitted a tool call, do not
   write prose about *wanting* to run one. Emit it, or write nothing and stop.
3. **Batch and chain.** Independent calls go in one block. Dependent steps go in
   one command (`a && b && c && …`). Do not split one logical step across turns
   with narration in between.
4. **Prose only after a result.** Put commentary only AFTER a tool returns, and
   keep it short.
5. **Stop on repeat.** If you catch yourself writing a second consecutive
   "let me…" / "I'll just…" without acting, stop immediately and emit the single
   tool call, or write one plain status line and stop.

Rationale: a stall that exhausts turns over trivial steps is a defect. The
discipline above is cheap to follow and prevents it. A repo-side tripwire is at
[plugins/herdr-machinist/scripts/guard-output.mjs](plugins/herdr-machinist/scripts/guard-output.mjs);
the upstream Deep Code harness change is specified in
[docs/deepcode-harness-guard.md](docs/deepcode-harness-guard.md).

## Governance

- Merge and deploy are never performed by a foreman; they are done by a human or
  the Gatekeeper seat.
- Reuse the open issue/branch/worktree for a task; never open a second PR for
  the same issue.
- Keep exactly one `<!-- machinist:foreman-state -->` comment per issue and the
  correct stage label.

## Durable knowledge

- What a session works out belongs in `notes/`, not only in the session:
  a `plan` before the work, `research` while a question is open, a `work-log`
  after something changed. Write them with `machinist notes new`.
- Do not edit a plan into agreement with what happened. Supersede it and write
  the work log; the gap between the two is usually the useful part.
- `machinist notes check` must pass. See
  [docs/durable-knowledge.md](docs/durable-knowledge.md).
