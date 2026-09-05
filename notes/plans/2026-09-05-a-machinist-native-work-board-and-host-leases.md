---
kind: plan
title: A Machinist-native work board and host leases
date: 2026-09-05
subject: Bostonvex/machinist#7
status: active
---

## What this is for

Phase D's second half. `buzz-workspace` holds two operational mechanisms that
Machinist has no equivalent of, and both have to exist here before that
repository can be frozen.

**The board.** `factory-kanban.sh` serves a glanceable page of who is doing
what. It is a projection: GitHub remains the store, and the columns are built
from `CLAIMED`/`RELEASED` comments and labels read live from the issues. What it
buys is that a second agent can see an issue is taken before it starts on it.

**The lease.** `fleet-lease.sh` decides which host's seats may run at all — the
rule being that agents must not run on a machine its owner is sitting at. Today
that rule is enforced by a signed control message in a Buzz relay channel, which
is exactly the collaboration infrastructure the migration plan says to skip. The
Machinist equivalent has to hold the same rule without the relay.

## What it changes

A `leases` table in the control plane, and the worker poll consulting it: a host
whose lease is held elsewhere, or stood down, is offered no work. The refusal
belongs on the dispatch side, not in the worker, so that standing a host down
does not depend on that host being healthy enough to obey.

A board view over `jobs`, `runs`, `workers` and `attempts`, served by the
control plane it already runs. Machinist owns its own work, so unlike the
kanban it does not have to reconstruct state from issue comments — the store
already has it. The claim concept still has to be ported for work that lives on
GitHub issues rather than in Machinist jobs.

## How it is checked

The lease fails closed: a lease that cannot be read is not an absent lease, and
a host with no lease record is not implicitly allowed to run. That is the whole
point of the mechanism — the incident it was written for was a stood-down seat
being woken by an automated chase twenty minutes after being forbidden to.

Each of those is a test that bites: break the read, and the host must be
refused work rather than offered it.
