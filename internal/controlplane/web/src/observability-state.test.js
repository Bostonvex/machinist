import assert from "node:assert/strict";
import test from "node:test";
import { observabilityView } from "./observability-state.js";

test("observability view keeps prompt and KV cache metrics separate", () => {
  const view = observabilityView({
    enabled: true, available: true,
    summary: { fleet: {
      model_metrics: { input_tokens: { p50: 100 }, output_tokens_per_second: 22 },
      infrastructure_metrics: { series: [
        { metric_name: "gpu_kv_cache_usage_ratio", latest: 0.5 },
        { metric_name: "requests_waiting", latest: 2 },
      ] },
    } },
    agents: { agents: [{ id: "agent-1" }] }, turns: { turns: [] }, samples: { samples: [] }, health: { status: "ok" },
  });
  assert.equal(view.kind, "ready");
  assert.equal(view.capacity.kvCache, 0.5);
  assert.equal(view.model.input_tokens.p50, 100);
  assert.equal(view.agents.length, 1);
});

test("observability view distinguishes disabled and unavailable", () => {
  assert.equal(observabilityView({ enabled: false }).kind, "disabled");
  assert.equal(observabilityView({ enabled: true, available: false }).kind, "unavailable");
});

