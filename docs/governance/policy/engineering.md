# Engineering

> Ported from `Bostonvex/agent-software-factory/factory/policy/engineering.md`.
> Adapted to Machinist's toolchain and repository layout.

Read [`AGENTS.md`](../../../AGENTS.md) and [`CONTRIBUTING.md`](../../../CONTRIBUTING.md)
before large changes. Protected paths: [protected-files.md](protected-files.md).

## Scope discipline

- Implement only the assigned issue or task.
- No unrelated refactors and no drive-by dependency upgrades.
- Follow this repository's toolchain — `just build`, `just check`, the pinned Go
  and Node versions in [`CONTRIBUTING.md`](../../../CONTRIBUTING.md). Do not
  invent a toolchain.
- Update [`ARCHITECTURE.md`](../../../ARCHITECTURE.md) when a boundary or
  contract changes.

## Restrictions

- No direct pushes to `main`.
- No merging pull requests.
- No production credentials in the tree.
- No bypassing CI.
- Escalate auth, secrets, destructive migrations, production data, and repeated
  validation failures per [protocol/escalation.md](../protocol/escalation.md).
