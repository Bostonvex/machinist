---
kind: work-log
title: Draft until reviewed
date: 2026-09-05
subject: Bostonvex/machinist#55
---

The foreman prompt said "open one non-draft pull request", and confirmed "open
non-draft state" as a success condition. So every change Machinist wrote was
presented as ready to read from the moment it was pushed, and stayed that way
whatever the independent reviewer went on to say.

The window is measurable. #52 was opened ready at 14:37:42Z and the reviewer's
`changes-requested` was recorded at 14:49:51Z — twelve minutes in which nothing
distinguished it from work that had passed: open, green, titled the same way.
It merged at 15:04:39Z, fifteen minutes after the objection was on the record.

Draft is the one state the forge offers that means "written, not yet vouched
for". The prompt now opens the change as a draft and is told, in as many words,
never to run `gh pr ready` — its own local review is not independent, because it
wrote the change.

Promotion is a control-plane act, in the review route, after the verdict is
recorded. Three things were worth getting right.

It folds every verdict on the change rather than the one that just arrived. Two
reviewers on one pull request is the normal case for high-risk work, and
promoting on the second reviewer's approval while the first reviewer's objection
stands would present objected-to work as ready to read — the failure the draft
exists to prevent, reached from the other direction.

It promotes and never demotes. Converting a change back to a draft would undo a
deliberate human act on the strength of an automated verdict, and the objection
is already where it is acted on: in the recorded verdict, and in what
`merge-owed` calls attention-owed.

Findings do not block it. That was the one place the cautious choice was the
wrong one. Nothing re-reviews approved work, so an approval carrying a nit would
have stayed a draft forever, and "draft" would have stopped meaning "unreviewed"
and started meaning "reviewed at some point". The gatekeeper already reports an
approval with open findings as attention-owed, which is the gate that should be
holding it — promotion is not merge authority.

Every failure leaves a draft, and none of them fails the review. The verdict is
recorded first and never depends on the promotion, so the standing still
reaches `merge-owed`; failing the submission would invite a reviewer retry, and
a retry would record the review twice. The asymmetry is the whole argument: a
change that stayed a draft when it could have been promoted costs a person one
click, and the opposite mistake costs a review.

Eighteen bite checks. The argv ones are worth naming — `gh pr ready` and `gh pr
merge` are two words apart on a command line, and `gh pr ready` with no number
promotes whatever the current directory's branch happens to be.
