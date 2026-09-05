---
kind: work-log
title: A run that stopped to ask is not a run that finished
date: 2026-09-05
subject: Bostonvex/machinist#77
---

## What happened

An agent that stops to ask a question exits zero. It ran, it did not crash, and
it produced nothing anybody can act on until a person answers. The board filed
that under `done` — the one lane that guarantees nobody looks at it again — and
the issue two lines below the marker said the work was waiting on a human. Both
statements were on the same screen and only one of them could be true.

The marker publisher now checks a completion claim against what the agent left
on the issue. A run that reports `complete` has its issue labels read; if one of
them is `machinist:needs-human` or `machinist:blocked`, the marker is published
as `parked` instead. The board gained a `parked` lane between `review` and
`done`, and `machinist board` says why a card is in it: "stopped, waiting on a
person".

## Why the label wins over the exit status

An asymmetry. "I am blocked" is a claim against the agent's own interest and "I
succeeded" is not. The label is also the fact an operator already reads, and the
control plane already goes to the forge for the facts it will not take an
agent's word for — the commit a review judged, the permission of an actor asking
for work.

Only a run claiming to have finished is checked. A running run is not claiming
anything yet, and a failed one is already reporting against itself. That is also
what keeps the cost at one extra forge call per finished run, which is why
`IssueLabels` was added beside `IssueDetails` rather than folded into it:
intake needs a paginated timeline, and this needs labels.

An unreadable label state is an error and no marker is written. The target stays
unpublished and the next scheduler pass retries. A stage that cannot be
confirmed is not confirmed, and the unconfirmed reading is the one that puts
finished work in front of nobody.

## What the board reads

The resolved stage is stored on `github_run_markers` (schema 15) at publication
time, so the board still asks GitHub nothing — it remains a projection of what
the control plane recorded rather than a reconstruction of what it can see.
Rows written before the column existed carry an empty stage, which means
unknown, and move no card.

## The test that had to exist

The parking tests derive their cases by ranging over `haltingLabels`, which
makes them unable to catch that set shrinking: deleting an entry deletes the
subtest with it. Bite-checking found this by deleting `machinist:blocked` and
watching nothing fail. The fix is a second source of truth —
`TestEveryTerminalLabelTheForemanSetsIsAccountedFor` reads the shipped foreman
prompt, extracts every `machinist:` label it tells an agent to set before a
terminal stop, and requires the two sets to agree in both directions. Removing a
label from the code now fails because the prompt still names it; adding one the
prompt never sets fails too.

## What is left

A label added or removed after the marker was published does not on its own
re-trigger publication — `GitHubMarkerTargets` detects change from run state,
attempt, verdict and pull request only. This is safe for the case the change
exists for, because the agent sets the label before it exits.

The other half of the same fail-open is still there and was out of scope: a
reviewer whose verdict the control plane refused still exits zero.
