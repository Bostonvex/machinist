# Context packs — bounded role prompts

> Ported from `Bostonvex/agent-software-factory/factory/protocol/CONTEXT-PACKS.md`.
> **Adapted in substance, not mechanism.** ASF *generated* context packs with
> `.factory/render.sh` into `docs/factory/context-packs/`, and layered a
> `buzz-workspace` nest overlay on top. Machinist has no renderer and no nest.
> The shape a role prompt must have is portable and is kept here; the generator
> is deliberately not ported. Composing a role's context from these documents is
> Machinist's own job ([`internal/config`](../../../internal/config) binds a role
> to a runtime and model).

## The shape

One bounded pack per role. Two kinds of content, and the split is the whole
point:

- **Hard limits inline.** Permissions, merge and deploy gates, repair caps, and
  every other load-bearing rule that must not be dropped when a prompt is
  trimmed.
- **Pointers everywhere else.** Workflow steps, output schemas, checklists, and
  full prose live in [`../roles/`](../roles/) and the linked governance
  documents, and are read on demand.

| Section | Content |
|---------|---------|
| Access | Source access, plus GitHub writes for roles that have them |
| Hard limits | The role's `## Hard limits` section, inline and complete |
| Pointers | Links to the full role contract, `AGENTS.md`, the loop protocol, and role-specific protocol |

## The budget

Target: a composed role prompt stays under **~8 KB** without losing a hard limit.

That number is not arbitrary. A prompt that grows past it gets trimmed by
whoever is composing it, and what gets trimmed is prose — until the trimming
reaches a hard limit and silently removes a control. Keeping limits inline and
prose behind pointers means the budget is met by *not including* rather than by
*cutting*.

**If a seat prompt exceeds the budget, move prose to pointers. Never drop a hard
limit to fit.**

## Do not paste shared rules into every seat

The ASF/Buzz failure this document exists to prevent: house rules and token
habits pasted verbatim into every agent file, so that changing one rule means
editing every seat, and every turn pays for text that has not changed.

Compose a live seat prompt from layers instead:

1. The role's bounded pack — hard limits inline.
2. One shared include, referenced by path and read at session start, for habits
   that apply to every seat.
3. A short seat-specific addendum — a few hundred bytes — for what is genuinely
   particular to that seat. The full rule text stays on disk.

In this repository, layer 2 is [`AGENTS.md`](../../../AGENTS.md) and the
[execution-discipline](../../execution-discipline.md) rule. Reference them; do
not inline them.

## Not ported

- The renderer (`.factory/render.sh`), the generated
  `docs/factory/context-packs/` tree, and `bash .factory/render.sh --check`.
- The `buzz-workspace` nest overlay: `HOUSE_RULES.md`, host-qualified `@mention`
  names, Decisions merge gates, and canvas status. The Decisions merge gate
  arrives natively as `internal/gatekeeper` in
  [migration-plan Phase B](../../migration-plan.md); channel and `@mention`
  dispatch is explicitly **skipped** as collaboration infrastructure rather than
  execution protocol.
