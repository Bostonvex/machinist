# Branch protection

> Ported from `Bostonvex/agent-software-factory/factory/policy/branch-protection.md`.
> Adapted: ASF applied this with `scripts/apply-branch-protection.sh` and a
> checked-in ruleset payload. Machinist does not yet ship that script — the
> shape below is applied by hand until Phase B's `internal/gatekeeper` owns the
> per-repository wiring ([migration-plan Phase B](../../migration-plan.md)).

The default for `main` in every repository Machinist works in.

```
deletion                           blocked
non_fast_forward                   blocked        (no force-push)
required_linear_history            on
allowed_merge_methods              squash only
required_review_thread_resolution  on
required_approving_review_count    0              <- deliberate, see below
required_status_checks             Linux checks, macOS checks, Windows checks
                                   strict, pinned to GitHub Actions
bypass_actors                      RepositoryRole: Admin, mode: always
```

## Why required approvals is zero

Because the merge gate is not in GitHub.

A required review only produces two outcomes when the humans and the agents
share a machine: the operator opens a browser for every pull request, or the
agent reaches for the operator's admin credential to get past it. The first is
the friction this system exists to remove. The second is worse than no rule at
all, because it makes credential-borrowing routine and leaves a control that
reads as enforcement while enforcing nothing.

So the approval requirement is removed, and the gate moves to the operator's
channel: an agent brings a finished pull request, states what it does and what
would deploy, and asks. [pull-requests.md](pull-requests.md) is the operative
rule.

**Be honest about what that is.** It is a promise the agent keeps, not a
permission the forge grants. Write it down that way; a control described as
stronger than it is will be trusted more than it deserves.

## What is still real

`required_status_checks` is pinned to GitHub Actions (`integration_id: 15368`).
An agent token with `repo` scope *can* post commit statuses, but they carry a
different origin and never satisfy a pinned requirement. **CI is the one control
on the repository that an agent cannot talk its way past**, which is why the
required checks must be the full [`ci.yml`](../../../.github/workflows/ci.yml)
matrix rather than a reduced-scope subset, and why the job names in
[testing.md](testing.md) are load-bearing: renaming a job unpins the requirement
without any error.

`strict` is on, so a pull request must be up to date with `main` before it
merges. That costs a rebase on busy branches and buys the guarantee that the
tested tree is the merged tree.

## Access, not just rules

Rules bind whoever the agent is. Two things make them mean something:

- **Agents get `write`, never `admin`.** Admin can read and rewrite the ruleset;
  write cannot. Verify with a refusal, not a settings page: an agent attempting
  `PUT /repos/{owner}/{repo}/rulesets/{id}` should get `404`.
- **`bypass_actors` names the Admin role, so the operator keeps an override.**
  Where the operator's admin credential lives on the same machine as the agents,
  that bypass is reachable by the agents too. That is a property of the machine,
  not of the ruleset, and no repository setting fixes it. Record it rather than
  papering over it.

## Verify by refusal

Never report protection as applied on the strength of reading the setting back.
Reading proves the field is stored; it does not prove the forge acts on it.

```sh
gh api repos/Bostonvex/machinist/rulesets/ID --jq .current_user_can_bypass  # agent -> never
gh api -X PUT repos/Bostonvex/machinist/rulesets/ID -f name=x               # agent -> 404
gh pr merge N --admin                                                       # agent -> rule violations
```

A ruleset is not visible through `GET /branches/{branch}/protection` — that
endpoint returns `404 Branch not protected` when protection lives in a ruleset.
The 404 means "not configured *that way*", not "not configured". Check
`GET /repos/{owner}/{repo}/rulesets` before concluding a branch is unprotected.
