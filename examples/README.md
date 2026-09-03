# Examples

The files at this level are the small default installed by `machinist init`:

- `config.toml` defines the shipped `foreman`, `audit`, and `shepherd` commands.
- `worker.toml` shows Codex and Claude Code process executors, their matching
  Herdr adapters, an environment-aware typed profile, and a registered
  repository.
- `prompts/` contains the editable default prompts.

`config.toml` includes a commented cron trigger that schedules Shepherd. Enable it only
after its repository name exists in `worker.toml`. Shepherd ensures the repository defines the
`machinist:auto-merge` label, but it never applies the label to a pull request. Unlabelled
pull requests remain read-only to Shepherd.

The [workflow examples](workflows/README.md) are self-contained definitions with exact
setup and run commands:

- issue to pull request;
- Codex and Claude Code multi-review;
- read-only code audit and issue creation.

The [GitHub comment intake example](github-actions/README.md) safely turns a new,
authorized `@machinist` issue comment into a `machinist:requested` label for a
managed GitHub trigger.

After copying the examples into `~/.machinist`, validate before starting a
worker:

```sh
machinist worker validate
machinist worker start                 # unattended process jobs
machinist worker start --transport herdr  # only from a Herdr plugin session
```

For editable terminal work, link the bundled plugin and use its workflow picker
instead of starting the `herdr` worker manually. See the
[Herdr guide](../docs/herdr.md).
