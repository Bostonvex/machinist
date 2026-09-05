# Fleet leases

A fleet lease decides whether a group of workers may take new work at all. It
holds an operator's rule, not the scheduler's: agents must not run on a machine
its owner is sitting at, and a host that has been stood down must stay down
until someone says otherwise.

buzz-workspace held that rule in a signed control message on a relay channel,
which made standing a host down depend on the relay being reachable. In
Machinist it is a row in the control plane's own database, consulted on the
dispatch side — so standing a fleet down does not depend on that fleet being
healthy enough to obey.

## Turning it on

Leasing is off by default. Absence-means-refusal would stop every existing
deployment the moment it upgraded, so turning it on is the operator stating
that they intend to hold the rule:

```toml
[server]
require_fleet_lease = true
```

Once on, it fails closed with no exceptions. A fleet with no lease, an expired
lease, or a lease the control plane cannot read is offered no new work.

Each worker names the group it belongs to, at the top of `worker.toml`:

```toml
fleet = "workshop"
```

A fleet is a statement, not a measurement. Nothing about a machine says which
other machines share its fate, so nothing is inferred: a worker that names no
fleet is refused under required leasing, because a worker that cannot be named
cannot be stood down, and that is precisely the state the mechanism exists to
make impossible.

## Deciding

```
machinist lease list
machinist lease stand-down --fleet workshop --reason "owner is at the keyboard" --for 8h
machinist lease allow      --fleet workshop --reason "owner has gone out"       --for 12h
```

Every decision needs a fleet, a reason and an end.

The **reason** is what the next operator reads at three in the morning. It is
also what a refused worker prints to its log, so it is the sentence that
explains why nothing is running.

The **end** is required because there is no open-ended lease. A permission that
never lapses outlives the situation it was granted for, and the person who
granted it is usually not the person who finds it still in force. Expiry lands
on refusal for either state: an expired `allowed` lease stops work, and an
expired `stood-down` lease does not silently resume it.

`machinist lease list` shows every lease, including expired ones — an expired
lease is the record of a decision — and says plainly when leasing is off, so a
listing of allowed fleets is not mistaken for a fleet under control.

## What it does and does not do

The lease is checked **before new work is offered** and never against work
already running. Cancelling a generation mid-flight is a different decision
with different consequences, and it is not the one an operator makes by
standing a fleet down. A worker holding a run when its fleet is stood down is
still handed that run back to finish and report; it is offered nothing new
afterwards.

A refusal is **not a failure**. The worker is behaving correctly and is being
told not to take work, so the poll answers `200` with a `refused` sentence
rather than an error — a worker that reported the control plane as broken every
few seconds would bury the message the operator needs. The worker prints the
refusal once when it appears and once when it lifts.

A stood-down host **stays on the roster**. The worker record is still updated
on a refused poll, because an operator standing a fleet down needs to keep
seeing it, and a refusal that also made the host vanish would hide its own
effect.

## Reading it fails closed

An unreadable lease state or timestamp is an error, never permission. It is
reported as a fault rather than as a refusal, and a lease write that fails for
the control plane's own reasons answers `500` rather than blaming the operator
for a lease that was already correct.

## API

| Route | Auth | Purpose |
| --- | --- | --- |
| `GET /api/v1/leases` | none | every lease, whether each currently allows work, and whether leasing is enforced |
| `POST /api/v1/leases` | worker token, or browser origin + CSRF | record a decision |

`POST` takes `{"fleet","state","expires_at","reason"}` where `state` is
`allowed` or `stood-down` and `expires_at` is an RFC 3339 time. It answers with
the lease as stored, not as sent.
