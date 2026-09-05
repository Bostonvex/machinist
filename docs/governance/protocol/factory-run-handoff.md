# FACTORY:RUN handoff schema

> Ported from `Bostonvex/agent-software-factory/factory/protocol/FACTORY-RUN-HANDOFF.md`.
> **Adapted, not copied:** ASF's marker is a single HTML comment with
> space-separated `key=value` attributes. Machinist's is an anchor line followed
> by `key: value` lines, because that is what
> [`internal/factoryrun`](../../../internal/factoryrun) renders and parses. Where
> this document and that package disagree, the package is the contract and this
> document is the bug.

## Choice and rationale

**Schema on GitHub, not channel prose.** The marker is compact and machine-read;
it replaces the free-text `RUN:` / `DISPATCHED:` / `APPLIED:` / `NEXT:` block ASF
retired after measuring roughly 2% emission. A "required" prompt section that is
emitted 2% of the time teaches agents that required sections are optional.

**System of record:** GitHub issue comments (the run marker), pull request
comments (evidence, verdict records), and pull request bodies. Not a second
blackboard, and not a `RUNS/` directory.

## Run marker

Write it on the **issue** when a run starts or its state materially changes:
claim, branch, pull request, stage, park. Search for an existing marker before
starting a second implementation ([factory-start.md](factory-start.md)
idempotency).

```text
<!-- machinist:FACTORY:RUN -->
repo: Bostonvex/machinist
job: job-1
run: run-1
attempt: attempt-1
branch: codex/example
pr: 23
stage: running
verdict: ready-for-human-review
issues: #4
check: Linux checks:success:https://github.com/...
check: macOS checks:success
updated_at: 2026-09-04T12:00:00Z
```

### Fields

| Key | Required | Meaning |
|-----|----------|---------|
| `repo` | yes | `owner/name` the run acts in |
| `job` | yes | Machinist job id |
| `run` | yes | Machinist run id |
| `attempt` | when known | Attempt id |
| `branch` | when known | Task branch name |
| `pr` | when known | Pull request number |
| `stage` | when known | How far the run has got, below |
| `verdict` | when reviewed | One of the three review verdicts, below |
| `issues` | optional | Comma-separated issue references |
| `check` | repeatable | `name:state[:details-url]` |
| `updated_at` | optional | RFC 3339, UTC |

### Stages

`stage` is a closed set, and it is the field that makes a republished marker
material rather than a timestamp bump:

| Stage | Meaning |
|-------|---------|
| `claimed` | the run exists and owns the issue, but has not started |
| `running` | an attempt is executing |
| `complete` | the run finished on its own terms |
| `failed` | the run finished without producing its work |
| `parked` | the run stopped and is waiting on a human |

Operator cancellation parks rather than fails: nothing was demonstrated about
the work, so the next reader is told to look, not that the work is beyond
saving.

### Who writes it

Machinist's control plane publishes the marker for every GitHub-triggered run,
on a scheduler pass, and records what it published. Three consequences worth
knowing before reading a marker:

- **A control plane with nothing new to say makes no GitHub call at all**, so
  the absence of a recent write is not evidence that anything is wrong.
- **Recovery is unbounded.** A run that ended while the control plane was down
  is still waiting to be described when it returns, however much later.
- **History is not re-described.** The database upgrade that turns publication
  on records finished work as already described, so switching it on does not
  comment on every issue the control plane has ever worked. Work still in
  flight is left undescribed on purpose, and gets its marker on the next pass.
