# Draft until reviewed

A change Machinist writes is opened as a draft pull request. It stops being a
draft when an independent reviewer's verdict is recorded as
`ready-for-human-review`, and at no other time.

## What the draft is holding shut

The window is real and it is measurable. `Bostonvex/machinist#52` was opened
ready for review at 14:37:42Z; the independent reviewer's `changes-requested`
verdict was recorded at 14:49:51Z. For those twelve minutes, unreviewed
machine-written work sat in a change that a person was invited to read, that
CI-gated automation could act on, and that looked exactly like work which had
already passed. It was merged fifteen minutes after the objection.

Nothing distinguishes an unreviewed change from a reviewed one by inspection.
Both are open, both are green, both are titled the same way. Draft is the one
state the forge offers that says *written, not yet vouched for*, so that is the
state the work starts in.

The implementer does not lift it. Its own local review is not independent: it
wrote the change. The prompt says so in as many words, and says never to run
`gh pr ready`.

## What promotes it

The review route promotes, after it has recorded the verdict:

1. It folds **every** verdict recorded against that pull request, not the one
   that just arrived. Two reviewers on one change is the normal case for
   high-risk work, and promoting on the second reviewer's approval while the
   first reviewer's objection still stands would present objected-to work as
   ready to read — the same failure, reached from the other side.
2. It promotes only on `ready-for-human-review`. Any other standing, including
   one this build cannot rank, leaves a draft.
3. It never demotes. Converting a change back would undo a deliberate human
   act — someone marking their own work ready — on the strength of an automated
   verdict, and a later objection is already visible where it is acted on: in
   the recorded verdict, and in what [`machinist merge-owed`](merge-owed.md)
   reports as attention-owed.
4. It promotes; it does not merge. `gh pr ready` is the only write the review
   path makes. What may land is the gatekeeper's decision and is taken
   separately.

Findings on an approval do not block promotion. The gatekeeper already reports
an approval carrying open findings as attention-owed rather than merge-owed, so
the objection is not lost; and if a nit kept the change a draft, nothing would
ever lift it — no second review is scheduled for work that was approved — and
"draft" would quietly stop meaning "unreviewed".

## When promotion fails

Every failure leaves the change a draft, and none of them fails the review.

The verdict is recorded before promotion is attempted and never depends on it,
so a change whose promotion failed still carries its approval and still shows up
in `machinist merge-owed`. Failing the submission instead would invite the
reviewer to retry, and a retry would record the review twice.

The direction of the failure is the point. A change that stayed a draft when it
could have been promoted costs a person one click. The opposite mistake costs a
review.

## Reading it back

The review response says what happened:

```json
{"verdict": "ready-for-human-review", "high_risk": false, "promoted": true}
```

`promoted` is reported rather than inferred from the verdict, because an
approval that could not be promoted and one that was are the same verdict — and
the difference is the only part a reader cannot work out for themselves.
