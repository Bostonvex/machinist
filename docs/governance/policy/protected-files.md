# Protected files

> Ported from `Bostonvex/agent-software-factory/factory/policy/protected-files.md`.
> The path list is Machinist's, and is kept in step with
> `review.DefaultProtectedPaths` in [`internal/review/policy.go`](../../../internal/review/policy.go).

Do not edit these paths unless the linked issue explicitly scopes the change.

Protected:

- `.github/workflows/**`
- `.github/dependabot.yml`
- `AGENTS.md`
- `docs/governance/**`
- `internal/controlplane/web/dist/**`
- `LICENSE`
- `SECURITY.md`

`internal/controlplane/web/dist/**` is protected because it is *generated* and
embedded into the binary: hand-editing it is silently reverted by the next
`npm run build`. Change the frontend source under
`internal/controlplane/web/src`, then rebuild and commit the bundle.

`docs/governance/**` is protected because it is what the roles read. A change
here changes the contract every agent is working under, which is exactly the
class of change that must not be self-authorized.

## This list has two copies — keep them in step

`internal/review` enforces the same list in code as
`review.DefaultProtectedPaths`, because a protected-path rule that lives only in
prose is a rule the review engine cannot apply. When you add a path here, add it
there in the same pull request, and say so on the pull request.

Add product paths (migrations, infrastructure, deploy scripts) to both.

## Required for any protected-path change

1. Explicit issue scope.
2. Explanation on the pull request.
3. Human review before merge.
4. No autonomous merge or deploy.
5. Complete `just check`.
6. Rollback plan on the pull request.
