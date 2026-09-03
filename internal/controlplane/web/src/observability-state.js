export function observabilityView(payload) {
  if (!payload?.enabled) return { kind: "disabled" };
  if (!payload.available) return { kind: "unavailable", message: payload.error || "Collector unavailable" };
  const agents = payload.agents?.agents || [];
  const turns = payload.turns?.turns || [];
  const samples = payload.samples?.samples || [];
  const fleet = { ...summarizeFleet(agents, turns), ...(payload.summary?.fleet || {}) };
  const model = fleet.model_metrics || {};
  const infrastructure = fleet.infrastructure_metrics || {};
  const series = infrastructure.series || [];
  const nodes = infrastructureNodes(samples);
  const currentHardware = nodes.filter((node) => node.kind === "hardware" && !node.isStale);
  const currentModels = nodes.filter((node) => node.kind === "model" && !node.isStale);
  return {
    kind: "ready",
    message: payload.error || "",
    health: payload.health || {},
    agents,
    turns,
    samples,
    fleet,
    model,
    infrastructure,
    nodes,
    capacity: {
      running: observedOr(latest(series, (item) => item.metric_name === "requests_running"), total(currentModels.map((node) => node.running).filter(Number.isFinite))),
      waiting: observedOr(latest(series, (item) => item.metric_name === "requests_waiting"), total(currentModels.map((node) => node.waiting).filter(Number.isFinite))),
      kvCache: observedOr(latest(series, (item) => item.metric_name === "gpu_kv_cache_usage_ratio", "mean"), average(currentModels.map((node) => node.kvCache).filter(Number.isFinite))),
      gpuUtilization: observedOr(latest(series, (item) => item.scope === "hardware" && item.metric_name?.endsWith("utilization_percent"), "mean"), average(currentHardware.map((node) => node.gpuUtilization).filter(Number.isFinite))),
      gpuMemory: latest(series, (item) => item.scope === "hardware" && item.metric_name?.includes("memory") && item.unit === "bytes"),
      gpuTemperature: observedOr(latest(series, (item) => item.scope === "hardware" && item.metric_name?.includes("temperature"), "mean"), average(currentHardware.map((node) => node.gpuTemperature).filter(Number.isFinite))),
      gpuPower: observedOr(latest(series, (item) => item.scope === "hardware" && item.metric_name?.includes("power")), total(currentHardware.map((node) => node.gpuPower).filter(Number.isFinite))),
    },
  };
}

function summarizeFleet(agents, turns) {
  const completed = turns.filter((turn) => ["completed", "succeeded", "success"].includes(turn?.outcome)).length;
  return {
    active_agents: agents.filter((agent) => agent?.current_turn_id).length,
    turn_count: turns.length,
    success_rate: turns.length ? completed / turns.length : undefined,
  };
}

function observedOr(primary, fallback) {
  return Number.isFinite(primary) ? primary : fallback;
}

export function infrastructureNodes(samples, nowMillis = Date.now()) {
  const nodes = new Map();
  for (const sample of samples) {
    const attributes = sample?.attributes || {};
    const kind = sample?.event_type === "hardware.sample" ? "hardware" : sample?.event_type === "server.sample" ? "model" : "";
    const id = kind === "hardware" ? attributes.node_id : sample?.endpoint_id;
    const metric = attributes.metric_name;
    const value = Number(attributes.value);
    const observed = Date.parse(sample?.observed_at);
    if (!kind || typeof id !== "string" || !id || typeof metric !== "string" || !metric || !Number.isFinite(value) || !Number.isFinite(observed)) continue;
    const key = `${kind}:${id}`;
    const node = nodes.get(key) || { id, kind, lastSeenAt: "", lastSeenMillis: -Infinity, metrics: new Map() };
    if (observed > node.lastSeenMillis) {
      node.lastSeenAt = sample.observed_at;
      node.lastSeenMillis = observed;
    }
    const current = node.metrics.get(metric);
    if (!current || observed > current.observed) node.metrics.set(metric, { value, observed, unit: attributes.unit });
    nodes.set(key, node);
  }
  return [...nodes.values()].map((node) => {
    const values = [...node.metrics.entries()];
    const matching = (predicate) => values.filter(([name]) => predicate(name)).map(([, item]) => item.value);
    return {
      id: node.id,
      kind: node.kind,
      lastSeenAt: node.lastSeenAt,
      isStale: nowMillis - node.lastSeenMillis > 45_000,
      gpuUtilization: average(matching((name) => name.endsWith("utilization_percent"))),
      gpuTemperature: average(matching((name) => name.includes("temperature"))),
      gpuPower: total(matching((name) => name.includes("power"))),
      gpuMemoryUsedMiB: total(matching((name) => name.endsWith("memory_used_mib"))),
      gpuMemoryTotalMiB: total(matching((name) => name.endsWith("memory_total_mib"))),
      running: total(matching((name) => name === "requests_running")),
      waiting: total(matching((name) => name === "requests_waiting")),
      kvCache: average(matching((name) => name === "gpu_kv_cache_usage_ratio")),
    };
  }).sort((left, right) => left.id.localeCompare(right.id) || left.kind.localeCompare(right.kind));
}

function average(values) {
  return values.length ? values.reduce((sum, value) => sum + value, 0) / values.length : undefined;
}

function total(values) {
  return values.length ? values.reduce((sum, value) => sum + value, 0) : undefined;
}

export function latest(series, predicate, strategy = "sum") {
  const values = series.filter(predicate).map((item) => Number(item.latest)).filter(Number.isFinite);
  if (!values.length) return undefined;
  if (strategy === "mean") return values.reduce((total, value) => total + value, 0) / values.length;
  return values.reduce((total, value) => total + value, 0);
}

export function formatObservedNumber(value, suffix = "") {
  return Number.isFinite(value) ? `${value.toLocaleString(undefined, { maximumFractionDigits: 1 })}${suffix}` : "Unavailable";
}
