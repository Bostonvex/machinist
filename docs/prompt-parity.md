# The prompt Machinist runs is the prompt Machinist tests

A prompt is not documentation. It is the program the agent executes, and it is
the only part of Machinist with no compiler, no type checker, and no stack trace
when it is wrong. Everything that holds it to a standard is a test in this
repository reading `examples/prompts/`. So the one thing that must stay true is
that the file the tests read is the file the agent runs.

For a while it was not.

## How the copies came apart

`machinist init` installs the shipped prompts into `~/.machinist/prompts/` and
**never overwrites one that is already there**. That is right, and it is not
changing: an operator's edits to their own prompt are theirs, and an upgrade
that silently replaced a tuned prompt with the default would be far worse than
drift.

What was wrong is that it kept the file *silently*. Once a prompt was edited in
place, nothing anywhere recorded that the running copy had diverged. The Foreman
prompt on this machine had gained a whole delegation mechanism — fifteen lines
naming `machinist run --command delegate-plan|delegate-build|delegate-review`
— that existed in no commit, no test, and no review. It was live for weeks. The
repository's copy still said native subagents were the only way to delegate and
told the agent to set `machinist:blocked` when the harness had none.

Both halves of that are bad, and in opposite directions:

- Rules that only exist in the running copy are rules no test can hold. The word
  cap, the forbidden-wording list, the ordering assertions in
  `TestExampleCommandDefinitionsLoad` — none of them ever saw those fifteen
  lines.
- Rules that only exist in the repository copy are rules no agent obeys. Every
  assertion about the shipped Foreman was being checked against a prompt that
  was not running anywhere.

## What changed

**Parity.** `examples/prompts/` now carries what the machine runs:
`foreman.md` with both delegation mechanisms, and `delegate-plan.md`,
`delegate-build.md`, and `reviewer.md`, which existed only on disk.

**The shipped config defines what the shipped prompt runs.**
`examples/config.toml` gained `[commands.delegate-plan]`, `[commands.delegate-build]`,
and `[commands.delegate-review]`. A prompt that tells the agent to run a command
nothing defines blocks the run it was meant to unblock, so the test derives the
set of commands out of the prompt and requires the config to define each one,
rather than restating a list that can go stale on its own.

**`machinist init` names drift.** A kept file that differs from the shipped copy
now reports `kept prompts/foreman.md (differs from the shipped copy)`. It still
keeps it. The only thing that changed is that the divergence is visible the next
time anyone runs `init`, instead of being invisible forever.

The worker token is deliberately exempt: the body offered for comparison is
generated during the run, so every existing token differs from it and none of
them has drifted from anything.

**The reviewer prompt now says what to emit.** `reviewer.md` described the job
well and never named the `VERDICT:` block that `internal/review.Parse` demands.
A reviewer following it would have produced prose, `Parse` would have refused
it, and the route would have recorded nothing — which reads exactly like a
change nobody looked at. The prompt now carries the block, and two tests in
`internal/review` hold it there: one checks that every key, verdict, and
severity the parser knows is named in the prompt, reading all three sets out of
the parser rather than restating them; the other feeds the example finding the
prompt tells reviewers to copy through `Parse` and requires it to come back
whole.

## What is still open

Nothing installs prompts on deploy. `machinist init` is a first-run command, and
the operator path for "take the new shipped prompts" is still to diff and copy
by hand. The drift notice makes that a decision someone can make rather than one
nobody knew was theirs, but it is a notice, not a pipeline.
