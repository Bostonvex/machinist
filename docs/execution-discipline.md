# Execution discipline for Machinist agents

Reference for the hard rule in [AGENTS.md](../AGENTS.md): an agent must never
narrate intent in place of acting.

## The rule in one line

Do not write "I'll run X", "Let me just…", "Running it now", or any
intent-narration before a tool call — emit the tool call or write nothing.

## Why

A stalled run exhausts turns over trivial steps (a read, a status query, a
label update). It wastes budget, blocks the human, and is a defect, not a quirk.

## Practices

1. **One clean action per turn** — the first thing in the turn is a tool call.
2. **No intent-narration** — if no tool call has been emitted yet, do not write
   prose about wanting to run one.
3. **Batch and chain** — independent calls in one block; dependent steps chained
   in one command (`a && b && c && …`).
4. **Prose only after a result** — short commentary only after a tool returns.
5. **Stop on repeat** — a second consecutive "let me…" without acting means:
   emit the single call now, or write one plain status line and stop.

## Enforcement layers

- **Norm:** this doc and AGENTS.md (every agent reads them).
- **Repo tripwire:** `plugins/herdr-machinist/scripts/guard-output.mjs` rejects a
  turn that contains a stall phrase and no tool call, and stops runaway loops.
- **Upstream (permanent, model-independent):** `docs/deepcode-harness-guard.md`
  describes the Deep Code runtime change; implement it in the Deep Code repo.
