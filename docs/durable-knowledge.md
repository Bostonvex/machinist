# Durable knowledge

A session ends. What it worked out has to outlive it, or the next one works it
out again — and the second answer is not always the same as the first.

Machinist keeps that record as files in the repository under `notes/`, read and
written by `machinist notes`. Not a database, not a wiki, not a channel: a
directory of markdown, so it is diffed, reviewed, branched and merged with the
change it describes, and so it survives Machinist itself.

## The three kinds

|  | Written | Answers |
|---|---|---|
| `plan` | before the work | what is intended, and how it will be checked |
| `research` | while a question is open | what was found out, and what is still unknown |
| `work-log` | after something changed | what actually happened, and what is left |

They are separate because they are written at different moments and are read
for different reasons. A plan that is edited after the fact to match what
happened is a work log wearing a plan's name, and the difference between what
was intended and what occurred is usually the most useful thing in the record.

Only a plan has a status — `draft`, `active`, `superseded` or `done` — because
only a plan can stop being true. Research and work logs record something that
already happened; they are superseded by a later note saying so, never edited
into agreement with it.

## The shape of a note

```markdown
---
kind: work-log
title: Phase C: the collector moved into Machinist
date: 2026-09-05
subject: Bostonvex/machinist#6
---

## What happened
...
```

`kind`, `title`, `date` and `subject` are required; `status` is required for a
plan and refused on anything else. `date` is a date, not a timestamp: the hour
a note was written is not what a later reader needs, and a timestamp invites a
timezone that sorts two notes written on one day into two.

`subject` is what the note is about — an issue, a repository, a host, a
subsystem. It is the field that makes the record searchable a year later, when
the title no longer means anything to anyone.

Everything fails closed. An unknown front-matter field is an error rather than
something ignored, because it is nearly always a misspelling of a required one,
which would otherwise be reported as absent. An unparsable date is not today. An
unknown kind is not a note of some other sort. A note with front matter and no
body is refused: it would sit in the listing looking like knowledge.

## The verbs

```
machinist notes new --kind work-log --title "..." --subject "..."
machinist notes new --kind plan --status draft --title "..." --subject "..." --body-file -
machinist notes list [--kind plan]
machinist notes check
```

`new` writes the file, names it `<date>-<slug>.md` so a directory listing is a
timeline, and refuses to overwrite one that exists — two notes on one subject on
one day is ordinary, and silently replacing the first with the second is how a
record loses half of itself. With no `--body-file` it writes the headings for
that kind and nothing else; invented prose would be indistinguishable, later,
from prose someone meant.

`list` reads the whole tree and stops on the first note it cannot parse. A
partial answer to "what do we know" is the one shape of answer that is worse
than none.

`check` is the opposite, and deliberately: it is what a hook or a CI job runs,
so it reports every unreadable note rather than the first. Fixing them one CI
run at a time is how a check stops being run.

## Where it came from

`buzz-workspace` kept this record in `PLANS/`, `RESEARCH/` and `WORK_LOGS/` —
markdown in the repository, which was the right instinct and the reason the
pattern is worth porting. What it lacked was a shape anything could check, so
the directories drifted: notes with no date, dates only in the filename, and
several naming conventions at once. The front matter and `machinist notes check`
are what this port adds.
