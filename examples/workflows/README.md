# Workflow examples

These examples demonstrate two orchestration styles: prompt-driven native agent
delegation and repository-owned scripts. Each exposes one fixed Machinist command
instead of accepting arbitrary remote shell input.

| Example | Outcome | Orchestration |
| --- | --- | --- |
| [Issue to pull request](issue-to-pr/README.md) | Refine one issue, implement it, review it, and open a PR | One Codex foreman delegates to fresh native subagents |
| [Code audit](code-audit/README.md) | Verify correctness findings and open at most three deduplicated issues | Read-only Codex prompt with explicit evidence gates |
| [Ordered multi-review](multi-review/README.md) | Run Codex and Claude reviews in sequence | Repository-owned shell script with fail-fast behavior |
| [Flow](flow/README.md) | Implement, open a PR, process review feedback, and leave it ready for merge | Repository-owned Python/`uv` script with one persistent Codex thread |

Prompt-driven examples use the configured agent executor directly. Script
examples must be copied into the target repository and registered as approved
worker executors; Machinist treats the script as one bounded run.

Stages inside a script are visible through logs, but are not separate Machinist
attempts. Timeout or cancellation kills the script process tree. A later run
starts from the beginning unless the script owns checkpointing.

Direct execution is useful while adapting an example:

```sh
machinist run --machinist-config=/path/to/example/config.toml \
  --command=EXAMPLE --repo=/path/to/repository --prompt="Task or issue URL"
```

For unattended use, register the repository in `worker.toml`, add the command to
the control-plane configuration, then submit it through the dashboard, CLI, or
a fixed trigger. Interactive Herdr mode is most useful for native agent commands;
opaque orchestration scripts normally remain headless `process` jobs.
