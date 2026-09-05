---
kind: work-log
title: The prompt that runs and the prompt that is tested
date: 2026-09-05
subject: Bostonvex/machinist#73
---

Deploying #72 meant editing `~/.machinist/prompts/foreman.md` by hand, because
overwriting it would have destroyed work that was only there. The live prompt
had grown a second delegation mechanism — `machinist run --command
delegate-plan|delegate-build|delegate-review` — with three prompt files to match,
none of which existed in any commit.

So every assertion in `TestExampleCommandDefinitionsLoad` had been checking a
file nothing ran, and the fifteen lines that were actually driving every issue
had never been read by a test, a reviewer, or a diff. The word cap that I
argued over for twenty minutes in #55 was a cap on the wrong file.

`machinist init` is not at fault for keeping the edited copy. Keeping it is
correct and stays. Keeping it *silently* is the bug: there was no moment, ever,
at which anything would have said the two had come apart. It now says
`kept prompts/foreman.md (differs from the shipped copy)`, and keeps it.

Bringing `reviewer.md` into the repository found a second thing. It described
the reviewer's job carefully and never mentioned the `VERDICT:` block that
`review.Parse` requires. A reviewer following that prompt would have written
good prose, `Parse` would have refused it, and `submitReview` would have
recorded nothing — which is indistinguishable from a change nobody reviewed.
That prompt has been live for as long as `delegate-review` has. It has never
successfully recorded a verdict, and nothing anywhere would have said so.

The two tests that now hold it read the key set, the verdict set, and the
severity set out of the parser instead of restating them, and run the example
finding through `Parse` rather than eyeballing it. A prompt is the one part of
this system with no compiler; the closest available substitute is a test that
derives its expectations from the code the prompt has to satisfy.

Twenty-one bite checks. Two of my first attempts did not bite, both because I
removed a word from one place and it survived in another — `escalate` in the
rules as well as the block, `blocker` in the sentence after the block. Both
non-bites were the test being right and the mutation being weak, which is the
good direction for that mistake to run in. Two assertions were deleted after
biting proved they could not fail on their own: a guard on the token's message
that the line-by-line check already covered, and a `Stat` of a prompt file that
`LoadCommand` already reads.
