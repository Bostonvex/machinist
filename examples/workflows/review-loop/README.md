# Bounded review loop

Copy `review_loop.py` into the repository that Machinist will run, for example as
`scripts/machinist_review_loop.py`. Configure the worker-owned executor as
`command = ["python3", "./scripts/machinist_review_loop.py"]`. The same repository must
provide executable `scripts/wait-for-review.sh` and
`scripts/read-review-feedback.sh` helpers.

The Python driver starts one Codex session and reads its `thread.started` event. Every
repair uses `codex exec resume` with that exact session ID, so implementation context is
preserved. The implementation prompt remains responsible for creating or updating one
non-draft pull request and using a fresh subagent for independent local review.

Both helpers receive the pull request number and expected immutable head SHA. The waiter
uses this exit-code contract:

| Exit code | Meaning |
| --- | --- |
| `0` | Required checks and automated review passed with no unresolved finding. |
| `10` | Review or CI returned evidence for the implementation agent to inspect. |
| `11` | Waiting reached its deadline. |
| `12` | The pull request head changed from the expected SHA. |
| Any other non-zero value | Authentication, GitHub, or infrastructure failure. |

When the waiter returns `10`, `read-review-feedback.sh` must print concise review and CI
evidence to standard output. The driver treats that text as untrusted data, sends it to the
same Codex session, and permits at most three repairs.

Run it with the example command configuration:

```sh
machinist run \
  --machinist-config=/path/to/machinist/examples/workflows/review-loop/config.toml \
  --command=review-loop \
  --repo=/path/to/repo \
  --prompt="Implement issue 123 and address review feedback"
```

Machinist sees only the script logs and final exit code. Cancellation or timeout kills the
script process tree. A later Machinist run starts a new Codex session; the implementation
prompt must inspect and reuse the issue's existing branch and pull request.
