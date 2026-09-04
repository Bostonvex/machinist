# Deep Code harness guard — upstream change spec

Status: spec for the Deep Code team. Implement in the Deep Code repo; this file
routes the work and is not itself the implementation.

## Goal
Make "no-intent-narration" a harness-enforced, model-independent rule: an agent
running under Deep Code can never stall by writing intent prose with no tool
call.

## Failure to prevent
Turns like "Let me run it now" / "I'll just run it" / "Running it now" with no
tool call, repeated until the step/context budget is exhausted.

## Required behavior (acceptance criteria)
1. One tool call per step before yielding; subsequent calls go to the next step.
2. Reject a turn where there is intent-narration AND no tool call; return a short
   instruction in place of it (e.g. "No prose intent. Emit the tool call now.").
3. No false positives: prose with a real tool call passes unchanged; the guard
   fires only when the turn has no tool call AND an intent phrase is present.
4. Start phrase list (configurable): "let me run", "let me just", "let me
   execute", "I'll run it now", "I'll just run", "running it now", "I'm going to
   run", "I will run it now", "doing it now", "executing now", "I'm running it",
   "for real", "no more loops".
5. Optional hard cap: if the same n-of-last-m steps (e.g. 4 of last 6) are all
   rejected intent-narration, hard-stop the run with a clear error.
6. Log each rejection (hashed/truncated turn + step id) and expose a counter.

## Where to look
In the Deep Code source, find the agent-loop / turn-processing / tool-call
dispatch module and add the guard before dispatch. Confirm exact paths with
`rg`/`ls`; likely names: agent_loop, runner, step, turn, tool_call, or the
harness I/O boundary where an assistant turn is passed to execute.

## Verification
- Unit tests: (a) pure intent turn rejected; (b) prose+tool passes; (c) tool-only
  passes; (d) phrase list is honored/overridable.
- Integration: replay the stall transcripts and assert each is caught at step 1.

## Definition of done
- Guard on by default, cheap, logged, configurable, covered by tests.
- Deep Code builds and existing agent tests stay green.
- Merge/handoff per Deep Code maintainers' process.
