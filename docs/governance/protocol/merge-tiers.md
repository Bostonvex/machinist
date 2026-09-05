# Merge tiers — optional policy

> Ported from `Bostonvex/agent-software-factory/factory/protocol/MERGE-TIERS.md`.

Human merge is the **default**, and nothing here changes that on its own. This
document describes two **opt-in** tiers a repository owner may adopt so that an
agent holding a dedicated merge seat (the `gatekeeper` role) may merge without
the owner naming each pull request.

**These tiers are documented, not enforced.** Every condition below must be
verified at merge time against the exact commit that will land. A tier that
appears to be enforced but is not is worse than one that is only documented.
Native enforcement arrives with `internal/gatekeeper` in
[migration-plan Phase B](../../migration-plan.md); until then, treat every
condition as a manual check.

**Deploy is never excepted.** Every deploy still needs the owner to name the
target and the procedure, regardless of tier.

## Green tier — documentation-only merges

A pull request may merge without the owner naming it only if **all** of the
following hold. Verify each, per pull request, against the commit that will
merge.

1. **Paths are words only.** Every changed path ends in `.md`, is `README.md` or
   under `docs/`, and is mode `100644` in the merge commit's tree. Symlinks
   (`120000`) and executables (`100755`) are not Green whatever they are named.
   Read file modes from the git tree — the pull request files API does not expose
   them.
2. **No governance or live agent-instruction sources.** No path is under
   `docs/governance/**` or is `AGENTS.md`. Those are what the roles execute
   from: a change to them is a change to the contract, and it is on the
   escalation list.
3. **No workflows and no deploy target.** No path under `.github/`, and the
   change cannot reach a release or deploy path.
4. **Independent review.** A reviewer that did not write the change returned
   `ready-for-human-review` with no open finding —
   [`internal/review`](../../../internal/review) refuses a review whose author
   and reviewer are the same agent or run.
5. **Closing issues match intent.** `closingIssuesReferences` matches what the
   pull request intends to close, re-read immediately before merging.

After a Green merge, record on the pull request: the five conditions, the
commands run, and the reviewer's verdict quoted. Condition 4 cannot be
reconstructed from the forge alone.

### Not Green — always needs owner authorization

- `AGENTS.md`, `docs/governance/**`, role and protocol sources, and any runbook
  agents execute from.
- Every script, whatever directory it sits in.
- Workflow and agent-configuration files.
- Protected paths per
  [policy/protected-files.md](../policy/protected-files.md).

Green is defined by **what a change cannot reach**, not by how small the diff
looks.

## Reviewed tier — low-risk code merges

A second tier for repositories that run CI and need code changes to merge without
the owner naming each pull request. **This tier applies only in repositories the
owner explicitly lists.**

```text
Repositories: <none — this tier permits nothing until a repository is named>
```

Adding a repository is an owner decision. Until a repository is named, this tier
permits nothing anywhere.

A pull request may merge under Reviewed only if **all six** hold — condition 6 is
a gate, not a note. Verify each against the commit that will merge.

1. **Independent review.** A reviewer that did not write the change returned
   **`ready-for-human-review` with no findings**, naming that exact head.
2. **The required checks are green on that head**, and `mergeStateStatus` is
   `CLEAN`. If the repository runs no required check, this tier is
   **unavailable** — absence is not green. If the checks exist but are not
   required and strict on the default branch, this tier is likewise unavailable.
3. **Risk is low or medium.** Read `risk-low` or `risk-medium` from **every**
   closing issue, never from the pull request alone. Fail closed if any closing
   issue has no risk label or carries `risk-high`.
4. **No protected path.** Use a repository-independent floor plus the
   per-repository list:

   ```text
   FLOOR   .github/**  docs/governance/**  AGENTS.md  scripts/**  infra/**
   PLUS    every path in policy/protected-files.md, and every pattern in
           review.DefaultProtectedPaths
   ```

   A missing protected-path list is never read as "no protected paths".
5. **No closing issue carries `human-required`.** Re-read
   `closingIssuesReferences` immediately before merging.
6. **No agent-configuration change.** The diff must not change files that
   configure or instruct agents — governance documents, role contracts, agent
   prompts, or the configuration that binds a role to a runtime.

**Deploy is unchanged.** A merge under Reviewed authorizes nothing beyond the
merge.

### Conditions 7 and 8 — mechanics, not gates

When strict checks force a branch update before merge
(`mergeStateStatus: BEHIND`):

7. **Re-pass review on the new head.** A verdict-only pass against the delta is
   acceptable if it names the new head.
8. **Record the merge** on the pull request with the conditions verified, for the
   same reason Green records condition 4.

## Enabling a tier

1. Amend [`AGENTS.md`](../../../AGENTS.md) to admit the tier.
2. Name the allowed repositories in the list above (Reviewed tier only).
3. Configure forge settings so that an agent merge is actually refused when the
   tier is not met — see [governance.md](governance.md).
4. Bind the `gatekeeper` role in Machinist configuration.

Automatic merge stays off until it is deliberately turned on. Documenting a tier
does not enable it.
