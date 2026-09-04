#!/usr/bin/env node
// Machinist-side execution-discipline tripwire.
import { readFileSync } from "node:fs";
// Reads an agent turn on stdin; exits 0 (pass) if the turn is acceptable,
// exits 1 (reject) with a message if it is intent-narration with NO tool call,
// or exits 2 if the run looks like a runaway intent loop.
//
// This is a repo-side guard that works before any upstream Deep Code change
// lands; see docs/deepcode-harness-guard.md for the permanent runtime fix.

const PHRASES = [
  "let me run", "let me just", "let me execute", "i'll run it now",
  "i'll just run", "running it now", "i'm going to run", "i will run it now",
  "doing it now", "executing now", "i'm running it", "for real", "no more loops",
];

function hasToolCall(text) {
  // A turn that references a real tool call normally contains one of these
  // markers. Tune as needed for the harness transcript format.
  return /<invoke name=|function call|<tool_call|\btool_use:|"tool":"/.test(text);
}

function stallPhrases(text) {
  const lower = text.toLowerCase();
  return PHRASES.filter((p) => lower.includes(p));
}

// Optional: detect a run that is mostly rejected intent turns (runaway loop).
function nearRunaway(turns) {
  if (turns.length < 6) return false;
  let flagged = 0;
  for (const t of turns.slice(-6)) {
    const inModel = t.model ?? "";
    if (!hasToolCall(inModel) && stallPhrases(inModel).length) flagged++;
  }
  return flagged >= 4;
}

const input = readFileSync(0, "utf8");
let turns;
try {
  turns = JSON.parse(input);
  if (!Array.isArray(turns)) throw new Error("not an array");
} catch {
  // Fallback: treat the whole input as the current turn text.
  turns = [{ model: input }];
}

const current = turns[turns.length - 1]?.model ?? "";
if (!hasToolCall(current) && stallPhrases(current).length) {
  if (nearRunaway(turns)) {
    console.error("guard: runaway intent loop — stopping run");
    process.exit(2);
  }
  console.error("guard: no prose intent — emit the tool call now");
  process.exit(1);
}
process.exit(0);
