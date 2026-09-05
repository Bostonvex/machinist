# Observability

Machinist serves the telemetry collector that its own observability reads from.
`machinist collector start` runs a collector that listens on a loopback address,
accepts events, keeps them in its own telemetry database under its own
retention, and answers the queries the control plane and the built-in UI read.
The collector is configured under `[collector]` in the Machinist config. Work
remains operational when the collector is stopped or unreachable — telemetry
fails open rather than blocking anything that runs.

There are two settings that name a collector, and they say different things:

- `[collector]` is where Machinist *serves* one — the collector it starts with
  `machinist collector start`.
- `[observability]` is where the control plane *reads* one from, and it can
  point at Machinist's own collector or at another collector on the loopback.

A deployment can do either without the other.

## Serving the collector (`[collector]`)

Start Machinist's collector:

```
machinist collector start
```

The `[collector]` block in `examples/config.toml` is the way to configure it:

```toml
[collector]
enabled = true
listen = "127.0.0.1:7900"
database = "~/.machinist/collector/telemetry.db"
token_file = "~/.machinist/collector/ingest-token"
identity_salt_file = "~/.machinist/collector/identity-salt"
retention = "168h"
provider_interval = "10s"
```

Both secret files are required and are created on first start. The salt is
never read by the collector: producers hash their identities with it before
they send anything. The collector's database is deliberately not the control
plane's database — telemetry is a high-volume append-only stream with its own
retention, and sharing the transactional control-plane database would sweep and
back up the two as one thing.

The collector listens on loopback only. It serves a `/healthz` endpoint and the
ingest endpoint at `http://127.0.0.1:7900/api/v1/events`, which is the only
path that accepts events.

Optional shared-infrastructure pollers. What has to hold is that every polled
thing has a name and no two share it: the name keys the provider's status row,
and two things under one name produce a failure nobody can attribute. So
`[collector.vllm]`, `[collector.nvidia]` and `[collector.proxy]` appear at most
once each, while `[[collector.nvidia_remote]]` repeats once per remote node —
a deployment reaches the number of GPU nodes it reaches, and a node nobody
polls is indistinguishable from a node that is idle:

```toml
[collector.vllm]            # one vLLM server's Prometheus metrics
metrics_url = "http://127.0.0.1:18000/metrics"
endpoint_id = "vllm-primary"

[collector.nvidia]          # this machine's GPUs
node_id = "local-nvidia"

[[collector.nvidia_remote]] # another node's, over strict-host-verified SSH
node_id = "spark-0e9f"
ssh_host = "spark-0e9f"

[[collector.nvidia_remote]]
node_id = "spark-27c2"
ssh_host = "spark-27c2"

# The model proxy. It runs as its own process, in front of one endpoint, and
# measures every call the harness makes through it. Point the harness's base URL
# at `listen` instead of `upstream`, and it needs no other change.
[collector.proxy]
listen = "127.0.0.1:7901"
upstream = "http://127.0.0.1:18000"
model = "ds-0731"
endpoint_id = "vllm-primary"
context_token_file = "~/.machinist/collector/proxy-context-token"
```

`node_id` is the name, and the config refuses a collision across the local and
remote tables together. Past one remote node it must be given explicitly rather
than defaulted, because `remote-nvidia` is a name for *the* remote node and
stops being one as soon as there are two. Each remote node reports itself as
`nvidia-smi-remote:<node_id>` in `/healthz`, so an operator reading a failing
poller can tell which machine stopped answering. See
[Configuration](configuration.md) for the full reference.

## Producing events (`[telemetry]`)

Enable the producer on a worker with the `[telemetry]` block. The token and
identity-salt file paths are read by the producer and never by Machinist — the
contents are handed to the instrumented harness, which uses the token to
authenticate to the collector and the salt to hash its identity. Their paths are
whatever the collector's `token_file` and `identity_salt_file` are configured
to:

```toml
[telemetry]
enabled = true
url = "http://127.0.0.1:7900/api/v1/events"
token_file = "~/.machinist/collector/ingest-token"
identity_salt_file = "~/.machinist/collector/identity-salt"
endpoint_id = "dgx-primary"
```

The URL is deliberately restricted to the collector's literal loopback event
endpoint. Remote workers should forward events through a separately secured
transport rather than exposing the collector listener. See
[Private multi-host fleet deployment](fleet-deployment.md) for the verified SSH
tunnel topology used by Linux workers.

## The control-plane dashboard bridge (`[observability]`)

The control plane never ingests or stores telemetry; it reads a collector and
renders what it gets. Enable the same-origin, read-only dashboard bridge:

```toml
[observability]
enabled = true
url = "http://127.0.0.1:7900"
```

This may point at Machinist's own collector (the loopback listener `machinist
collector start` binds) or at another collector serving the same API on the
loopback.

Open the **Agents & infra** page in the Machinist UI. It shows fleet and agent
state, exact model throughput and TTFT coverage, vLLM running/waiting requests,
server KV cache, NVIDIA/DGX utilisation, temperature and power, and provider
health. Hardware `node_id` and model `endpoint_id` values remain separate in a
per-node view; samples older than 45 seconds are marked stale instead of being
shown as a healthy zero. The control plane requests fixed read-only JSON
endpoints, keeps the collector's heavier summary off the interactive request
path, and continues to show available live data if one optional view times out.
It rejects redirects and non-loopback destinations, caps each response, and
never proxies browser-chosen URLs or collector write endpoints.

## Guarantees

- **Fail-open.** Work stays operational when the collector is unreachable; a
  slow or broken collector costs freshness on the observability page, not the
  run it describes.
- **Metadata only.** Events carry no prompt, response, reasoning, source,
  filesystem path, arbitrary environment value, credential, or tool payload.
  The instrumented harness receives only the collector URL, the token and salt
  file paths, and the endpoint identifier, plus bounded correlation metadata:
  `MACHINIST_JOB_ID`, `MACHINIST_RUN_ID`, and `MACHINIST_ATTEMPT_ID`; command,
  role, route, profile, harness, provider, and model identifiers; and worker
  instance and environment digest.
- **Loopback only.** The collector listens on `127.0.0.1` and nowhere else, and
  the control plane refuses observability URLs that are not the literal
  loopback origin. Reaching the collector from another host is an SSH tunnel, a
  deliberate act.
- **Privilege separation.** The ingest endpoint demands a token of at least 32
  characters, and the collector never reads the identity-salt contents.

Prompt-cache token counters and inference-server KV-cache utilisation are
different signals and must never be combined. GPU/vLLM samples are shared
infrastructure context unless exact request correlation proves otherwise.

## Compatibility with the collector being retired

The collector Machinist serves matches the ingest path and event schema of the
collector it replaces, so existing observers keep working. The producer emits
`BUZZ_TELEMETRY_*` aliases alongside `MACHINIST_TELEMETRY_*` for compatibility.
Parity with the retired collector's validator is enforced case-by-case, from a
recorded fixture, by `TestParityWithTheCollectorItReplaces`.
