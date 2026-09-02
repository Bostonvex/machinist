export function observabilityView(payload) {
  if (!payload?.enabled) return { kind: "disabled" };
  if (!payload.available) return { kind: "unavailable", message: payload.error || "Collector unavailable" };
  const fleet = payload.summary?.fleet || {};
  const model = fleet.model_metrics || {};
  const infrastructure = fleet.infrastructure_metrics || {};
  const series = infrastructure.series || [];
  return {
    kind: "ready",
    health: payload.health || {},
    agents: payload.agents?.agents || [],
    turns: payload.turns?.turns || [],
    samples: payload.samples?.samples || [],
    fleet,
    model,
    infrastructure,
    capacity: {
      running: latest(series, (item) => item.metric_name === "requests_running"),
      waiting: latest(series, (item) => item.metric_name === "requests_waiting"),
      kvCache: latest(series, (item) => item.metric_name === "gpu_kv_cache_usage_ratio", "mean"),
      gpuUtilization: latest(series, (item) => item.scope === "hardware" && item.metric_name?.endsWith("utilization_percent"), "mean"),
      gpuMemory: latest(series, (item) => item.scope === "hardware" && item.metric_name?.includes("memory") && item.unit === "bytes"),
      gpuTemperature: latest(series, (item) => item.scope === "hardware" && item.metric_name?.includes("temperature"), "mean"),
      gpuPower: latest(series, (item) => item.scope === "hardware" && item.metric_name?.includes("power")),
    },
  };
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

