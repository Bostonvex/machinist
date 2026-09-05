---
kind: work-log
title: A path that names a retired project is an instruction to recreate it
date: 2026-09-05
subject: Bostonvex/machinist#9
---

Phase F retires what Machinist absorbed. The telemetry half is done: the Python
collector on `127.0.0.1:7900` is stopped, disabled, and its plist moved to
`~/Library/LaunchAgents/retired-phase-f/`, with its 570,849 events archived
rather than deleted.

Retiring the service was the small part. The larger one was that the repository
still told a reader to put Machinist's own secrets in
`~/.config/buzz-agent-observability/`.

That is not a cosmetic reference. A path in a documented example is an
instruction: a reader following `docs/macos-deployment.md` today would create a
directory belonging to a project that no longer exists, and every later reader
would find it there and conclude it was load-bearing. The retired project would
survive its own retirement as a directory nobody could argue with. So the paths
moved with the files — the same bytes, deliberately, because the identity salt
is what producers hash their identities with, and a new one would silently
rename every agent in the record.

Three statements had also become false without anyone touching them.
`docs/fleet-deployment.md` said to run a `buzz-agent-observability` collector;
`docs/adaptive-agent-platform.md` said to keep it as a separately versioned
service; `docs/macos-deployment.md` documented the retired binary's provider
flags and its one-remote-node limit, which #80 removed. The principle in the
second one survived and the subject changed: telemetry stays a separate,
fail-open service, and that was never a claim about who ships it.

The instructive part came last. #74 was filed because a page named a collector
that no longer existed, and the fix was a rewrite — which is exactly the thing
that goes stale again the next time a key is renamed. So the examples are now
read back by the loader that reads a real config, with unknown keys refused:
`TestEveryDocumentedConfigurationExampleLoads` extracts every fenced TOML block
under `docs/`, merges it over the parts an example is entitled to omit, and
loads it as `config.toml` or `worker.toml` according to the keys it declares.

Twenty-seven examples. Two of them did not load, and neither had been noticed by
reading:

- `docs/fleet-leases.md` put `fleet` under a `[worker]` table. There is no
  `[worker]` table; `fleet` is a top-level key in `worker.toml`. The example was
  in the one document about a mechanism that *fails closed on an unnamed
  worker*, telling a reader to name their worker in a way that does not name it.
- `docs/windows-deployment.md` gave a profile `base_url` with no `base_url_env`.
  The loader refuses that pair half-set. A reader pasting it got a worker that
  would not start.

Both are the same defect as #74, and both were invisible to the kind of review
that reads prose for sense. A configuration example is code that happens to be
printed. It should be run.

The half of Phase F that is *not* done is recorded honestly rather than
optimistically: the ASF ticker, kanban and webhook receiver are still running,
because they still orchestrate nineteen live cards — and eleven of them belong
to `auspicia-nextgen`, a repository the migration plan never mentions. Stopping
them would stop real work in a repository nobody agreed to migrate. That is a
finding, not a delay.
