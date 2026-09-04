# DeepCode evaluation for the DGX-backed DeepSeek profile

## Decision

Use [`lessweb/deepcode-cli`](https://github.com/lessweb/deepcode-cli) 0.3.1 as
the primary DeepSeek harness for trusted Mac mini work. Keep the official
[`deepseek-ai/deepseek-harness`](https://github.com/deepseek-ai/deepseek-harness)
available as an opt-in fallback for tasks that need its operating-system
sandbox, nested agents, workflows, or Ralph/goal machinery.

DeepCode replaces the coding loop and terminal UI that Machinist would
otherwise have to assemble. Machinist retains only thin adapters for its
standard-input prompt contract, token accounting, and Herdr lifecycle. This is
an integration boundary, not a fork or a hand-built DeepSeek harness.

## Evidence from the Mac mini and DGX

Evaluation date: 2026-09-03. DeepCode source revision:
`b69e845ff159f7480ec698a8c6c76bfea010f209`; published package: 0.3.1.
Host: Darwin arm64, Node.js 22.22.3. Model: `ds-0731`, served by vLLM through
`http://127.0.0.1:18000/v1`.

| Check | DeepCode 0.3.1 | Official DeepSeek Harness | Result |
| --- | ---: | ---: | --- |
| Exact-response smoke | 5,264 input / 8 output tokens | 7,543 input / 7 output tokens | Both passed; DeepCode used 30.2% fewer input tokens |
| One-file write task | 16,427 input / 205 output tokens over 3 requests | 22,963 input / 191 output tokens over 3 requests | Both produced the exact 22-byte file; DeepCode used 28.5% fewer input tokens |
| Native terminal UI | Yes | No; requires the web UI or a separate TUI bundle | DeepCode is simpler for Herdr |
| One-shot non-interactive mode | `--exec --prompt` | Headless profile | Both work against the DGX |
| Warm task time in the one-file smoke | about 10.5 seconds | about 9.7 seconds | No meaningful speed winner from one sample |

These are compatibility smokes, not a statistically useful quality benchmark.
The DGX reports vLLM prefix caching enabled and a high aggregate local cache-hit
rate. Both harnesses use stable request prefixes, but DeepCode's smaller base
prompt leaves more of the context window for repository work.

## Capability comparison

| Capability | DeepCode 0.3.1 | Official DeepSeek Harness | Consequence for Machinist |
| --- | --- | --- | --- |
| Local OpenAI-compatible endpoint | Yes; verified with vLLM | Yes; verified | Either can use the DGX tunnel |
| Interactive work | Native Ink TUI | Web UI and headless; terminal UI is an added bundle | DeepCode removes a third-party TUI dependency |
| Unattended work | `--exec`, persisted status, resume/fork | Headless, durable sessions, goals | DeepCode is sufficient when Machinist owns orchestration |
| Tools | Small DeepSeek-tuned set with snippet-scoped edits | Larger tool catalog | DeepCode reduces prompt overhead and ambiguous edits |
| Skills and MCP | Yes | Yes | Existing portable skills can move across |
| Context management | Stable cache-aware prefix and automatic compaction | Token meter, pruning, spill, compaction | Both address long sessions |
| Token/KV observability | Usage and per-model cache fields in the session index | Usage events in durable session logs | The adapter feeds DeepCode totals to Machinist; vLLM remains the KV source |
| Permission model | Application policy based on declared side effects | Application policy plus local OS sandbox modes | Prefer DSH for untrusted code or strict isolation |
| Nested agents and workflows | Not built in | Built in | Machinist should fan out DeepCode jobs; use DSH when in-harness delegation is required |
| Machine-readable CLI result | Plain final text; no JSON flag | Durable structured event log | The adapter reads the new session's usage record |
| Herdr lifecycle hooks | None | Plugin event surface | DeepCode adapter observes its persisted status without reading messages |
| Platform support | macOS/Linux; Windows through Git Bash; Node 22+ | macOS/Linux/Windows with platform-specific shell tools | Compatible with the Mac mini; keep platform-specific wrappers |
| License | MIT | MIT | No license blocker |

## Risks and controls

- Pin `@vegamo/deepcode-cli@0.3.1`. It is a fast-moving third-party project;
  upgrade only after the DGX smoke, adapter tests, and a canary task pass.
- Disable DeepCode telemetry for the local profile. Its upstream default is
  enabled.
- DeepCode permissions are not an OS sandbox. On trusted repositories, deny
  writes and deletes outside the checkout and allow the remaining work so AFK
  runs do not stop. Use DSH or another sandboxed profile for untrusted inputs.
- The DeepCode web-search fallback may use an external service when the model
  endpoint is not the official DeepSeek API. Configure a local search command,
  explicitly allow the external service, or deny network access for private
  workloads.
- Binary or image input can rapidly inflate context. Keep multimodal support
  off for `ds-0731`, avoid reading binary files as text, and retain the 512K
  automatic-compaction threshold.
- DeepCode's CLI does not expose the new session ID directly. The process
  adapter snapshots the per-project index before launch and attributes the new
  entry after completion. A single Machinist worker runs one task at a time;
  avoid launching a second manual DeepCode process in the same checkout during
  a headless run.

## Cutover

1. Install and pin DeepCode 0.3.1 on the Mac mini; keep official DSH installed.
2. Add `dgx-deepcode` beside, not in place of, the current fallback profiles.
3. Verify text response, one file edit, token capture, Herdr state changes, and
   cancellation against the tunneled DGX endpoint.
4. Route ten representative trusted tasks to DeepCode. Compare acceptance,
   elapsed time, total input/output tokens, retries, manual interventions, and
   post-run corrections with the existing baseline.
5. Make `dgx-deepcode` first in the normal implementation route only if it does
   not regress acceptance or rework and reduces median tokens or elapsed time.
6. Keep DSH for high-isolation and in-harness multi-agent work, and keep Codex
   and Claude subscription profiles as capacity and model-quality fallbacks.

Rollback is a route-order change: remove `dgx-deepcode` from the route or move
it behind the fallback profile. DeepCode session history remains local and does
not alter Machinist's canonical job history.
