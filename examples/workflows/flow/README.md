# Flow: task to reviewed pull request

One file takes a task from implementation to a pull request that has answered its
reviewers. The script owns git and GitHub. One Codex thread owns the code and keeps its
context from implementation through every repair.

1. Create a branch and ask the thread to implement the task, have a fresh subagent review
   the diff, run the tests, and commit.
2. Ask the thread for a title and body as JSON, push, and open the pull request.
3. Wait for feedback. Feedback is anything newer than the last push: an unresolved review
   thread with a new comment, a review that requests changes, or a failing check on the
   current head.
4. Hand the feedback to the same thread. It triages each item: real defects get the
   smallest fix, nitpicks and impossible edge cases get a short reply saying why, unrelated
   failing checks get a separate commit or are called a flake. It replies in and resolves
   every thread it handled. A person can reopen any of them.
5. Push and go back to step 3, at most `FLOW_MAX_ROUNDS` times, then wait once more for the
   verdict on the last push.

The loop ends with exit 0 when the pull request is approved after its latest push with green checks, when no new
feedback arrives within `FLOW_FEEDBACK_WAIT` seconds, or when the thread decides nothing
needs to change. It ends with exit 1 when feedback is still open after the last round. The
last push is the only cursor, so the script keeps no state on disk and a rerun starts from
a fresh branch and a fresh thread.

## Set up

Copy `flow.py` into the repository Machinist will run, for example as `scripts/flow.py`.
The file declares its own dependency on the [Codex Python SDK](https://github.com/openai/codex/tree/main/sdk/python)
with inline script metadata, so `uv run` installs it on first use. Add the executor to
`worker.toml`:

```toml
[executors.flow]
command = ["uv", "run", "--script", "./scripts/flow.py"]
```

The worker needs `uv`, authenticated `git` and `gh` commands, and network access to PyPI on
the first run. The SDK bundles its own Codex binary, so `codex` need not be on the path, but
the worker must already be logged in to Codex. To lock the resolved dependency next to the script, run
`uv lock --script scripts/flow.py` and add `--locked` to the executor command.

Token usage is written to `MACHINIST_TOKEN_USAGE_PATH`, so Machinist records it for the run
the same way it does for a direct Codex executor.

The thread runs with Codex's full-access sandbox by default because it must reach GitHub to
reply in review threads. Machinist workers are isolated machines, which is what makes that
acceptable; set `FLOW_SANDBOX=workspace-write` if your Codex config grants network access
another way.

## Run it

```sh
machinist run \
  --machinist-config=/path/to/machinist/examples/workflows/flow/config.toml \
  --command=flow \
  --repo=/path/to/repo \
  --prompt="Add a --json flag to the status command"
```

Or queue it through the control plane with `machinist submit`, or select it from a trigger.

## Limits

Machinist sees only the script's output and exit code. A timeout or cancellation kills the
script and the Codex process underneath it. The thread resolves review threads it has
answered, because branch protection often requires it; reopen any you disagree with. The
script never merges. Bot reviewers that reply within minutes fit the
default wait of 30 minutes; raise `FLOW_FEEDBACK_WAIT` and the command timeout for repos
that rely on human review.
