# Architecture

Machinist owns process execution, not orchestration.

- `config.toml` defines portable named commands, optional prompt templates, timeouts,
  triggers, and server settings.
- `worker.toml` defines approved executor argument arrays and logical repository paths.
- `internal/runner` starts one process in one repository, writes the prompt to stdin,
  streams both output channels, records artifacts and token usage, and terminates the
  process tree on timeout or cancellation.
- `internal/controlplane` stores one job and one run, leases it to a capable worker,
  rejects stale completions, and exposes authenticated APIs and the web UI.
- `internal/managedworker` resolves only worker-owned executor and repository names.
- `internal/review` decides one independent review: it refuses a review whose author
  and reviewer are the same agent or run, parses the reviewer verdict contract, and
  applies protected-path and severity policy. It only makes a verdict stricter. The
  control plane calls it: a reviewer run submits its output block against the run it
  judged, and the route — not a convention — supplies the evidence the decision needs.
  Both identities are read from the runs themselves (the role each held and the profile
  that actually ran) and the changed paths from the pull request's own diff, so a party
  can neither describe itself into independence nor decide for itself which files it
  changed. A refused review records nothing: a run that could not be reviewed keeps no
  verdict rather than gaining one. A run judged more than once carries the strictest
  verdict recorded against it. The control plane also asks for the review: a scheduler
  pass pairs finished GitHub-triggered work with a configured reviewer whose profiles
  cannot include the agent that wrote the change, so an assignment is never made that
  the route would have to refuse. Which change to review comes from the issue's own
  timeline, and only when exactly one open pull request identifies the work — no open
  change means there is nothing to review yet, and several means the control plane
  would be choosing which change the run made.
- `internal/factoryrun` renders and parses the `FACTORY:RUN` handoff marker that
  carries one run's evidence across sessions. It reads only the lines after the
  marker anchor, requires an explicit state on every check, and accepts only the
  verdicts `internal/review` can produce, so unreadable evidence is an error
  rather than a passing default. The control plane publishes that marker for
  every GitHub-triggered run: a scheduler pass writes the run's stage, and any
  verdict recorded against it, to the run's issue and records what it wrote, so an
  unchanged run makes no GitHub call and a run that ended while the control plane
  was down is still described when it returns. The marker is found by its own
  content, so the comment is edited in place rather than duplicated.

Each job has exactly one run. The database enforces this with a unique `runs.job_id`.
Terminal state comes only from the process result. There is no internal stage model.
