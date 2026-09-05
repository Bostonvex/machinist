# Escalation

> Ported from `Bostonvex/agent-software-factory/factory/protocol/ESCALATION.md`.

The `escalation` role is held by a person. This is the list it owns, and the
contract every agent owes it.

Two failures are possible here and only one of them is loud. Escalating too
little hands an agent an authorization nobody gave it. Escalating too much moves
the work onto a person and quietly turns the escalation point into a bottleneck.
The list is bounded in both directions on purpose: **nothing outside it
escalates, nothing inside it proceeds.**

## The list

| Escalation | Granularity |
|---|---|
| Merge to `main` | Per pull request, named |
| Deploy | Per target and procedure, named |
| Pull request approval | Per pull request |
| `risk-high` work | Whether it may be implemented autonomously at all |
| `risk-medium` work | Plan approval before implementation |
| Protected paths | Per issue scope |
| Auth / authz, secrets, credentials | Always |
| Destructive migrations, production data or infrastructure | Always |
| Payments, legal, material spend | Always |
| Rulesets, branch protection, CI configuration, governance | Always |
| Two honest repair attempts failed on one root cause | Park the run |
| Changes to `docs/governance/**` or the agent identities bound to the roles | Always |

Anything not on this list is a decision the system was given the authority to
make.

## The contract

**1. A decision, not a question.** One message containing what is being asked,
the recommended answer, what happens on yes, and what happens on no. It should be
answerable in one word. "What do you think?" is a defect in the agent, not a
question for the human.

**2. Never blocking.** The orchestrator sets `human-required`, updates the
`FACTORY:RUN` marker, and moves to the next issue. Nothing idles waiting for a
reply. Whether this seat is an escalation point or a bottleneck is decided here,
not by how often it is asked.

**3. One authorization, one action.** A previous approval does not carry to the
next pull request, the next deploy, or a case that resembles it. Silence is not
consent. Green CI is not permission. A clean review is not permission.

**4. Batched per run.** One escalation message per run where the decisions can be
taken together, rather than a drip of separate asks.

## Make it a permission, not a promise

On a runtime whose guards are advisory, everything above is a sentence agents are
asked to respect. Convert the load-bearing part into something they cannot do:

- **Require an approving review on `main` from a human.** Merge then stops being
  something an agent declines to do and becomes something the forge refuses.
- **Require the CI checks.**
- **Enforce both on administrators, and run the agents as a machine account with
  `write`.** A required review is a control only because the forge refuses to let
  an account approve its own pull request — which does nothing if the agents and
  the human share one login, and nothing again if that login can bypass.
- **Keep deploy credentials out of the agents' environment.**

The first is worth more than the whole document above it. Where an agent runs on
a human's own workstation with that person's credentials, deploy authority is not
separable this way — say so plainly rather than implying a control that does not
exist. That is the case in this repository today.

**How to configure and verify all of this — including the two ways to read the
settings and get a confidently wrong answer — is in
[governance.md](governance.md).**
