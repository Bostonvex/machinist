# Documentation

Machinist can be used as a small local wrapper, a durable unattended control
plane, or an interactive agent factory with Herdr. Start with the path that
matches the way you want to work.

## Start here

| Goal | Read this |
| --- | --- |
| Run one named command locally | [Root quick start](../README.md#quick-start) |
| Configure commands, profiles, models, routes, and repositories | [Configuration and migration](configuration.md) |
| Watch and steer an agent in an editable terminal | [Herdr interactive integration](herdr.md) |
| Add repository-owned multi-step automation | [Workflow examples](../examples/workflows/README.md) |

## Operate Machinist

- [macOS and DGX Spark deployment](macos-deployment.md) — Mac control plane,
  LaunchAgents, persistent DGX tunnel, local models, and the Herdr plugin.
- [VM deployment](vm-deployment.md) — system services on a single Linux host.
- [Private multi-host fleet deployment](fleet-deployment.md) — one hub, remote
  workers, and multiple model or DGX endpoints.
- [Windows deployment](windows-deployment.md) — native workers and process-tree
  cancellation.
- [Observability bridge](observability.md) — agents, tokens, prompt cache, model
  KV cache, GPU health, and the failure-isolated telemetry collector.

## Understand and evaluate the platform

- [Architecture](../ARCHITECTURE.md)
- [Adaptive agent platform design](adaptive-agent-platform.md)
- [Buzz/ASF comparison and cutover assessment](buzz-asf-comparison.md)
- [Cutover benchmark](../benchmarks/README.md)
- [Development and verification](development.md)
