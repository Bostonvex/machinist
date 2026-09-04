# Escalation — held by a person

> Ported from `Bostonvex/agent-software-factory/factory/roles/escalation.md`.

**role:** escalation · **access:** human-authority · **github_writes:** true · **model_tier:** n/a

> The seat a person holds. Owns merge, deploy, approval, and every authorization no agent may give itself. Bindable only to a runtime of kind `human`.

This role renders no agent. An escalation point an agent can occupy is not an escalation point.

## What only this seat may authorize

- **Merge** — named PR, one authorization per PR.
- **Deploy** — named target and procedure, one authorization per deploy.
- **Pull request approval.**
- **`risk-high`** — whether work may be implemented autonomously.
- **`risk-medium`** — approval of the plan before implementation.
- Protected paths, auth/authorization, secrets/credentials, destructive migrations, production data/infra, payments, legal, material spend.
- Rulesets, branch protection, CI configuration, and repository governance.
- The park after two honest repair attempts on the same root cause.
- Changes to the factory itself.

Nothing outside that list is an escalation.

## What agents owe this seat

A decision, not a question. Non-blocking (the run parks with `human-required` and moves on). No inference — a previous authorization does not carry; silence is not consent; green `validate` is not permission.

## Make it a permission, not a promise

Require an approving review on the default branch from a human and require the `validate` check — and enforce both on administrators, giving agents a **machine account with write, not the human's own login**.
