# Deploy a private multi-host Machinist fleet

This guide separates a central control plane and observability collector from
one or more Linux workers. It preserves Machinist's loopback security boundary:
ports 7331 and 7900 are never exposed on a LAN or the public internet.

The recommended production shape is:

```text
operator --verified SSH--> hub 127.0.0.1:7331 (control plane + dashboard)
                              127.0.0.1:7900 (observability collector)
                                  ^
                                  | verified outbound SSH local forwards
                      +-----------+-----------+
                      |                       |
                 worker / DGX A          worker / DGX B
                 local :7331/:7900       local :7331/:7900
```

Each worker keeps its harness subscriptions, API keys, repository credentials,
and local-model access. The hub stores the durable queue and aggregate metadata.
Use a private overlay instead of SSH forwards only when it supplies equivalent
mutual authentication, encryption, least-privilege firewall rules, and a stable
private address. Never bind the current unauthenticated UI or collector to a
non-loopback address.

## 1. Record and approve the deployment inventory

Before changing a host, record:

- the hub SSH alias and every worker SSH alias;
- host OS and architecture;
- the pinned `OWNER/REPOSITORY` and release tag;
- the repositories and harness profiles allowed on each worker;
- a unique worker name, telemetry `endpoint_id`, and identity-salt file;
- the canary worker and the prior release used for rollback.

Do not infer these values from hostnames. Run the canary on one worker before
expanding to the rest of the fleet.

## 2. Bootstrap the roles

On the hub, review and run the pinned release bootstrap with:

```sh
MACHINIST_VERSION=VERSION \
MACHINIST_REPOSITORY=OWNER/REPOSITORY \
MACHINIST_NODE_ROLE=control-plane \
bash scripts/setup-vm.sh
```

On each worker, use `MACHINIST_NODE_ROLE=worker`. The role-aware bootstrap
installs all three systemd unit files, but it never enables an unvalidated
worker and a control-plane-only host receives no coding harnesses.

## 3. Provision the shared worker token without printing it

`machinist init` creates the hub token at
`/home/machinist/.machinist/server/worker.token`. Copy that file to the same
path on each worker over an authenticated administrative channel. Keep mode
`0600`, owner `machinist:machinist`, and never paste the value into a command,
log, issue, or chat.

On every worker, configure:

```toml
[control_plane]
url = "http://127.0.0.1:7331"
token_file = "~/.machinist/server/worker.token"
```

The HTTP URL is safe here only because the connection terminates on a local SSH
forward. Machinist requires HTTPS for a directly addressed non-loopback server.

## 4. Create a tunnel-only account and verified SSH alias

Create a distinct unprivileged account on the hub. Install one worker-specific
public key in that account's `authorized_keys`. A minimal key option for the two
local forwards is:

```text
restrict,port-forwarding,permitopen="127.0.0.1:7331",permitopen="127.0.0.1:7900" ssh-ed25519 PUBLIC_KEY worker-a-tunnel
```

Confirm the exact options supported by the hub's OpenSSH version. Give each
worker its own key so one can be revoked independently. Do not use
`StrictHostKeyChecking=no`, accept-new, an unverified `known_hosts` entry, agent
forwarding, or the worker's GitHub key.

As the `machinist` user on the worker, verify the hub host key out of band and
add an alias to `~/.ssh/config`:

```sshconfig
Host machinist-hub
  HostName <HUB_PRIVATE_NAME_OR_ADDRESS>
  User <TUNNEL_ONLY_USER>
  IdentityFile ~/.ssh/id_ed25519_machinist_tunnel
  IdentitiesOnly yes
  BatchMode yes
  StrictHostKeyChecking yes
```

Test without weakening verification:

```sh
ssh -NT -o ExitOnForwardFailure=yes \
  -L 127.0.0.1:7331:127.0.0.1:7331 \
  -L 127.0.0.1:7900:127.0.0.1:7900 \
  machinist-hub
```

## 5. Bind the worker lifecycle to the tunnel

Enable the supplied instance unit, then add a worker drop-in:

```sh
systemctl enable --now machinist-fleet-tunnel@machinist-hub.service
systemctl edit machinist-worker.service
```

Use:

```ini
[Unit]
Requires=machinist-fleet-tunnel@machinist-hub.service
After=machinist-fleet-tunnel@machinist-hub.service
```

The base worker unit intentionally does not require a local control plane. The
drop-in prevents a remote worker from starting until both forwards are active.

## 6. Configure telemetry and per-node identity

Run one central collector on hub loopback with `machinist collector start`,
and configure the Machinist control plane's `[observability]` URL as
`http://127.0.0.1:7900`. `[collector]` is where the hub *serves* one;
`[observability]` is where the control plane *reads* one. Copy the collector
ingest-token file securely to each worker; create a different identity-salt
file on every worker. Then configure:

```toml
[telemetry]
enabled = true
url = "http://127.0.0.1:7900/api/v1/events"
token_file = "~/.machinist/collector/ingest-token"
identity_salt_file = "~/.machinist/collector/identity-salt"
endpoint_id = "worker-a"
```

Infrastructure providers run beside the central collector. Configure a unique
hardware `node_id` for each DGX and a unique model `endpoint_id` for each vLLM
server. Repeat `[[collector.nvidia_remote]]` once per DGX; the config refuses
two nodes under one `node_id`, because which machine a reading describes is the
whole content of the reading.
Those read-only probes need a separately authenticated private path from the
hub to each DGX. Do not reuse the telemetry ingest key or tunnel account for
hardware shell access. If that path is not approved, leave infrastructure
metrics unavailable; agent execution and event telemetry continue fail-open.

The dashboard preserves each hardware `node_id` and model `endpoint_id`, and
marks a node stale after 45 seconds without a new sample. Prompt-cache token
counters and server KV-cache utilisation remain separate.

## 7. Validate and canary

On the canary worker:

```sh
su - machinist -c 'machinist worker validate'
systemctl restart machinist-worker.service
systemctl is-active machinist-fleet-tunnel@machinist-hub.service machinist-worker.service
journalctl -u machinist-worker.service -n 100 --no-pager
```

From the operator machine, reach the hub dashboard through a separate verified
SSH local forward and open `http://127.0.0.1:7331`:

```sh
ssh -NT -L 7331:127.0.0.1:7331 <HUB_ADMIN_ALIAS>
```

Submit one narrow canary task. Verify poll, lease, completion, cancellation,
token totals, retry fencing, worker environment, per-node freshness, and
collector-unavailable fail-open behavior before enabling another worker.

## Roll back

Stop new admissions, then drain or cancel active attempts from the dashboard.
On each worker:

```sh
systemctl disable --now machinist-worker.service
systemctl disable --now machinist-fleet-tunnel@machinist-hub.service
machinist update --version PREVIOUS_VERSION
```

Restore the prior worker configuration and restart only after
`machinist worker validate` succeeds. On the hub, back up the SQLite database
before upgrading. Restore the prior binary and configuration; restore the
pre-deploy database copy only when the new schema is not backward-readable.
Keep Buzz/ASF available until the paired cutover gates pass.
