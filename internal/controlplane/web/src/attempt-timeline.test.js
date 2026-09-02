import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("task detail exposes fenced attempt and fallback evidence", async () => {
  const source = await readFile(new URL("./main.jsx", import.meta.url), "utf8");
  assert.match(source, /Attempt timeline/);
  assert.match(source, /run\.attempt_count/);
  assert.match(source, /run\.max_attempts/);
  assert.match(source, /attempt\.error_class/);
  assert.match(source, /attempt\.profile \|\| attempt\.harness/);
});
