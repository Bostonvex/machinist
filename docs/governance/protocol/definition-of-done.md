# Definition of done

> Ported from `Bostonvex/agent-software-factory/factory/protocol/definition-of-done.md`.

- [ ] Scope matches the issue only
- [ ] Feature branch (not `main`)
- [ ] Draft pull request; merge only with human action or explicit per-PR permission
- [ ] PR body includes `Closes #<issue>` (or `Fixes` / `Resolves`) so GitHub links
      the issue, **or** an explicit **no closing issue** line when that is
      intentional — required before undraft or a merge ask
- [ ] No secrets in the diff
- [ ] Complete `just check` (or docs-only N/A explained)
- [ ] `ARCHITECTURE.md` updated if a boundary or contract changed
- [ ] Tests added or updated, or N/A documented
- [ ] Tests not weakened to force green
- [ ] Required CI checks green on the head that will merge
- [ ] Protected-path policy followed ([policy/protected-files.md](../policy/protected-files.md))
- [ ] No agent approve, merge, or deploy without explicit human permission for
      that named action

The orchestrator and gatekeeper verify that GraphQL `closingIssuesReferences` is
non-empty for the intended issue (or that the pull request carries an explicit
no-close line) before undraft and before a merge ask.
