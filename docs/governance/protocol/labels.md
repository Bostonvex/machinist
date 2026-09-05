# GitHub labels

> Ported from `Bostonvex/agent-software-factory/factory/protocol/LABELS.md`.

Machinist runs on two label vocabularies and they are not interchangeable.

## Run state — the ASF vocabulary

Exactly one at a time. This is a partition, not a set of tags.

| Name | Purpose |
|------|---------|
| `agent-ready` | Assessed, risk-labelled, and eligible to be claimed |
| `agent-running` | Active run |
| `agent-review` | Draft pull request awaiting review |
| `agent-blocked` | Needs a human after repeated failure |
| `human-required` | Escalated; agents must not implement |

## Risk — exactly one per issue

`risk-low` · `risk-medium` · `risk-high`

## Type

`type-bug` · `type-feature` · `type-maintenance` · `type-documentation`

## The `machinist:` labels already in use

This repository also carries `machinist:ready-for-review`, `machinist:verifying`,
and `machinist:blocked`, written by the existing loop. They mean roughly what
`agent-review`, `agent-running`, and `agent-blocked` mean above.

**Do not run both vocabularies as if they were one partition.** Until the two are
reconciled — Phase D, when the work board lands — treat the `machinist:` labels
as the live ones in this repository and the table above as the contract the
ported roles are written against. An issue carrying one label from each
vocabulary has no defined state.

## Creating the labels

```sh
gh label create agent-ready --description "Eligible to be claimed" --color "0E8A16" || true
gh label create agent-running --description "Active run" --color "1D76DB" || true
gh label create agent-review --description "Draft PR ready for review" --color "5319E7" || true
gh label create agent-blocked --description "Blocked after repeated failure" --color "B60205" || true
gh label create human-required --description "Human must take over" --color "D93F0B" || true
gh label create risk-low --description "Risk: low" --color "C2E0C6" || true
gh label create risk-medium --description "Risk: medium" --color "FEF2C0" || true
gh label create risk-high --description "Risk: high" --color "E99695" || true
gh label create type-bug --color "D73A4A" || true
gh label create type-feature --color "A2EEEF" || true
gh label create type-maintenance --color "FBCA04" || true
gh label create type-documentation --color "0075CA" || true
```
