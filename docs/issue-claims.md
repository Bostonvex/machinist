# Issue claims

An issue claim says who is working on a GitHub issue and until when. It exists
so that two agents pointed at the same backlog do not both start on the same
issue, discover it four hours later, and throw one of the two efforts away.

The agent-software-factory held claims in the issue thread. `factory-claim.sh`
wrote `CLAIMED:` and `RELEASED:` comments and replayed them to work out who
held what. That grammar had two failure modes that cost real work: a reader
whose paginated fetch stopped short of the last page concluded the issue was
free, and two agents commenting within the same second both concluded they had
taken it, because a comment is not a decision — it is a record of one, and
nothing arbitrated between them.

In Machinist a claim is a row keyed on `(repository, issue)`. The database
arbitrates. Taking an issue somebody else holds is refused, not recorded and
reconciled afterwards.

## Taking and giving back

```
machinist claim take --issue Bostonvex/machinist#7 \
  --holder workshop-a --reason "porting the claim concept" --for 4h
machinist claim list
machinist claim release --issue Bostonvex/machinist#7 \
  --holder workshop-a --reason "landed in #67"
```

Every claim ends. `--for` is required and there is no open-ended form: a claim
that never lapses becomes a lock the moment its holder stops answering, and the
agent that finds it standing is never the one who took it. An expired claim
stops nobody, and holding one does not entitle its holder to silence — extend it
and the original `claimed_at` is preserved, so how long an issue has really been
in one pair of hands stays visible.

A release deletes the row. A row that lingers saying nobody holds the claim is
state every future reader has to remember to discount, and one of them will not.

## Stopping without giving back

Work stops for reasons that are not "done" and are not "free for anyone". An
agent blocked on a review, or redirected onto an incident, should be able to say
so without the issue being picked up behind it:

```
machinist claim hold --issue Bostonvex/machinist#7 \
  --holder workshop-a --reason "blocked on the schema decision in #9" \
  --for 24h --transfer Bostonvex/machinist#9
```

A held issue is still not free work. `--for` is required here too, for the same
reason: a hold with no end is an abandonment that reads like a plan.

## What is refused

Every refusal below is a refusal, not a default:

- An issue held live by somebody else — the error names the holder and when the
  claim runs out, because "taken" without "by whom" is not actionable.
- A release or hold of an issue nobody holds, or that somebody else holds. A
  release that silently succeeds is how a stale read gets confirmed.
- A claim with no expiry, and a claim whose expiry has already passed. These are
  different mistakes and get different messages.
- A claim state or expiry the running build cannot read. An unreadable row is an
  error; it is never treated as free work. The whole mechanism exists to stop
  two agents on one issue, and a reader that guesses on the way to that answer
  is worse than no reader.
- Anything that is not `owner/repo#n` in `--issue`. A claim written against a
  repository nobody meant is worse than a claim that was not written.

## Over the API

```
GET  /api/v1/claims          every claim, with whether it is still live
POST /api/v1/claims          {"action": "take"|"release"|"hold", ...}
```

Reading is open to anything that can reach the port, as the status and board
views are. Writing needs the submission token: a claim nobody is accountable for
is not a claim. An action other than the three above is refused rather than
guessed at, and a conflict over who holds an issue is a `409`, distinct from the
`400` that means the request was malformed.
