---
kind: work-log
title: A verdict that names what it judged
date: 2026-09-05
subject: Bostonvex/machinist#68
---

`run_reviews` recorded a verdict and the pull request it judged, but not the
commit. So an approval survived a force-push. Nothing was wrong with the row —
it said a reviewer looked at PR #41 and approved it, and that was true. It just
could not be re-checked, and a judgement that cannot be re-checked is one that
quietly starts applying to work nobody looked at.

`factory-merge-owed.sh` knew this. Most of its complexity was `classify_findings`
and `verdict_comment_at_head` — a parser that walked issue comments looking for
a `VERDICT:` block that mentioned the current head SHA, through Markdown
blockquotes, code fences, and prose restatements of an earlier verdict. Three
separate bug numbers are cited in that function's comments, all the same shape:
some formatting variant made a stale verdict look current.

None of that machinery is about reviewing. It is about the verdict living in a
comment. Machinist records the verdict structurally, so binding it to a commit
is one column.

The part worth getting right was where the commit comes from. The review route
already refuses to let the reviewer name the changed paths — it reads the diff
from the forge, because "neither the reviewer nor the author gets to say
either." Which commit was reviewed is the same kind of claim, and a reviewer
that could name its own head could name one it had already approved. So the
control plane asks the forge, before it evaluates the reviewer's output, so the
commit recorded is the one that was in place while the judgement was being made.

A head the forge will not give up refuses the whole submission and records
nothing. That is not the cautious choice, it is the only coherent one: the
question a stored verdict answers is "does this approval still apply", and
without a commit that question has no answer. An unanswerable approval that is
stored anyway will be read by something as a yes.

Rows written before the column carry an empty head, and the upgrade leaves them
that way. It would have been easy to backfill them with the current head — the
data is right there — and that would have manufactured exactly the approval this
column exists to prevent.

Twenty-one bite checks. One found a test passing for the wrong reason: the
store-level test that a non-commit is refused used run IDs that do not exist, so
a foreign key refused the write before anything looked at the head at all. The
test now asserts the message names `reviewed_head`, which is what separates the
two refusals.
