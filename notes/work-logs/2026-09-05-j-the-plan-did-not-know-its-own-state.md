---
kind: work-log
title: The plan said nothing had shipped, and named three of the four repos it would stop
date: 2026-09-05
subject: Bostonvex/machinist#85
---

`docs/migration-plan.md` opened with **"Decision recorded; plan document. NOT
yet executed."** Phases A through E were closed (#4–#8), the collector the plan
describes was serving live traffic, and one of the three source repositories was
already retired and archived. The authoritative document for the migration was
the only place you could read that the migration had not started.

Nobody wrote that falsehood. It was written once, correctly, and then five
phases shipped past it. The document had no way to notice, because no part of it
was derived from any other part — the status was a sentence, and a sentence
agrees with nothing.

## What the fix actually is

Not "update the status". Updating the status leaves the next reader in the same
position I was in: trusting a sentence that was true when someone typed it.

The fix is to state each phase's state **twice, in two places that are edited by
different changes**, and let a test compare them. The status table near the top
is what a reader consults; the `_(delivered)_` marker on each `### Phase`
heading is what the person shipping that phase touches. Those two hands move at
different times. `TestThePlanAgreesWithItselfAboutWhatHasShipped` fails when they
disagree.

That is a derived check rather than a restated one, and the difference matters:
a test asserting "the status says A–E delivered" would have to be edited by the
same change that makes it wrong, which is no check at all.

The state vocabulary is closed — `delivered`, `in progress`, `not started`. An
unrecognized word is an error, not a state the test has yet to learn. "done" and
"shipped" both read as fine to a human and compare as different to a machine.

Two guards on the test itself, both of which are the ways a document check
silently passes:

- **It refuses to read nothing.** Zero headings or zero rows is a fatal error,
  not a vacuous success. Bitten by renaming `### Phase` to `### Stage`
  throughout: `read 0 phase headings and 6 status rows`.
- **Line endings are normalised.** The same CRLF trap that failed the docs
  example test on Windows CI earlier today lives here too — every pattern is
  anchored to a line boundary.

One branch had to be rewritten before it could be bitten at all. The issue
column was originally matched as `(#\d+)`, so a row that forgot its issue simply
was not a row, and the failure surfaced as *phase missing from the table* —
pointing the reader at the wrong defect. Matching the cell loosely and checking
it explicitly costs a regexp and buys a message that names what is wrong. **A
check only reachable through another check is not reached.**

Ten mutations, ten distinct messages naming the phase and both states.

## The scope hole this came out of

I filed #84 asking which of three options to take on `auspicia-nextgen`, a
repository with 34 open cards that ASF orchestrates and that the migration plan
never mentions. The repository's owner had answered it **19 hours earlier**, in
`auspicia-nextgen#919`: Machinist is its control plane of record, and the
machine-local registration is "handled outside this issue".

I could not have found that from this document, and that is the defect. §1 named
three source repositories; Phase F would have switched off a service dispatching
work in a fourth; nothing connected the two. The plan is now explicit that
`auspicia-nextgen` is a *consumer* of ASF rather than something being absorbed
by it — no code moves, but the orchestration under it does.

Phase F's own table now separates **drained** from **retired** from
**archived**, because ASF's repository holds 0 open cards while the three
LaunchAgents it ships dispatch 74 cards in two other repositories. An empty repo
read as a drained system; it is not one. What a project runs is what has to
stop.
