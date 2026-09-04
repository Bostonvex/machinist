# Governance

> Ported from `Bostonvex/agent-software-factory/factory/protocol/GOVERNANCE.md`.

Everything else here is an instruction an agent is asked to follow. This file
describes the part the forge refuses on its own, and what has to be true for it
to refuse.

The distinction matters more than it sounds. A policy file says "never merge
without permission". A repository setting says "merge is impossible until a
person approves". The first survives only as long as every agent behaves; the
second holds when one does not.

## 1. The controls that actually bind

| Control | What it refuses | Where it lives |
|---|---|---|
| **Required status checks** (`Linux checks`, `macOS checks`, `Windows checks`) | Merge, until CI is green on the head commit | Ruleset |
| **Admin enforcement** | The above, for admins too — otherwise it applies to everyone except the account agents use | `enforce_admins`, or the ruleset's bypass list |
| *Required approving review* | Merge, until a human approves *that* pull request | **Off by default — see below** |

The second is the one that is easy to leave off and easy to believe you have.
Protection with admin enforcement disabled is protection that binds every account
except the privileged one — and the privileged one is usually the account the
agents inherited.

**Why the required review is off by default.** It only binds when the approver is
a different account from the author *and* the approval can be given from
somewhere the agents cannot reach. Where one person runs the agents on their own
workstation, neither holds: the operator's credential is on the same disk, so a
required review produces either a browser trip for every pull request or an agent
borrowing that credential to get past it. The second is worse than no rule,
because it makes credential-borrowing routine while leaving a control that reads
as enforcement and enforces nothing.

So the default puts the approval in the operator's channel and says so plainly —
a promise the agent keeps, not a permission the forge grants.
[policy/branch-protection.md](../policy/branch-protection.md) carries the shape
and the reasoning. **Turn the required review back on when the approval can come
from an account or a device the agents do not control**, which in practice means
a second person, or an approval issued from a phone. At that point it becomes the
strongest control here.

Two settings worth understanding rather than copying:

- **Dismiss stale reviews on push**, plus **require approval from someone other
  than the last pusher.** Without these, an approval granted on one diff silently
  authorizes a later one. An approval names a diff, not a branch. Both are moot
  while approvals are zero — and `require_last_push_approval` should be *off*
  there, because it can still demand an approval after a push and appear as an
  unexplained block.
- **Strict status checks** require the branch to be up to date with `main` before
  merge. This is a real workflow cost — every pull request needs an update when
  another lands first — and it is the price of the check having tested the code
  that will actually be on the branch.

## 2. Identity: why one account cannot hold both seats

**A required review is only a control when the author and the approver are
different accounts.** GitHub refuses to let anyone approve their own pull
request, and that refusal is the entire mechanism. It does nothing if the agents
and the human are the same login.

So the arrangement assumed here:

- **Agents act as a machine account with `write`, never `admin`.** Write is
  enough to push a branch, open a pull request, and comment. It is not enough to
  edit protection, and that gap is the point.
- **The human keeps their own account** and approves as themselves.
- The machine account's token is scoped to the agent processes — a per-agent
  environment, not a shell profile — so the human's own credential stays active
  and untouched for their own work.

**The consequence for roles, which is easy to miss:** every agent role shares
that one machine account. So the `reviewer` role **cannot approve a pull
request** — to the forge it is the same account that opened it. This is correct,
not a limitation to work around. Review produces findings; approval is a human
act. [`internal/review`](../../../internal/review) is built to that shape: it
enforces that the author of a change is never its reviewer, and it can only make
a verdict stricter, never manufacture an approval. Do not add a second machine
account to make an agent able to approve — that reintroduces exactly the property
this section removes.

## 3. What agents must never do

- Never merge with an admin override (`--admin`, or any bypass path) — that is
  the control being routed around, not a merge.
- Never edit branch protection, rulesets, required checks, or bypass actors. That
  is on the escalation list, always.
- **Never drop, unset, or substitute the machine-account credential to obtain a
  more privileged one.** If a command is refused, the refusal is the answer.
  Escalate it; do not re-run it as someone else.
- Never take a green check or a clean review as permission. Those make merge
  *possible*, and a person makes it *authorized*.

## 4. What this does not cover — say so plainly

- **Bypass actors.** If any role, team, app, or deploy key is allowed to bypass,
  the control is as strong as that actor's credentials are separated. Prefer the
  narrowest bypass mode the forge offers over an unconditional one, and know who
  holds it.
- **Deploy.** Where agents run on a person's own workstation with that person's
  credentials, deploy authority is not separable by a repository setting. Nothing
  in this file constrains it. Deploy remains an escalation, held by procedure and
  not by the forge.
- **A credential is not a boundary.** An agent process that can read another
  credential can use it. Scoping the token makes the privileged path deliberate
  and visible rather than default; it does not make it impossible. Only an
  organization-owned repository with the human outside the agents' reach closes
  that.

## 5. Verifying it — by refusal, not by reading

Two failure modes, and the second is the one that produces a confident wrong
answer.

**Protection can live in two different places.** A repository can be protected by
classic branch protection *or* by a ruleset. Reading the wrong one returns `404`,
which looks exactly like "unprotected":

```sh
gh api repos/Bostonvex/machinist/branches/main/protection
gh api repos/Bostonvex/machinist/rulesets
```

Check **both** before concluding anything. A 404 on the first is not evidence.

**A setting read back is not proof.** The setting can be right and still not
apply to the account in question — that is precisely what admin enforcement
decides. Confirm by making the forge say no, from the account the agents actually
use:

```sh
gh pr merge <n>                        # expect: refused while checks are not green
gh pr merge <n> --admin                # expect: refused, or the bypass is wider than you think
git push origin main                   # expect: protected branch hook declined
gh api -X PUT repos/Bostonvex/machinist/rulesets/<id> -f name=x   # expect: 404
```

If a refusal does not arrive, the control is not there, whatever the settings
page says.

The last one matters most under the default above: with approvals at zero, the
only thing standing between an agent and an unreviewed merge is that it cannot
rewrite the rules. An agent account with `admin` rather than `write` silently
removes that, and nothing else in this file would notice.

## 6. When the token is scoped, check reach

Scoping agents to a machine account changes what they can see, not just what they
can do. A repository the machine account was never invited to reports as **not
found**, not as forbidden — so it presents as a network or typo problem and gets
diagnosed as one.

After introducing or rotating a machine account, confirm reach on every
repository the system touches:

```sh
gh repo view Bostonvex/machinist --json viewerPermission
git ls-remote https://github.com/Bostonvex/machinist.git >/dev/null && echo reachable
```

An invitation is not access until it is **accepted by the invitee**. Accepting it
requires acting as the machine account, not as the admin who sent it.
