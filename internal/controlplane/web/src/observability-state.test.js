import assert from "node:assert/strict";
import test from "node:test";
import { infrastructureNodes, observabilityView } from "./observability-state.js";

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

test("observability view remains useful while the heavy summary is warming", () => {
  const observedAt = new Date().toISOString();
  const view = observabilityView({
    enabled: true, available: true, error: "summary warming",
    health: { events: 10 },
    agents: { agents: [{ id: "busy", current_turn_id: "turn-1" }] },
    turns: { turns: [{ outcome: "completed" }, { outcome: "failed" }] },
    samples: { samples: [
      { event_type: "hardware.sample", observed_at: observedAt, attributes: { node_id: "spark-a", metric_name: "gpu.0.utilization_percent", value: 50 } },
      { event_type: "server.sample", observed_at: observedAt, endpoint_id: "vllm-a", attributes: { metric_name: "requests_running", value: 1 } },
    ] },
  });
  assert.equal(view.kind, "ready");
  assert.equal(view.message, "summary warming");
  assert.equal(view.fleet.active_agents, 1);
  assert.equal(view.fleet.turn_count, 2);
  assert.equal(view.fleet.success_rate, 0.5);
  assert.equal(view.capacity.gpuUtilization, 50);
  assert.equal(view.capacity.running, 1);
});

test("infrastructure nodes preserve separate DGX and model endpoint identities", () => {
  const nodes = infrastructureNodes([
    { event_type: "hardware.sample", observed_at: "2026-09-02T12:00:00Z", attributes: { node_id: "spark-a", metric_name: "gpu.0.utilization_percent", value: 40, unit: "percent" } },
    { event_type: "hardware.sample", observed_at: "2026-09-02T12:00:01Z", attributes: { node_id: "spark-a", metric_name: "gpu.0.utilization_percent", value: 60, unit: "percent" } },
    { event_type: "hardware.sample", observed_at: "2026-09-02T12:00:01Z", attributes: { node_id: "spark-a", metric_name: "gpu.0.memory_used_mib", value: 1200, unit: "MiB" } },
    { event_type: "hardware.sample", observed_at: "2026-09-02T12:00:01Z", attributes: { node_id: "spark-a", metric_name: "gpu.0.memory_total_mib", value: 2400, unit: "MiB" } },
    { event_type: "hardware.sample", observed_at: "2026-09-02T12:00:01Z", attributes: { node_id: "spark-b", metric_name: "gpu.0.utilization_percent", value: 25, unit: "percent" } },
    { event_type: "server.sample", observed_at: "2026-09-02T12:00:02Z", endpoint_id: "vllm-a", attributes: { metric_name: "requests_running", value: 2, unit: "requests" } },
    { event_type: "server.sample", observed_at: "2026-09-02T12:00:02Z", endpoint_id: "vllm-a", attributes: { metric_name: "gpu_kv_cache_usage_ratio", value: 0.7, unit: "ratio" } },
  ], Date.parse("2026-09-02T12:01:00Z"));
  assert.deepEqual(nodes.map((node) => node.id), ["spark-a", "spark-b", "vllm-a"]);
  assert.equal(nodes[0].gpuUtilization, 60);
  assert.equal(nodes[0].gpuMemoryUsedMiB, 1200);
  assert.equal(nodes[0].gpuMemoryTotalMiB, 2400);
  assert.equal(nodes[1].gpuUtilization, 25);
  assert.equal(nodes[2].running, 2);
  assert.equal(nodes[2].kvCache, 0.7);
  assert.equal(nodes[0].isStale, true);
  assert.equal(nodes[2].isStale, true);
});
