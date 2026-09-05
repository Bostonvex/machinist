---
kind: work-log
title: A work board projected, not reconstructed
date: 2026-09-05
subject: Bostonvex/machinist#7
---

`factory-kanban.sh` was 996 lines and every one of them existed because nothing
in the factory knew what state a piece of work was in. It read GitHub issue
labels, classified them into nine columns, and rendered a board that was only as
accurate as the last label someone remembered to move.

Machinist's control plane already writes down every job it admits, every run it
dispatches, every attempt a worker makes, every review it assigns and every
verdict that comes back. So the board is 250 lines, and it is a projection of
those rows rather than a reconstruction from what happened to be visible from
outside. The board and the dispatcher read the same tables — they cannot
disagree.

## What was carried over

Two rules from the kanban, both of which it had learned the hard way, and both
of which survive here as tests rather than as comments:

**Unclassified goes to a lane, never absent.** The kanban's `other` column came
from an incident where work with an unexpected label simply stopped appearing.
Here a job state this build has no lane for lands in `other` with its raw state
on the card, and `TestEveryLaneTheStateMapCanProduceHasAColumn` derives the
check from the state map rather than restating the lane list, so a new state
cannot be added to one and forgotten in the other. A card in the wrong column is
recoverable; a missing card is not.

**An unreadable read banners rather than rendering as zero.** The kanban's note
was "ignoring it is what let a failed read render as zero". `Store.Board` fails
whole: any read that does not come back is an error and no board is returned.
The handler answers 500, and the CLI exits non-zero and prints nothing. An empty
board is a statement that there is no work; an unreadable board is the absence
of a statement, and the two must never look the same.

## What was not carried over

The kanban's nine columns collapsed to seven, because three of them
(`authoring`, `plan`, `human`) were distinctions the label scheme had to make
that the store makes for free through the command a job is running. Adding them
back as lanes would have meant re-encoding a fact that already exists.

## Decisions worth remembering

- **`review` is an overlay, not a state.** A run that succeeded and was sent for
  review is not done — the work is finished and nobody has accepted it. Folding
  that into the state map would have meant one table encoding two different
  questions. The state says what the run did; the assignment says whether anyone
  accepted it, and they are answered separately.
- **A verdict with no text is an error, not an acceptance.** An empty verdict
  string would have moved a card out of the review lane while nobody had said
  anything about it. The read refuses instead.
- **`recognised` is on the card.** A reader should not have to infer "this build
  did not understand the state" from the lane being named `other`.
- **Empty lanes are printed with their count.** A board that drops empty lanes
  changes shape as work moves, so an operator scanning for "nothing waiting on
  review" would have to notice an absence rather than read a zero.
- **A `--lane` filter that matches nothing is refused.** An empty board and a
  misspelled lane name look identical and mean opposite things: one is good
  news, the other is a typo. The error lists the lanes that do exist.

## Bite checks

Twenty-six mutations, all confirmed to fail the test that claims to cover them.
The ones worth naming: dropping `LaneCancelled` from the column order is caught
by the derived-set test, not by a restated list; making the CLI print a
short-form job id in `--json` is caught, because a board nothing can be scripted
against is not a board; and making the CLI swallow a failed read and print an
empty board is caught at the exit code as well as at the text.

Related: [[fleet-leases-replace-the-relay-signed-stand-down]].
