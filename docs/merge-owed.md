# What is owed a merge

`machinist merge-owed` answers one question: what did we finish and not land?

It reads every open pull request in a repository, folds in the verdicts the
control plane recorded against them, and sorts the result into three standings.
It reports and it never merges. That is not a limitation of the port — merge is
a human act, and a command that both found the work and landed it would be a
merge bot wearing a report's name.

```
$ machinist merge-owed --repository machinist
Bostonvex/machinist, read at 2026-09-05T13:41:02-04:00

OWED A MERGE (2)
  PR   STANDING FOR  TITLE                        WHY
  #66  3h            feat: the work board          approved at its current commit
  #67  41m           feat: issue claims            approved at its current commit

OWED ATTENTION (1)
  PR   STANDING FOR  TITLE                        WHY
  #59  2d            fix: proxy connection close   the branch has moved since it was approved

NOTHING OWED YET (4)
  PR   STANDING FOR  TITLE                        WHY
  #71  -             feat: merge-owed detection    nobody has recorded a verdict on it
  ...
```

## The three standings

- **merge-owed** — a reviewer approved the exact commit the branch points at
  now, no finding is outstanding, the forge says it can merge, and every check
  the branch rules require has passed. A person can land it.
- **attention-owed** — someone has to look. The approval names a commit that has
  been pushed over, or the verdict was not an approval, or findings are open, or
  a required check failed, or the branch conflicts.
- **waiting** — nothing is owed yet, and nothing is wrong. Nobody has reviewed
  it, or a required check is still running, or the forge has not decided whether
  it can merge.

Nothing is dropped. Every open change appears under exactly one standing,
including drafts and changes carrying a standing this build does not recognise —
those print under their own heading rather than vanishing.

## Exit codes

Carried over from `factory-merge-owed.sh` so the cron entries that call it keep
working:

| Code | Meaning |
| --- | --- |
| 0 | Nothing is owed a merge. Attention may still be owed. |
| 1 | Something is owed a merge. |
| anything else | The question could not be answered. |

"Nothing is owed" and "I could not find out" are the two answers this command
exists to keep apart. A rate-limited read is reported as a failure, never as an
empty report; so is an answer that arrives without saying which repository it
read or when.

```
machinist merge-owed --repository machinist --quiet || notify "work is waiting to land"
```

`--json` prints the same answer as a document and still says it in the exit code.

## Where the judgement lives

The decision is `gatekeeper.Owed`, a pure function over one change and one
judgement. It takes no client and calls no forge, so every rule below is a test
rather than a fixture:

1. **Review before landability.** Whether the work was approved is decided
   before whether it can merge. A stale approval on a branch that is
   transiently `BEHIND` is still reported as needing attention, and a change
   nobody reviewed is never described in merge-state terms.
2. **Two lists, never one.** A change that cannot be merged is not the same as a
   change nobody has looked at, and collapsing them is how work goes quiet.
3. **Named states only.** `MERGEABLE`, `CLEAN`, `BEHIND` and the rest are matched
   by name. A state this build does not recognise never lands anything.
4. **The newest run of a check is the one that counts**, and two runs reporting
   at the same instant with different answers are refused rather than guessed
   at.
5. **A closed world on severity.** A finding grade this build cannot rank stands
   in the way. Only `info` is advisory.
6. **Full commits only.** The approval's commit is compared against the branch
   head in full. Nothing compares abbreviations; the seven characters in the
   messages are for reading.

## What it costs

One `gh pr list` for the whole repository, plus one branch-rules read per
distinct base branch. A repository whose changes all target `main` costs two
requests however many changes are open.

That batching is what `pr-drain.sh` contributed. Its per-pull-request REST walk
exhausted the hourly budget on a repository with forty open changes. A read
that hits the rate limit refuses and names the limit; it never retries into an
exhausted bucket, and it never consults `gh api rate_limit` to decide whether
there is budget — that endpoint's display lagged a real 403 by 23 seconds when
it was measured.

More than 200 open pull requests is refused rather than silently truncated. A
merge that is owed and outside the window is exactly the work this looks for.

## Where the verdicts come from

`run_reviews`, scoped by the run's repository — the same name the review route
scopes a submission on. Several reviewers on one change fold into one judgement:
the strictest verdict wins, every reviewer's findings stay open, and the commit
and the time come from the newest review. A verdict this build cannot rank, a
finding list that will not decode, and a timestamp that will not parse are each
refused rather than defaulted.

The commit binding is what makes any of this possible; see
[a verdict that names what it judged](../notes/work-logs/2026-09-05-c-a-verdict-that-names-what-it-judged.md).

## HTTP

`GET /api/v1/merge-owed?repository=<logical name>`

The repository is named under its logical Machinist name; the control plane maps
it to the forge slug. An unmapped name is refused rather than passed through — a
404 from the forge reads exactly like a repository with nothing open in it.
