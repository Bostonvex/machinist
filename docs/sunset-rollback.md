# Sunsetting the collector Machinist replaced

Phase F of the [migration plan](migration-plan.md) retires the services Machinist
absorbed. This is the record of what was retired, what was kept, and how to put
each piece back.

A sunset with no way back is not a sunset, it is a deletion that has not been
noticed yet. Everything below was left recoverable on purpose, and the cost of
that is a directory of backups nobody will read. That is the correct trade: the
day one of them is needed, it is needed at the moment the system is already
wrong.

## What was retired

`com.buzz.agent-observability` — the Python telemetry collector on `127.0.0.1:7900`.

Machinist serves the same ingest path and event schema (`machinist collector
start`, `[collector]`), enforced case-by-case from a recorded fixture by
`TestParityWithTheCollectorItReplaces` in `internal/telemetry/parity_test.go`.

It was retired only after the reads and the writes had moved and been observed
moving, not on the grounds that they should have:

- the control plane's `[observability]` URL, verified by
  `GET /api/v1/observability` returning `available: true` with populated
  provider rows;
- the worker's `[telemetry]` URL;
- the three herdr harness session configs under `~/.machinist/herdr-sessions/`,
  which were the last producers still naming the old port, and which the first
  pass of the cutover missed;
- both DGX Spark GPU nodes, which Machinist polls as separate providers
  (`nvidia-smi-remote:<node_id>`) rather than as the single node the old
  collector's fixed provider allowed.

## What was kept

**The record.** The retired collector's database was snapshotted, not deleted:

```
~/.machinist/archive/phase-f/buzz-agent-observability-telemetry-<UTC>.sqlite3
```

570,849 events, 15 agents, 395 turns, the last agent seen 2026-09-02. The
benchmark commands in `benchmarks/README.md` read this archive. They point at
the archive rather than at the path the service used to write, because a closed
record read as though it were live reports a system that stopped without saying
so.

**The service definition.** Its LaunchAgent was stopped, disabled, and moved
rather than removed:

```
~/Library/LaunchAgents/retired-phase-f/com.buzz.agent-observability.plist
```

**The secrets.** The ingest token and identity salt moved from
`~/.config/buzz-agent-observability/` into `~/.machinist/collector/` as the same
bytes. The originals are still in place. Copying rather than regenerating is not
laziness: producers hash their identities with the salt before sending anything,
so a new salt would silently rename every agent in the record, and the rename
would look like a fleet of new machines rather than like a configuration change.

**Every configuration edited.** Each file was backed up beside itself before it
was touched, tagged with what the edit was for:

```
~/.machinist/config.toml.before-phase-e-cutover-<UTC>
~/.machinist/worker.toml.before-canary-<UTC>
~/.machinist/{config,worker}.toml.before-secret-move-<UTC>
~/.machinist/{config,worker}.toml.before-port-7900-<UTC>
~/.machinist/herdr-sessions/*.toml.before-phase-f-<UTC>
```

## Rolling back

Restore the service:

```sh
mv ~/Library/LaunchAgents/retired-phase-f/com.buzz.agent-observability.plist \
   ~/Library/LaunchAgents/
launchctl enable  "gui/$(id -u)/com.buzz.agent-observability"
launchctl bootstrap "gui/$(id -u)" ~/Library/LaunchAgents/com.buzz.agent-observability.plist
```

It will bind `127.0.0.1:7900`, which Machinist's collector now holds, so move
Machinist's collector off that port first — `[collector] listen` — or whichever
starts second fails to bind and says so in its own stderr log.

Point the readers and writers back by restoring the backups, newest first, and
restarting:

```sh
launchctl kickstart -k "gui/$(id -u)/sh.machinist.collector"
launchctl kickstart -k "gui/$(id -u)/sh.machinist.worker"
launchctl kickstart -k "gui/$(id -u)/sh.machinist.control-plane"
```

Then check, rather than assume:

```sh
curl -fsS http://127.0.0.1:7331/api/v1/observability   # available, providers ok
curl -fsS http://127.0.0.1:7900/healthz                # whichever collector holds it
```

## Ports

Machinist's collector listens on `127.0.0.1:7900`, which is what `[collector]
listen` and `[telemetry] url` both default to in the code. It ran on `7902`
only for as long as the collector it replaces still held `7900`. A deployment
that overrides every default forever is a deployment where the defaults are
wrong for the only machine that runs it, so the override was removed rather
than documented.

## What Phase F has not retired

`com.buzz.factory-ticker`, `com.buzz.factory-kanban` and
`com.buzz.factory-webhook-receiver` are still running, and they are not
telemetry. They orchestrate live agent work, and stopping them would stop it.
See the Phase F tracking issue for the count and for which repositories.

`com.buzz.rsi-ticker` is unrelated to this migration and is out of scope.
