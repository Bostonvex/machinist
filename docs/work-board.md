# The work board

`machinist board` shows every job the control plane knows about, in the lane it
is currently in. It is the view to open when the question is "where is work
piling up" rather than "what happened to job X".

```
$ machinist board

QUEUED (2)
  JOB           REPOSITORY  TITLE                          DETAIL
  job_18efd6b3  machinist   Fix the flaky proxy test       queued
  job_2c0a91ff  machinist   Port factory-claim to the CLI  queued

RUNNING (1)
  JOB           REPOSITORY  TITLE                          DETAIL
  job_cf7b83bc  machinist   Publish run markers to issues  shop-floor, attempt 2 of 3

REVIEW (1)
  JOB           REPOSITORY  TITLE                          DETAIL
  job_e62a3472  machinist   Hold fleets from the plane     awaiting review of #65

DONE (14)
  ...
```

## Where it comes from

The board is a projection of what the control plane already recorded: the
`jobs`, `runs`, `attempts`, `review_assignments` and `run_reviews` it wrote as
work moved through it. It does not read GitHub, and it does not reconstruct
state from labels or comments.

That is the difference from the factory kanban it replaces. The kanban had to
rebuild the state of every piece of work from issue labels, because nothing else
knew it — which meant a board that was only as accurate as the last label anyone
remembered to move. Here the board and the dispatcher read the same rows.

## The lanes

| Lane | What is in it |
| --- | --- |
| `queued` | Admitted, waiting for a worker to poll |
| `running` | A worker holds a lease on it |
| `review` | The run finished and was sent for review; no verdict yet |
| `done` | Succeeded, and reviewed if it was sent for review |
| `failed` | Failed, or abandoned after its attempts ran out |
| `cancelled` | Cancelled before it finished |
| `other` | A state this build has no lane for |

`review` is not a job state. It is applied on top of the state, because a run
that has been sent for review is not finished no matter what its own state says:
the run did its work, and nobody has accepted it. Those are separate questions,
so the board answers them separately.

## Two rules it holds to

**Work that does not fit a lane still gets a lane.** A job whose state this
build does not recognise goes to `other`, and its card says which state it
actually holds. A card in the wrong column is a mistake an operator can see and
correct; a card that is simply absent is a mistake nobody can see at all. This
is what makes it safe to add a job state without upgrading every binary at once.

**A read that failed is reported as a failed read.** If any part of the board
cannot be read, `machinist board` exits non-zero with the error and prints no
board. It never prints an empty one. An empty board is a statement that there is
no work, and an unreadable board is not that statement — it is the absence of
one. The same rule holds at the API: `GET /api/v1/board` answers 500 rather than
200 with no columns.

Empty lanes are printed with their count for the same reason at a smaller
scale. An operator scanning for "nothing is waiting on review" should read
`REVIEW (0)`, not have to notice that a heading is missing.

## Flags

| Flag | Effect |
| --- | --- |
| `--lane <name>` | Show only that lane. An unknown name is refused, and the error lists the lanes that do exist — a typo and an empty lane must not look the same. |
| `--json` | Print the board as JSON. This carries full job identifiers; the table shortens them to stay readable. |

## API

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/v1/board` | Every column, in order, always all of them |

The response is `{"columns": [{"lane": "...", "cards": [...]}], "generated_at": "..."}`.
Every card carries `recognised`, which is false exactly when this build had no
lane for its state — readers do not have to infer that from the lane name.
