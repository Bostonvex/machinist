#!/usr/bin/env bash

set -euo pipefail

fail() {
  printf 'machinist macOS setup: %s\n' "$*" >&2
  exit 1
}

if [[ $(uname -s) != Darwin ]]; then
  fail "this installer supports macOS only"
fi
if [[ $(id -u) -eq 0 ]]; then
  fail "run this installer as the login user, not root"
fi

machinist_repository=${MACHINIST_REPOSITORY:-owainlewis/machinist}
if [[ ! $machinist_repository =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
  fail "MACHINIST_REPOSITORY must be an owner/repository name"
fi

machinist_role=${MACHINIST_NODE_ROLE:-all}
case $machinist_role in
  all | control-plane | worker) ;;
  *) fail "MACHINIST_NODE_ROLE must be all, control-plane, or worker" ;;
esac

machinist_version=${MACHINIST_VERSION:-}
machinist_source_binary=${MACHINIST_BINARY:-}
if [[ -z $machinist_source_binary && ! $machinist_version =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
  fail "set MACHINIST_VERSION to a release tag or MACHINIST_BINARY to a local build"
fi
if [[ -n $machinist_source_binary && (! -f $machinist_source_binary || ! -x $machinist_source_binary) ]]; then
  fail "MACHINIST_BINARY must name an executable regular file"
fi

dgx_ssh_host=${MACHINIST_DGX_SSH_HOST:-}
if [[ -n $dgx_ssh_host && ! $dgx_ssh_host =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$ ]]; then
  fail "MACHINIST_DGX_SSH_HOST must be a bounded SSH alias"
fi
dgx_local_port=${MACHINIST_DGX_LOCAL_PORT:-18000}
dgx_remote_port=${MACHINIST_DGX_REMOTE_PORT:-8000}
for port in "$dgx_local_port" "$dgx_remote_port"; do
  if [[ ! $port =~ ^[0-9]+$ ]] || ((port < 1 || port > 65535)); then
    fail "DGX ports must be integers between 1 and 65535"
  fi
done

runtime_home=${HOME:?HOME is required}
install_directory=${MACHINIST_INSTALL_DIR:-$runtime_home/.local/bin}
launch_agent_directory=$runtime_home/Library/LaunchAgents
log_directory=$runtime_home/Library/Logs/Machinist
mkdir -p "$install_directory" "$launch_agent_directory" "$log_directory"
chmod 700 "$log_directory"

machinist_binary=$install_directory/machinist
if [[ -n $machinist_source_binary ]]; then
  install -m 0755 "$machinist_source_binary" "$machinist_binary"
else
  curl -fsSL "https://raw.githubusercontent.com/$machinist_repository/$machinist_version/install.sh" | \
    env HOME="$runtime_home" MACHINIST_VERSION="$machinist_version" \
      MACHINIST_REPOSITORY="$machinist_repository" \
      MACHINIST_INSTALL_DIR="$install_directory" sh
fi
"$machinist_binary" init

script_directory=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
source_root=$(cd "$script_directory/.." && pwd)
template_directory=$source_root/deploy/launchd
temporary_directory=$(mktemp -d)
trap 'rm -rf "$temporary_directory"' EXIT

required_templates=(
  sh.machinist.control-plane.plist.in
  sh.machinist.worker.plist.in
  sh.machinist.dgx-tunnel.plist.in
)
if [[ ! -d $template_directory ]]; then
  [[ -n $machinist_version ]] || fail "launchd templates are unavailable beside the installer"
  template_directory=$temporary_directory/templates
  mkdir -p "$template_directory"
  for template in "${required_templates[@]}"; do
    curl -fsSL "https://raw.githubusercontent.com/$machinist_repository/$machinist_version/deploy/launchd/$template" \
      -o "$template_directory/$template"
  done
fi
for template in "${required_templates[@]}"; do
  [[ -f $template_directory/$template ]] || fail "missing launchd template $template"
done

provider_source=$source_root/scripts/nvidia-smi-json-provider.py
if [[ ! -f $provider_source ]]; then
  [[ -n $machinist_version ]] || fail "NVIDIA telemetry adapter is unavailable beside the installer"
  provider_source=$temporary_directory/nvidia-smi-json-provider.py
  curl -fsSL "https://raw.githubusercontent.com/$machinist_repository/$machinist_version/scripts/nvidia-smi-json-provider.py" \
    -o "$provider_source"
fi
provider_install_directory=$runtime_home/.local/libexec/machinist
mkdir -p "$provider_install_directory"
install -m 0755 "$provider_source" "$provider_install_directory/nvidia-smi-json-provider"

path_entries=("$install_directory")
for executable in codex claude opencode pi aider; do
  if executable_path=$(command -v "$executable" 2>/dev/null); then
    path_entries+=("$(dirname "$executable_path")")
  fi
done
path_entries+=(/opt/homebrew/bin /usr/local/bin /usr/bin /bin /usr/sbin /sbin)
launch_path=$(IFS=:; printf '%s' "${path_entries[*]}")

for value in "$runtime_home" "$machinist_binary" "$log_directory" "$launch_path" "$dgx_ssh_host"; do
  if [[ $value == *'&'* || $value == *'<'* || $value == *'>'* || $value == *'|'* || $value == *'\\'* || $value == *$'\n'* ]]; then
    fail "a deployment path or host contains a character unsafe for a launchd template"
  fi
done

render_template() {
  local source=$1 destination=$2
  sed \
    -e "s|__HOME__|$runtime_home|g" \
    -e "s|__MACHINIST_BIN__|$machinist_binary|g" \
    -e "s|__PATH__|$launch_path|g" \
    -e "s|__LOG_DIR__|$log_directory|g" \
    -e "s|__DGX_SSH_HOST__|$dgx_ssh_host|g" \
    -e "s|__DGX_LOCAL_PORT__|$dgx_local_port|g" \
    -e "s|__DGX_REMOTE_PORT__|$dgx_remote_port|g" \
    "$source" >"$destination"
  plutil -lint "$destination" >/dev/null
}

launch_domain=gui/$(id -u)
install_agent() {
  local label=$1
  local template=$2
  local rendered=$temporary_directory/$label.plist
  local target=$launch_agent_directory/$label.plist
  render_template "$template_directory/$template" "$rendered"
  launchctl bootout "$launch_domain/$label" >/dev/null 2>&1 || true
  if [[ -f $target ]] && ! cmp -s "$target" "$rendered"; then
    cp -p "$target" "$target.backup.$(date -u +%Y%m%dT%H%M%SZ).$$"
  fi
  install -m 0644 "$rendered" "$target"
  launchctl bootstrap "$launch_domain" "$target"
  launchctl kickstart -k "$launch_domain/$label"
}

disable_agent() {
  local label=$1
  launchctl bootout "$launch_domain/$label" >/dev/null 2>&1 || true
}

if [[ -n $dgx_ssh_host ]]; then
  ssh -o BatchMode=yes -o StrictHostKeyChecking=yes -o ConnectTimeout=5 "$dgx_ssh_host" true
  install_agent sh.machinist.dgx-tunnel sh.machinist.dgx-tunnel.plist.in
else
  disable_agent sh.machinist.dgx-tunnel
fi

if [[ $machinist_role == all || $machinist_role == control-plane ]]; then
  install_agent sh.machinist.control-plane sh.machinist.control-plane.plist.in
else
  disable_agent sh.machinist.control-plane
fi

worker_enabled=false
if [[ $machinist_role == all || $machinist_role == worker ]]; then
  if "$machinist_binary" worker validate >/dev/null 2>&1; then
    install_agent sh.machinist.worker sh.machinist.worker.plist.in
    worker_enabled=true
  else
    disable_agent sh.machinist.worker
  fi
else
  disable_agent sh.machinist.worker
fi

printf 'Machinist macOS deployment installed.\n'
printf '  binary: %s\n  role: %s\n  control plane: %s\n  worker: %s\n' \
  "$machinist_binary" "$machinist_role" \
  "$([[ $machinist_role == all || $machinist_role == control-plane ]] && printf enabled || printf disabled)" \
  "$([[ $worker_enabled == true ]] && printf enabled || printf disabled-until-valid)"
if [[ -n $dgx_ssh_host ]]; then
  printf '  DGX tunnel: %s -> 127.0.0.1:%s\n' "$dgx_ssh_host" "$dgx_local_port"
fi
