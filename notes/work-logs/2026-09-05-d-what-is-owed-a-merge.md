---
kind: work-log
title: What is owed a merge
date: 2026-09-05
subject: Bostonvex/machinist#70
---

`factory-merge-owed.sh` was 640 lines and most of them were a parser. It found
work that had been reviewed and not landed by walking issue comments looking for
a `VERDICT:` block that named the current head SHA, then classifying findings out
of the prose underneath. That is where its bugs were, and #69 removed the reason
for all of it: the verdict is a row, and the row names the commit.

What was left after subtracting the parser was four rules, and every one of them
was learned by the script being wrong first.

**Two lists, never one.** The first version printed "not mergeable" for both a
conflicted branch and a branch nobody had reviewed. Those need different people.
A change that cannot be merged is a thing to go and fix; a change nobody has
looked at is a thing to go and read. Collapsing them is how work goes quiet, so
the port has three standings and drops nothing into a fourth: every open change
appears exactly once, including drafts and including a standing this build does
not recognise, which prints under its own heading.

**Named states only.** The script matched `mergeStateStatus` against a list of
states it knew, and refused to act on anything else. Someone later "simplified"
that to "not BLOCKED", and a `DIRTY` branch was reported as ready. The port keeps
the named sets — and keeps two of them, because a repository with a merge queue
may enqueue from `BEHIND` and `BLOCKED` while a repository without one may only
land from `CLEAN`.

**The newest run of a check is the one that counts.** Reruns are the normal case
and a check that failed and was re-run and passed is a passing check. The script
learned this by holding a green branch hostage to a flake for two days. The port
takes the newest by report time, prefers when a run finished over when it
started, and refuses when two runs report at the same instant and disagree —
because that is the one case where "newest" does not name a run.

**A closed world on severity.** A finding grade the build does not recognise
stands in the way. Only `info` is advisory, and the blocking set is derived by
subtracting the advisory grades from what the reviewer actually recorded rather
than by restating the ladder in a second place, where it can drift.

Two things are deliberately different from the shell.

The script *dropped* a change whose approval named a commit that had been pushed
over — it simply did not match, so the change fell out of both lists and looked
like nothing was happening. The port reports it as attention-owed, which is what
it is: someone approved work, someone else pushed over it, and a person has to
decide whether that approval still means anything.

And the script drew no line between a required check that failed and a required
check that had not finished. Both were "not ready". They are not the same: one
needs a person and the other needs ten minutes. The port sends the first to
attention-owed and the second to waiting.

The judgement is a pure function in `internal/gatekeeper`, which holds the
package's rule that nothing there calls a forge. The order it decides in is the
part worth keeping: review before landability. Asking "was this approved at this
commit" before asking "can the forge merge it" is what lets a stale approval be
reported while the branch is transiently `BEHIND`, and what stops an unreviewed
change being described in merge-state terms it was never eligible for.

The forge read is one `gh pr list` for the whole repository plus one branch-rules
read per base branch. That is `pr-drain.sh`'s contribution and its scar: its
per-change REST walk exhausted the hourly budget on a repository with forty open
changes. Its other rule is carried verbatim — a rate-limited read refuses and
never retries, and never asks `gh api rate_limit` whether there is budget,
because that endpoint's display lagged a real 403 by 23 seconds when it was
measured.

The command exits 1 when a merge is owed and 0 when it is not, which is what the
cron entries already expect. Every other outcome is a failure to answer, and the
distinction is enforced in three places: a rate-limited read, a listing that will
not decode, and an answer that arrives without saying which repository it read or
when. "Nothing is owed" and "I could not find out" print identically if you let
them, and this is the one command where that is the whole failure mode.

This is the last row in the consolidation capability matrix. Nothing in ASF now
does anything Machinist does not.
