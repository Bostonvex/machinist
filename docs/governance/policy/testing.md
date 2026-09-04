# Testing

> Ported from `Bostonvex/agent-software-factory/factory/policy/testing.md`.
> Adapted: ASF's single `scripts/validate.sh` is Machinist's `just check`.

- New or changed behaviour needs tests, or the issue documents why tests are N/A.
- Do not skip, delete, or weaken tests to force green.
- Definition of done is the complete check, not a reduced-scope subset: `just check`.
- The blocking CI checks are the `Linux checks`, `macOS checks`, and
  `Windows checks` jobs in [`ci.yml`](../../../.github/workflows/ci.yml). Their
  names are load-bearing: branch protection pins the required check by name, so
  renaming a job silently unpins the requirement.
- Go changes run under the race detector (`go test -race ./...`). A test that
  only passes without `-race` is not passing.
