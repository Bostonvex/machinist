import assert from "node:assert/strict";
import test from "node:test";
import { sortedProfiles, workerEnvironmentLabel } from "./worker-environment.js";

test("worker environment label is compact and honest for legacy workers", () => {
  assert.equal(workerEnvironmentLabel({ os: "linux", arch: "arm64", execution: "wsl", shell: "bash" }), "linux/arm64 · wsl · bash");
  assert.equal(workerEnvironmentLabel({}), "Environment unavailable");
});

test("worker profiles are stable and tolerate missing data", () => {
  assert.deepEqual(sortedProfiles(undefined), []);
  assert.deepEqual(sortedProfiles({ local: { available: true }, codex: { available: false } }).map(([name]) => name), ["codex", "local"]);
});

