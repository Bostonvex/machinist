<p align="center">
  <img src=".github/assets/machinist-lockup.svg" width="520" alt="Machinist">
</p>

<p align="center">
  <strong>The open source AI software factory for agentic coders.</strong><br>
 Machinist is an open source software factory for repeatable and scalable AI coding workflows. 
</p>

<p align="center">
  <a href="https://machinist.sh">Website</a> ·
  <a href="docs/README.md">Documentation</a> ·
  <a href="docs/adaptive-agent-platform.md">Adaptive platform</a> ·
  <a href="docs/configuration.md">Configuration</a> ·
  <a href="examples/workflows/README.md">Workflow examples</a>
</p>

<p align="center">
  <img src=".github/site/technical-drawings.webp" width="100%" alt="Technical drawings of a milling machine, supervised coding-agent system, and precision linear assembly">
</p>

<p align="center"><sub>Machine section · supervised agent system · exploded assembly</sub></p>

Machinist is an open-source software factory implementation. It runs on your machine, keeps repository access and credentials local, and records the work from request to handoff. Commands can invoke Codex, Claude Code, another agent CLI, a test runner, a shell script, or repository-owned orchestration.

Please note: this is early access software and subject to change. 

## Adaptive agent platform release candidate

The Bostonvex `v0.5.0-rc.5` release extends Machinist into an
environment-aware, multi-harness control plane. A command can select an ordered
route of worker-local profiles instead of being coupled to one executable or
model. This keeps prompts, source, credentials, and subscription sessions on
the execution host while allowing the control plane to schedule and observe the
work.

| Capability | What is available |
| --- | --- |
| Harness and model routing | Per-command and per-role profiles for Codex, Claude Code, OpenCode, Pi, generic executables, and bounded custom identifiers such as DeepSeek or Aider |
| Cost and authentication | Subscription-backed CLIs, API-key providers, and local inference can be ordered in the same route; secret values never enter the control-plane catalog |
| Local and DGX models | OpenAI-compatible and CLI-mediated local endpoints, including a Mac control plane using one or more DGX Sparks as dedicated inference servers |
| Environment awareness | Automatic OS, architecture, execution mode, shell, path-style, and feature discovery, plus operator-controlled trust tags and compatibility admission |
| Recovery and cost bounds | Durable fenced attempts, normalized failure classes, explicit fallback policy, attempt limits, aggregate token ceilings, cancellation, and bounded lease-loss recovery |
| Unattended operation | Scheduled and repository-owned orchestration within fixed commands, repositories, profiles, environment requirements, retry policy, and token budgets |
| Dashboard and telemetry | Work and run state, workers, routes, agents, reported tokens, prompt cache, server KV cache, model endpoints, and NVIDIA/DGX health without placing high-volume telemetry in the job database |
| Platforms | Native macOS and Linux workers plus Windows `amd64`/`arm64` workers with Job Object process-tree cancellation; release archives are produced for all six targets |

The first paired shadow task completed with equivalent acceptance, one attempt,
33.4% lower elapsed time, and 66.0% fewer reported tokens than its historical
Buzz execution. That is encouraging but is only one pair: the project remains
in a staged pilot until the [cutover benchmark](benchmarks/README.md) has enough
representative tasks. See the
[Buzz/Agent Software Factory comparison](docs/buzz-asf-comparison.md),
[platform design and cutover plan](docs/adaptive-agent-platform.md), and
[first pilot report](benchmarks/pilot-2026-09-02.md) for the scope, tradeoffs,
evidence, and adoption criteria.

## Why Machinist

- **One controlled entrypoint.** Workers expose named commands and repositories, never arbitrary shell text or machine-local paths.
- **Bring your own harness.** Use any executable that accepts a prompt on standard input.
- **Keep authority local.** Repositories, credentials, model aliases, and executor configuration stay on the worker.
- **Inspect every run.** Stream output, retain durable events and artifacts, and track terminal outcomes, duration, and reported token use.
- **Bound unattended work.** Fixed catalogs, profile routes, environment requirements, retry policy, and budgets constrain scheduled or repository-owned automation.

<a id="quick-start"></a>

## Quick start

Build and initialize Machinist:

```sh
git clone https://github.com/Bostonvex/machinist.git
cd machinist
mkdir -p ./bin && go build -o ./bin/machinist ./cmd/machinist
./bin/machinist init
```

Configure an approved command:

```toml
# ~/.machinist/config.toml
[commands.foreman]
executor = "codex"
prompt_file = "prompts/foreman.md" # optional
timeout = "45m"
```

Run it directly:

```sh
./bin/machinist run --command=foreman --repo=/path/to/repo --prompt="Implement issue 42"
```

## How execution works

For a direct run, Machinist maps the configured command name to a fixed executable and uses the path supplied with `--repo` as the working directory. Managed submissions instead resolve an approved repository name from the worker configuration. In both cases, Machinist renders the prompt, sends it on standard input, streams stdout and stderr, and applies one overall timeout and cancellation. Exit code 0 succeeds; every non-zero exit code fails.

Scripts are intentionally opaque. Their internal stages appear in logs, but Machinist does not invent child runs, graphs, checkpoints, or resumable stages. A killed script restarts from the beginning unless the script owns checkpointing.

## Go deeper

| Guide | What it covers |
| --- | --- |
| [Documentation](docs/README.md) | Choose the right setup and operations guide |
| [Adaptive platform](docs/adaptive-agent-platform.md) | Architecture, multi-harness roadmap, safety model, dashboard, and cutover plan |
| [Buzz/ASF comparison](docs/buzz-asf-comparison.md) | Side-by-side feature matrix, tradeoffs, and switching recommendation |
| [Configuration](docs/configuration.md) | Commands, profiles, routes, harnesses, providers, models, workers, and repositories |
| [Observability](docs/observability.md) | Agents, tokens, prompt cache, KV cache, model endpoints, and DGX metrics |
| [Cutover benchmark](benchmarks/README.md) | Paired evidence schema, gates, and evaluation procedure |
| [Development](docs/development.md) | Build, test, and work on Machinist locally |
| [VM deployment](docs/vm-deployment.md) | Run the control plane and worker as services |
| [macOS + DGX deployment](docs/macos-deployment.md) | Run Machinist on a Mac with local models served by DGX Sparks |
| [Private fleet deployment](docs/fleet-deployment.md) | Operate a hub, remote workers, and multiple DGX/model endpoints |
| [Windows deployment](docs/windows-deployment.md) | Run native Windows workers with Job Object cancellation |
| [Workflow examples](examples/workflows/README.md) | Repository-owned multi-step orchestration |

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) for development setup and pull-request expectations. Security issues should follow [SECURITY.md](SECURITY.md).

Machinist is released under the [MIT License](LICENSE).
