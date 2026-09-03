import { useEffect, useState } from "react";
import { Card } from "@/components/ui/card";
import { PageHeading, QuietState } from "@/components/ui/page-heading";
import { formatObservedNumber, observabilityView } from "@/observability-state";

export function ObservabilityPage() {
  const [result, setResult] = useState({ kind: "loading" });
  useEffect(() => {
    let stopped = false;
    let timer;
    const load = async () => {
      try {
        const response = await fetch("/api/v1/observability", { headers: { Accept: "application/json" } });
        if (!response.ok) throw new Error(`Observability request failed (${response.status})`);
        const value = observabilityView(await response.json());
        if (!stopped) setResult(value);
      } catch (error) {
        if (!stopped) setResult({ kind: "error", message: error.message });
      }
      if (!stopped) timer = window.setTimeout(load, 10_000);
    };
    load();
    return () => { stopped = true; window.clearTimeout(timer); };
  }, []);

  return <div className="mx-auto max-w-[1500px] space-y-6 p-4 sm:p-6 lg:p-8">
    <PageHeading title="Agents & infrastructure" description="Metadata-only agent, model, token, cache, and DGX telemetry. Shared infrastructure is never attributed without exact correlation." />
    {result.kind === "loading" ? <Card><QuietState title="Connecting to telemetry" description="Reading the local observability collector." role="status" /></Card>
      : result.kind === "disabled" ? <Card><QuietState title="Observability is disabled" description="Enable [observability] on the control plane and [telemetry] on workers." /></Card>
      : result.kind === "unavailable" || result.kind === "error" ? <div role="alert" className="rounded-md border border-warning/35 bg-warning/10 px-3 py-2 text-sm text-warning">{result.message}. Agent execution is unaffected.</div>
      : <ObservabilityDashboard view={result} />}
  </div>;
}

