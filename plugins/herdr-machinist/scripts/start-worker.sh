#!/bin/sh
set -eu

state_dir=${HERDR_PLUGIN_STATE_DIR:?HERDR_PLUGIN_STATE_DIR is required}
socket_path=${HERDR_SOCKET_PATH:?HERDR_SOCKET_PATH is required}
session_name=$(basename "$(dirname "$socket_path")")

# The conventional `machinist` session uses the normal worker configuration.
# Any other named session is enabled only by an explicitly provisioned config
# with the same name. This lets operators isolate one harness/profile per Herdr
# namespace without making the plugin active in unrelated sessions.
worker_config=${MACHINIST_WORKER_CONFIG:-}
if [ -z "$worker_config" ] && [ "$session_name" != "machinist" ]; then
  case "$session_name" in
    "" | *[!A-Za-z0-9._-]*) exit 0 ;;
  esac
  session_config_dir=${MACHINIST_HERDR_CONFIG_DIR:-"$HOME/.machinist/herdr-sessions"}
  worker_config="$session_config_dir/$session_name.toml"
  if [ ! -f "$worker_config" ]; then
    exit 0
  fi
fi
if [ -n "$worker_config" ] && [ ! -f "$worker_config" ]; then
  printf 'Machinist Herdr worker config does not exist: %s\n' "$worker_config" >&2
  exit 1
fi
mkdir -p "$state_dir"
pid_file="$state_dir/worker.pid"
log_file="$state_dir/worker.log"

if [ -f "$pid_file" ]; then
  old_pid=$(sed -n '1p' "$pid_file")
  if [ -n "$old_pid" ] && kill -0 "$old_pid" 2>/dev/null; then
    exit 0
  fi
fi

if [ -n "${MACHINIST_BIN:-}" ] && [ -x "$MACHINIST_BIN" ]; then
  machinist_bin=$MACHINIST_BIN
elif command -v machinist >/dev/null 2>&1; then
  machinist_bin=$(command -v machinist)
elif [ -x "$HOME/.local/bin/machinist" ]; then
  machinist_bin="$HOME/.local/bin/machinist"
else
  printf '%s\n' 'Machinist is not installed; interactive worker was not started.' >&2
  exit 127
fi

set -- worker start --transport herdr
if [ -n "$worker_config" ]; then
  set -- "$@" --config "$worker_config"
fi
nohup "$machinist_bin" "$@" >>"$log_file" 2>&1 &
worker_pid=$!
printf '%s\n' "$worker_pid" >"$pid_file"
