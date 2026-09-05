---
kind: work-log
title: A claim is a row, not a comment
date: 2026-09-05
subject: Bostonvex/machinist#7
---

`factory-claim.sh` decided who owned a GitHub issue by writing `CLAIMED:` and
`RELEASED:` comments into the issue thread and replaying them. That works right
up until it does not, and it failed in two ways that both cost real work.

The first was pagination. A reader that fetched comments and stopped before the
last page saw no claim and concluded the issue was free. Nothing about that read
was malformed — it was a correct read of an incomplete fetch, which is the worst
kind, because there is nothing to detect.

The second was that a comment is a record of a decision, not the decision. Two
agents could both post `CLAIMED:` within the same second, both see their own
comment land, and both start work. The script had a tie-break that read the
earliest timestamp, which turned a race into a rule only for readers that ran
after both comments existed. The agent already three commits in did not re-read.

Machinist has a database, so the claim is a row keyed on `(repository, issue)`
and the database arbitrates. The same-second collision is not something to
detect afterwards; it is not writable. That deletes most of the shell script,
but the interesting part is what did not delete.

What survived is the judgement, not the mechanism. Every claim ends — `--for` is
required, with no open-ended form — because the factory learned that a claim
without an expiry becomes a lock the moment its holder stops answering, and the
agent that finds it standing months later has no standing to break it. Extending
a claim preserves the original `claimed_at`, so an issue that has been in one
pair of hands for a week cannot present as freshly taken. And `hold` exists as a
state distinct from both held and free, because work stops for reasons that are
neither "done" nor "anyone may take this", and an agent blocked on a review
needs to say so without the issue being picked up behind it.

The fail-closed rule from the leases work carried over unchanged: a claim state
or expiry this build cannot read is an error, never free work. A reader that
guesses on the way to answering "is anyone working on this" defeats the only
thing the mechanism is for.

Two of the thirty-eight bite checks on this change found overlapping gates —
places where deleting a check changed nothing because a later check happened to
catch the same input. Supplying no expiry and supplying one that has already
passed are different mistakes; an agent that supplied nothing is not helped by
being told their claim expired. Both tests now assert the message, which is what
keeps the two checks from silently collapsing into one.
