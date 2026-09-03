#!/bin/sh
set -eu

state_dir=${HERDR_PLUGIN_STATE_DIR:?HERDR_PLUGIN_STATE_DIR is required}
socket_path=${HERDR_SOCKET_PATH:?HERDR_SOCKET_PATH is required}
session_name=$(basename "$(dirname "$socket_path")")
# The default socket lives directly under the Herdr config directory. Machinist
# intentionally runs one interactive dispatcher, in the dedicated session.
if [ "$session_name" != "machinist" ]; then
  exit 0
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

nohup "$machinist_bin" worker start --transport herdr >>"$log_file" 2>&1 &
worker_pid=$!
printf '%s\n' "$worker_pid" >"$pid_file"