function ObservabilityDashboard({ view }) {
  const successRate = Number.isFinite(view.fleet.success_rate) ? `${(view.fleet.success_rate * 100).toFixed(1)}%` : "Unavailable";
  const providers = view.health.providers || {};
  return <>
    {view.message ? <div role="status" className="rounded-md border border-warning/35 bg-warning/10 px-3 py-2 text-sm text-warning">{view.message}</div> : null}
    <section className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4" aria-label="Fleet telemetry">
      <Metric label="Active agents" value={formatObservedNumber(view.fleet.active_agents)} detail={`${view.agents.length} observed agents`} />
      <Metric label="Turns" value={formatObservedNumber(view.fleet.turn_count)} detail={`${successRate} success`} />
      <Metric label="Fleet output" value={formatObservedNumber(view.model.output_tokens_per_second, " tok/s")} detail={`${view.model.exact_call_count || 0} exact model calls`} />
      <Metric label="Events" value={formatObservedNumber(view.health.events)} detail={`SQLite ${view.health.journal_mode || "unavailable"}`} />
    </section>

    <section className="grid gap-4 lg:grid-cols-2">
      <Card className="p-4 sm:p-5"><SectionTitle title="Tokens & prompt cache" detail="Provider-reported usage; unavailable values are not estimated." /><dl className="mt-4 grid grid-cols-2 gap-4"><Datum label="Input p50" value={formatObservedNumber(view.model.input_tokens?.p50, " tokens")} /><Datum label="Input p95" value={formatObservedNumber(view.model.input_tokens?.p95, " tokens")} /><Datum label="Prompt-cache reads" value={formatObservedNumber(view.model.cached_tokens?.sum, " tokens")} /><Datum label="Reasoning tokens" value={formatObservedNumber(view.model.reasoning_tokens?.sum, " tokens")} /><Datum label="TTFT p50" value={formatObservedNumber(view.model.ttft_ms?.p50, " ms")} /><Datum label="Call output p50" value={formatObservedNumber(view.model.per_call_output_tokens_per_second?.p50, " tok/s")} /></dl></Card>
      <Card className="p-4 sm:p-5"><SectionTitle title="DGX / model capacity" detail="Shared vLLM and hardware context, not per-agent attribution." /><dl className="mt-4 grid grid-cols-2 gap-4"><Datum label="Requests running" value={formatObservedNumber(view.capacity.running)} /><Datum label="Requests waiting" value={formatObservedNumber(view.capacity.waiting)} /><Datum label="Server KV cache" value={Number.isFinite(view.capacity.kvCache) ? `${(view.capacity.kvCache * 100).toFixed(1)}%` : "Unavailable"} /><Datum label="GPU utilization" value={formatObservedNumber(view.capacity.gpuUtilization, "%")} /><Datum label="GPU temperature" value={formatObservedNumber(view.capacity.gpuTemperature, "°")} /><Datum label="GPU power" value={formatObservedNumber(view.capacity.gpuPower, " W")} /></dl></Card>
    </section>

    <section><SectionTitle title="DGX and model nodes" detail="Latest per-node samples; data older than 45 seconds is marked stale." /><Card className="mt-3 overflow-hidden">{view.nodes.length ? view.nodes.map((node) => <div key={`${node.kind}:${node.id}`} className="grid gap-3 border-b border-border px-4 py-3 last:border-b-0 sm:grid-cols-[minmax(10rem,1.2fr)_minmax(7rem,1fr)_minmax(8rem,1fr)_minmax(8rem,1fr)_10rem] sm:items-center"><div><p className="truncate text-sm font-medium">{node.id}</p><p className="text-xs capitalize text-muted-foreground">{node.kind === "hardware" ? "DGX / GPU" : "Model endpoint"}</p></div><Datum label={node.kind === "hardware" ? "GPU utilization" : "Requests"} value={node.kind === "hardware" ? formatObservedNumber(node.gpuUtilization, "%") : `${formatObservedNumber(node.running)} running / ${formatObservedNumber(node.waiting)} waiting`} /><Datum label={node.kind === "hardware" ? "GPU memory" : "Server KV cache"} value={node.kind === "hardware" ? formatMemory(node.gpuMemoryUsedMiB, node.gpuMemoryTotalMiB) : Number.isFinite(node.kvCache) ? `${(node.kvCache * 100).toFixed(1)}%` : "Unavailable"} /><Datum label={node.kind === "hardware" ? "Temperature / power" : "Capacity source"} value={node.kind === "hardware" ? `${formatObservedNumber(node.gpuTemperature, "°")} / ${formatObservedNumber(node.gpuPower, " W")}` : "vLLM / compatible"} /><Datum label="Last sample" value={`${node.isStale ? "Stale · " : ""}${formatTime(node.lastSeenAt)}`} /></div>) : <QuietState title="No node samples observed" description="Configure vLLM, NVIDIA, or allowlisted JSON providers on the collector." />}</Card></section>

    <section><SectionTitle title="Agents" detail="Current harness/model state and last observation." /><Card className="mt-3 overflow-hidden">{view.agents.length ? view.agents.map((agent) => <div key={agent.id} className="grid gap-2 border-b border-border px-4 py-3 last:border-b-0 sm:grid-cols-[minmax(10rem,1fr)_minmax(8rem,1fr)_minmax(8rem,1fr)_9rem] sm:items-center"><div><p className="truncate text-sm font-medium">{agent.display_name || "Unknown agent"}</p><p className="truncate font-mono text-xs text-muted-foreground">{agent.id}</p></div><p className="text-sm">{agent.current_state || "unknown"}</p><p className="truncate font-mono text-xs">{[agent.harness, agent.model].filter(Boolean).join(" / ") || "Unavailable"}</p><time className="text-xs text-muted-foreground">{formatTime(agent.last_seen_at)}</time></div>) : <QuietState title="No agents observed" description="Connect an instrumented harness or widen the collector retention window." />}</Card></section>

    <section><SectionTitle title="Provider health" detail={`${view.samples.length} recent shared samples · ${view.turns.length} recent turns`} /><Card className="mt-3 p-4"><div className="flex flex-wrap gap-2">{Object.keys(providers).length ? Object.entries(providers).map(([name, provider]) => <span key={name} className="rounded-md border border-border bg-muted px-2 py-1 font-mono text-xs" title={provider.error || ""}>{name}: {provider.status || (provider.healthy ? "healthy" : "configured")}</span>) : <span className="text-sm text-muted-foreground">No infrastructure providers configured.</span>}</div></Card></section>
  </>;
}

function Metric({ label, value, detail }) { return <Card className="p-4 sm:p-5"><p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">{label}</p><p className="mt-2 text-2xl font-semibold tabular-nums">{value}</p><p className="mt-1 text-xs text-muted-foreground">{detail}</p></Card>; }
function Datum({ label, value }) { return <div><dt className="text-xs text-muted-foreground">{label}</dt><dd className="mt-1 text-sm font-medium tabular-nums">{value}</dd></div>; }
function SectionTitle({ title, detail }) { return <div><h2 className="text-sm font-semibold">{title}</h2><p className="mt-1 text-xs text-muted-foreground">{detail}</p></div>; }
function formatTime(value) { if (!value || !Number.isFinite(Date.parse(value))) return "Unavailable"; return new Date(value).toLocaleString(); }
function formatMemory(used, total) { return Number.isFinite(used) && Number.isFinite(total) ? `${used.toLocaleString()} / ${total.toLocaleString()} MiB` : "Unavailable"; }
