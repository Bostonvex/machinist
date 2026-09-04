// Basic tests for guard-output.mjs.
// Run: node --test plugins/herdr-machinist/scripts/guard-output.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const __dirname = dirname(fileURLToPath(import.meta.url));
const guard = join(__dirname, "guard-output.mjs");

function run(input) {
  return spawnSync(process.execPath, [guard], {
    input,
    encoding: "utf8",
  });
}

test("rejects pure intent-narration with no tool call (exit 1)", () => {
  const r = run(JSON.stringify([{ model: "Let me just run it now" }]));
  assert.equal(r.status, 1);
  assert.match(r.stderr, /emit the tool call/);
});

test("passes a tool call turn (exit 0)", () => {
  const r = run(JSON.stringify([{ model: '<invoke name="bash">echo hi</invoke>' }]));
  assert.equal(r.status, 0);
});

test("passes prose with a real tool call (exit 0)", () => {
  const r = run(JSON.stringify([{ model: 'Checking now.\n<tool_call>{"cmd":"ls"}</tool_call>' }]));
  assert.equal(r.status, 0);
});

test("stops a runaway loop (exit 2)", () => {
  const turns = Array.from({ length: 6 }, () => ({ model: "I'll just run it now" }));
  const r = run(JSON.stringify(turns));
  assert.equal(r.status, 2);
  assert.match(r.stderr, /runaway/);
});
