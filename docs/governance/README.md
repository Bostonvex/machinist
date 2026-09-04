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

## Policy and protocol

To be ported in follow-on A1c PRs: `factory/policy/*` and `factory/protocol/*`
(governance, merge-tiers, AGENT-LOOP, CONTEXT-PACKS, ESCALATION,
FACTORY-RUN-HANDOFF, FACTORY-START, definition-of-done, labels, branch
protection, protected-files, pull-requests, security, testing, engineering).
