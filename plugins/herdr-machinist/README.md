# Machinist for Herdr

This plugin makes Herdr the interactive terminal surface for Machinist while
Machinist remains the source of truth for job state, routing, retries, and
history.

- **New interactive workflow** opens a picker and submits a `herdr` job.
- **Task board** shows jobs and their live workspace/pane/agent binding.
- The startup hook runs a session-bound interactive worker. It can only claim
  `herdr` jobs; the normal service worker can only claim `process` jobs.
- Agent terminals are ordinary editable Herdr panes. Machinist records lifecycle
  metadata and the terminal binding, but never records raw keystrokes.

Local development install:

```sh
herdr plugin link ./plugins/herdr-machinist --enabled
```

Start or attach to Herdr after linking. Startup hooks run when the persistent
server starts; use **Machinist: Restart interactive worker** if linking into an
already-running server.
