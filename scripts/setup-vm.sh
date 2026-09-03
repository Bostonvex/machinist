#!/usr/bin/env bash

set -euo pipefail

if [[ $(id -u) -ne 0 ]]; then
  echo "run this script as root" >&2
  exit 1
fi

machinist_version=${MACHINIST_VERSION:-}
if [[ ! $machinist_version =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
  echo "set MACHINIST_VERSION to the release being installed, such as v0.2.0" >&2
  exit 2
fi

machinist_repository=${MACHINIST_REPOSITORY:-owainlewis/machinist}
if [[ ! $machinist_repository =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
  echo "MACHINIST_REPOSITORY must be an owner/repository name" >&2
  exit 2
fi

machinist_node_role=${MACHINIST_NODE_ROLE:-all}
case $machinist_node_role in
  all | control-plane | worker) ;;
  *)
    echo "MACHINIST_NODE_ROLE must be all, control-plane, or worker" >&2
    exit 2
    ;;
esac

legacy_root_install=false
if [[ -d /root/.machinist ]]; then
  legacy_root_install=true
fi
for legacy_unit in machinist-control-plane.service machinist-worker.service; do
  if [[ -f /etc/systemd/system/$legacy_unit ]] && grep -q '^User=root$' "/etc/systemd/system/$legacy_unit"; then
    legacy_root_install=true
  fi
done
if [[ $legacy_root_install == true ]]; then
  echo "legacy root-based Machinist installation detected" >&2
  echo "follow the v0.1.x migration steps in docs/vm-deployment.md before running this bootstrap" >&2
  exit 3
fi

if [[ ! -r /etc/os-release ]]; then
  echo "unsupported Linux distribution: /etc/os-release is missing" >&2
  exit 1
fi
. /etc/os-release
if [[ ${ID:-} != ubuntu && ${ID:-} != debian ]]; then
  echo "unsupported Linux distribution: ${ID:-unknown}" >&2
  exit 1
fi

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y ca-certificates curl git gh jq openssh-client tar

runtime_user=machinist
if ! id "$runtime_user" >/dev/null 2>&1; then
  useradd --create-home --shell /bin/bash "$runtime_user"
fi
runtime_home=$(getent passwd "$runtime_user" | cut -d: -f6)
if [[ -z $runtime_home || ! -d $runtime_home ]]; then
  echo "could not determine home directory for $runtime_user" >&2
  exit 1
fi

# Codex and Claude Code are worker dependencies. A dedicated control-plane node
# does not receive model credentials or agent launchers.
if [[ $machinist_node_role != control-plane ]]; then
  runuser -u "$runtime_user" -- env HOME="$runtime_home" \
    bash -c 'curl -fsSL https://chatgpt.com/codex/install.sh | sh'
  runuser -u "$runtime_user" -- env HOME="$runtime_home" \
    bash -c 'curl -fsSL https://claude.ai/install.sh | bash'
fi
curl -fsSL "https://raw.githubusercontent.com/$machinist_repository/$machinist_version/install.sh" | \
  env MACHINIST_VERSION="$machinist_version" MACHINIST_REPOSITORY="$machinist_repository" sh

if [[ $machinist_node_role != control-plane ]]; then
  # Standalone agent installers use ~/.local/bin. Login shells commonly add
  # that directory to PATH, but services and other non-interactive processes do not.
  for agent_command in codex claude; do
    agent_path="$runtime_home/.local/bin/$agent_command"
    if [[ ! -x "$agent_path" ]]; then
      echo "$agent_command installer did not create $agent_path" >&2
      exit 1
    fi
    ln -sfn "$agent_path" "/usr/local/bin/$agent_command"
  done
fi

runuser -u "$runtime_user" -- env HOME="$runtime_home" machinist init

worker_was_enabled=false
worker_was_active=false
if systemctl is-enabled --quiet machinist-worker.service 2>/dev/null; then
  worker_was_enabled=true
fi
if systemctl is-active --quiet machinist-worker.service 2>/dev/null; then
  worker_was_active=true
fi

service_base_url="https://raw.githubusercontent.com/$machinist_repository/$machinist_version/deploy/systemd"
service_tmp_dir=$(mktemp -d)
trap 'rm -rf "$service_tmp_dir"' EXIT
for service in machinist-control-plane.service machinist-worker.service machinist-fleet-tunnel@.service; do
  curl -fsSL "$service_base_url/$service" -o "$service_tmp_dir/$service"
  install -m 0644 "$service_tmp_dir/$service" "/etc/systemd/system/$service"
done
systemctl daemon-reload

if [[ $machinist_node_role == all || $machinist_node_role == control-plane ]]; then
  systemctl enable machinist-control-plane.service
  systemctl restart machinist-control-plane.service
else
  systemctl disable --now machinist-control-plane.service
fi

if [[ $machinist_node_role == control-plane ]]; then
  systemctl disable --now machinist-worker.service
elif runuser -u "$runtime_user" -- env HOME="$runtime_home" machinist worker validate --help >/dev/null 2>&1; then
  if runuser -u "$runtime_user" -- env HOME="$runtime_home" machinist worker validate >/dev/null 2>&1; then
    systemctl enable machinist-worker.service
    systemctl restart machinist-worker.service
  else
    systemctl disable --now machinist-worker.service
  fi
else
  if [[ $worker_was_active == true ]]; then
    if [[ $worker_was_enabled == true ]]; then
      systemctl enable machinist-worker.service
    else
      systemctl disable machinist-worker.service
    fi
    systemctl restart machinist-worker.service
    echo "installed Machinist release does not support worker validation; restored the previously active worker" >&2
  else
    if [[ $worker_was_enabled == true ]]; then
      systemctl enable machinist-worker.service
      systemctl stop machinist-worker.service
      echo "installed Machinist release does not support worker validation; preserved the enabled but inactive worker" >&2
    else
      systemctl disable --now machinist-worker.service
    fi
  fi
fi

printf '\nVM bootstrap complete.\nNode role: %s\nRelease source: %s\n\n' \
  "$machinist_node_role" "$machinist_repository"
if [[ $machinist_node_role == control-plane ]]; then
  cat <<'EOF'
Next steps:
  1. Configure ~/.machinist/config.toml as the `machinist` user.
  2. Keep the listener on 127.0.0.1 and check `systemctl status machinist-control-plane`.
  3. Reach the dashboard through an authenticated private tunnel:
     ssh -N -L 7331:127.0.0.1:7331 machinist
EOF
else
  cat <<'EOF'
Next steps:
  1. Run `su - machinist`, then authenticate only the harnesses this worker uses.
  2. Run `gh auth login` when repository workflows use the GitHub CLI.
  3. Run `codex` and/or `claude` once when their subscription profiles are enabled.
  4. Clone approved repositories and register their absolute paths in
     ~/.machinist/worker.toml.
  5. For a remote control plane, follow docs/fleet-deployment.md before enabling the worker.
  6. Exit back to root and run `systemctl enable --now machinist-worker`.
  7. Check `systemctl status machinist-worker` and, when used, the fleet tunnel.
EOF
fi
