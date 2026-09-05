---
kind: work-log
title: Phase C: the collector moved into Machinist
date: 2026-09-05
subject: Bostonvex/machinist#6
---

## What happened

`buzz-agent-observability`'s collector, its OpenAI/Anthropic-shaped model
proxy, its providers and its dashboard now run inside Machinist as
`internal/telemetry`. The Python collector is no longer the thing that has to
be running for an operator to see what the agents are doing.

Parity was established by derivation rather than by claim: the Python HTTP
route literals against the Go mux registrations, the Python `add_parser` verbs
against the Go `Use:` strings, the provider files against each other, and the
three dashboard assets diffed. The only gap the comparison found was the `demo`
verb, which is now `machinist collector demo --confirm-synthetic-events`.

## What changed

Five pull requests: #57, #58, #60, #61, #62.

Two of them fixed defects that the parity work uncovered rather than ported
anything. `Close` on the proxy sink raced its own background sender and
cancelled the in-flight submission carrying events it had already taken, so
shutting down lost the last batch. And a model response abandoned mid-body made
`ReverseProxy` panic with `http.ErrAbortHandler`, which unwound past the
reporting and left the call recorded as started and never finished — which an
operator reads as a generation still running.

The last change was a refusal rather than a feature: `[collector] database` and
`[server] database` may no longer name the same file, so the collector's
retention sweep can never run against the control plane's table of runs.

## What is left

#59 — the proxy forwarding tests still close their upstream connection
mid-response on Linux CI, and the cause is unexplained. #61 makes it visible as
a failed call rather than a missing one, which is what Phase C needed; the close
itself is still open.
