# Security

> Ported from `Bostonvex/agent-software-factory/factory/policy/security.md`.

- Never commit secrets, tokens, private keys, or filled `.env` files.
- Never log credentials or production customer data into pull requests or issues.
- Prefer least privilege; no production credentials in agent environments.
- Do not weaken CI, Dependabot, or branch protection — and do not **bypass** them
  either. Never use an admin override, and never fall back to a more privileged
  credential because the one you were given was refused. A refusal is an answer;
  escalate it.
- Never print a credential to check whether it is set — print its length.
- Guards are defense in depth, not the sole security boundary, and their strength
  varies by runtime.
- Merge and production deploy **escalate to a human** rather than being refused
  outright: the contract allows both with explicit permission for a named pull
  request or target, and a hard deny would block the authorized case.
  Push-to-`main`, pull request approval, and destructive commands stay hard
  denials.

Report vulnerabilities through [`SECURITY.md`](../../../SECURITY.md), not a
public issue.
