# Pull requests

> Ported from `Bostonvex/agent-software-factory/factory/policy/pull-requests.md`.

- Target `main` as a **draft** until human review.
- Link the issue; include validation evidence.
- Never merge your own pull request; never enable auto-merge.
- **When the pull request is ready, ASK for merge permission — do not wait to be
  asked.** Ready means the required checks are green and review findings are
  resolved. Say what the change does, what validation shows, and what would be
  deployed, then ask. Silence is not a queue: a pull request nobody mentions is a
  pull request nobody merges.
- Asking is not permission. Merge only after a human names **that** pull request,
  and never infer it from green CI, a clean review, or permission given for a
  different pull request.
- Never approve pull requests as an agent, at any risk level. Where agents share
  one machine account the forge refuses this anyway — the `reviewer` role
  produces findings, never an approval. See
  [roles/reviewer.md](../roles/reviewer.md) and
  [`internal/review`](../../../internal/review), which can only make a verdict
  stricter and can never manufacture an approval.
- A pull request blocked on `REVIEW_REQUIRED` is **working as designed**, not a
  fault to route around. Never merge with `--admin` or any bypass, and never swap
  to a more privileged credential because one was refused.
- With strict status checks, a pull request must be up to date with `main` before
  it can merge — update the branch and let the checks re-run rather than asking
  for permission twice.
- Pushing to a pull request dismisses any approval it already had. Get the branch
  final, then ask.
- Use Conventional Commit messages, and keep the commit message honest about what
  was verified.
- **Clean up your worktree once the pull request merges.** Machinist work happens
  in per-branch worktrees, and a worktree left behind pins its branch name, so
  merged branches cannot be pruned and `git worktree list` stops reliably
  describing what is actually in flight. Before removing anything, confirm with
  `gh pr view <number> --json mergedAt,headRefOid`, `git rev-parse <branch>`, and
  `git -C <path> status --porcelain` that the pull request is merged, the branch
  tip still matches the reviewed head, and the worktree is clean. If any check
  fails, **do not remove the worktree or delete the branch**; report what remains.
  Once all checks pass, run `git worktree remove <path>`, then
  `git branch -d <branch>`. Because these pull requests squash-merge, `-d` may
  still refuse a safe deletion; use `-D` only after the same merged-head and
  clean-tree checks pass.

[protocol/governance.md](../protocol/governance.md) records which of these the
repository enforces on its own and which are still only this file.