- **The published marker carries identity, stage, and what was decided.** The
  verdict and the pull request it was given on appear once a review has been
  recorded against the run through
  [the review route](#where-the-verdict-comes-from). Branch and checks are not
  part of a run's recorded state, so the control plane does not report them and
  does not guess. An agent that knows them may write them; the schema is the
  same either way.
- **More than one review resolves to the strictest.** A run judged twice
  publishes the stricter of the two verdicts, so a later reviewer can add a
  constraint but never lift one another reviewer applied.

### Where the verdict comes from

A reviewer submits its output block to the control plane against the run it
judged. The route decides the verdict; the reviewer does not.

- **Independence is decided from recorded state.** The submission carries no
  claim about who either party is. The control plane reads both identities from
  the runs themselves -- the role each held and the profile that actually ran --
  so an agent cannot review its own work by describing itself differently, and a
  run cannot review itself.
- **The reviewer names the change; GitHub says what it touched.** The changed
  paths come from the pull request's own diff, so protected-path policy applies
  to what the change really contains rather than to what the review says it
  contains.
- **A refused review is not a failed review.** A submission that is not
  independent, or that does not parse, records nothing at all: the run keeps no
  verdict rather than gaining a negative one.
- **The route decides and records. It does not act.** No merge, no deploy, no
  label.
- **The review is asked for, not waited for.** The control plane pairs finished
  GitHub-triggered work with a reviewer that cannot run as the change's author,
  against the one open pull request the issue's timeline names. Where the change
  is ambiguous or no independent reviewer is configured, nothing is assigned and
  the run's marker carries no verdict, which is the honest report.

### The parts that fail closed

These are not stylistic. Evidence is read back by a later session to decide what
already happened, so a field that cannot be read must be an error, never a
permissive default.

- **Only the anchor carries evidence.** Parsing starts at
  `<!-- machinist:FACTORY:RUN -->` and reads the lines after it. A comment that
  merely mentions `FACTORY:RUN` in prose is not evidence.
- **A check has no default state.** `check: Linux checks` with no state is a
  parse error, not a passing check. States are `success`, `failure`, `pending`,
  `neutral`; anything else is rejected. Evidence carrying no checks at all is not
  evidence that checks passed.
- **`verdict` is the review contract, not a free string.** It is
  `ready-for-human-review`, `changes-requested`, or `escalate` — the three
  [`internal/review`](../../../internal/review) can produce — or absent, meaning
  the run has not been reviewed yet. The writer cannot record a verdict the
  review engine would reject, and specifically cannot record an approval the
  engine can never produce.
- **`stage` is a closed set too.** A stage a reader does not recognize is an
  error, and a run state the writer cannot map onto a stage produces no marker
  at all: a guessed stage is worse than an absent one.
- **Republishing unchanged evidence writes nothing.** The stored marker is
  compared as evidence, not as bytes, so a clock that has moved on is not a
  change and a retried handoff does not churn the issue. A stamped `updated_at`
  therefore always marks something that actually changed.
- **The marker is found by its content, not by a remembered id**, so a writer
  that has restarted -- or that never wrote the marker -- edits the existing
  comment instead of adding a second one.

## Role return payloads

Analysis and worker roles return structured blocks to the orchestrator; the
orchestrator mirrors the load-bearing fields into the issue marker. Channel text
stays conclusion plus pointer, with the full evidence on the pull request.

| Role | Return shape | Contract |
|------|--------------|----------|
| intake | `VERDICT:` / `RISK:` | [roles/intake.md](../roles/intake.md) |
| planner | `PLAN_SUMMARY:` / `STEPS:` | [roles/planner.md](../roles/planner.md) |
| implementer | branch, PR, validation evidence | [roles/implementer.md](../roles/implementer.md) |
| reviewer | `VERDICT:` / `FINDINGS:` | [roles/reviewer.md](../roles/reviewer.md) |
| ci-investigator | root cause, fix summary, re-run | [roles/ci-investigator.md](../roles/ci-investigator.md) |
| gatekeeper | `MERGE:` / `RESULT:` | [roles/gatekeeper.md](../roles/gatekeeper.md) |
| post-merge-verifier | `VERDICT:` / `FOLLOW_UPS:` | [roles/post-merge-verifier.md](../roles/post-merge-verifier.md) |

These are real contracts with observable emission — review verdicts and merge
records. They are not the retired channel scroll block.

## Retired — do not reintroduce

```text
RUN: <issue> <branch> <pr|none> <state>
DISPATCHED: <role> → <holder>
APPLIED: …
ESCALATED: …
NEXT: …
```

Dispatch in channel with a short table or bullets; persist state with the marker
on the issue.
