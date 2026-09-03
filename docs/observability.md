# Observability bridge

Machinist integrates with the separately versioned
`buzz-agent-observability` collector instead of copying its high-frequency
storage into the control-plane database. Work remains operational when the
collector is stopped or unreachable.

Enable the bridge on a worker:

```toml
[telemetry]
enabled = true
url = "http://127.0.0.1:7900/api/v1/events"
token_file = "~/.config/buzz-agent-observability/ingest-token"
identity_salt_file = "~/.config/buzz-agent-observability/identity-salt"
endpoint_id = "dgx-primary"
```

Enable the same-origin, read-only dashboard bridge on the control plane:

```toml
[observability]
enabled = true
url = "http://127.0.0.1:7900"
```

Open the **Agents & infra** page in the Machinist UI. It shows fleet and agent
state, exact model throughput and TTFT coverage, vLLM running/waiting requests,
server KV cache, NVIDIA/DGX utilisation, temperature and power, and provider
health. Hardware `node_id` and model `endpoint_id` values remain separate in a
per-node view; samples older than 45 seconds are marked stale instead of being
shown as a healthy zero. The bridge requests fixed read-only JSON endpoints,
keeps the collector's heavier summary off the interactive request path, and
continues to show available live data if one optional view times out. It rejects redirects and
non-loopback destinations, caps each response, and never proxies browser-chosen
URLs or collector write endpoints.

The URL is intentionally restricted to the collector's literal loopback event
endpoint. Remote workers should forward events through a separately secured
transport rather than exposing the collector listener. See
[Private multi-host fleet deployment](fleet-deployment.md) for the verified SSH
tunnel topology used by Linux workers.

Instrumented child harnesses receive provider-neutral
`MACHINIST_TELEMETRY_*` variables and compatibility `BUZZ_TELEMETRY_*`
aliases. Values are limited to the fixed collector URL, token and salt file
paths, and the endpoint identifier. Machinist never reads the token or salt.

The execution also receives bounded correlation metadata:

- `MACHINIST_JOB_ID`, `MACHINIST_RUN_ID`, and `MACHINIST_ATTEMPT_ID`;
- command, role, route, profile, harness, provider, and model identifiers;
- worker instance and environment digest.

These fields contain no prompt, response, reasoning, source, filesystem path,
arbitrary environment value, credential, or tool payload. Existing collector
schema-v1 observers can operate through the Buzz aliases immediately. A future
collector schema adds the Machinist correlation fields to the event envelope;
until then, its existing session/turn/span IDs remain the correlation source.

Prompt-cache token counters and inference-server KV-cache utilisation are
different signals and must never be combined. GPU/vLLM samples are shared
infrastructure context unless exact request correlation proves otherwise.
