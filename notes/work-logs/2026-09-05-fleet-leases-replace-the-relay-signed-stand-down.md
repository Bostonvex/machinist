---
kind: work-log
title: Fleet leases replace the relay-signed stand-down
date: 2026-09-05
subject: Bostonvex/machinist#7
---

## What was built

Fleet leases: a row in the control plane's database that decides whether a
group of workers may take new work at all. `require_fleet_lease` in `[server]`
turns it on, `fleet` in `[worker]` says which group a machine belongs to, and
`machinist lease list|allow|stand-down` is how an operator decides.

This is the Machinist replacement for buzz-workspace's `fleet-lease.sh`.

## Why it does not look like the thing it replaces

buzz-workspace held the rule in a signed control message on a relay channel.
That made standing a host down depend on the relay being reachable — the
mechanism you reach for when a machine is misbehaving was itself downstream of
that machine's collaboration infrastructure working.

The migration plan says to skip Buzz's relay and channel signing entirely, so
the rule had to be held somewhere that does not depend on the fleet being
healthy enough to obey. It is consulted on the dispatch side, inside the poll
transaction, so a lease revoked between the read and the dispatch cannot let one
more run through.

## The decisions worth remembering

**Leasing is a setting, not the default.** Absence-means-refusal is the correct
fail-closed reading, and it would have stopped every existing deployment the
moment it upgraded. Turning it on is the operator saying they intend to hold the
rule. Once on there are no exceptions — including for a worker that names no
fleet, which is refused precisely because a host that cannot be named cannot be
stood down.

**The gate sits below the worker upsert and below the active-run branch.**
Below the upsert, so a stood-down fleet still shows as alive rather than as a
host that has disappeared; a refusal that also made the host vanish from the
roster would hide its own effect. Below the active-run branch, so a run already
in flight is still handed back to be finished and reported. Cancelling a
generation mid-flight is a different decision with different consequences, and
it is not the one an operator makes by standing a fleet down.

**A refusal is not a failure.** The poll answers 200 with a `refused` sentence.
A worker being told not to take work is behaving correctly, and an error status
would have it report the control plane as broken every few seconds. The worker
prints the refusal once when it appears and once when it lifts, because a
standing refusal repeated every poll would bury the log the operator reads to
find out why nothing is running.

**Expiry lands on refusal for either state.** A lease is a statement with a
deadline, and a deadline that quietly extends itself is not one. There is no
open-ended lease: a permission that never lapses outlives the situation it was
granted for, and the person who granted it is usually not the person who finds
it still in force.

**Every reading failure is a fault, never permission.** An unreadable state or
timestamp is an error rather than a refusal, and a lease write that fails for
the control plane's own reasons answers 500 rather than blaming the operator for
a lease that was already correct.

## Two tests that had to be tightened

`SetLease` validated the state and so did the handler, which meant a test named
"refused at the edge" could not tell which one refused. Validation now lives in
`SetLease` alone, wrapping `ErrInvalidLease` so the handler can tell a bad
request from a failed write — which also fixed a real defect where a database
fault was reported as a 400.

The write handler echoed its own input rather than the stored row, so an
operator who typed a padded fleet name would be shown their typing rather than
the name a poll will actually be matched against. It now re-reads the lease.

## Still open

The Phase D work board — a board view over jobs, runs, workers and attempts,
plus the GitHub-issue claim concept from `factory-claim.sh` — is unstarted. It
is described in [[a-machinist-native-work-board-and-host-leases]].
