#!/bin/sh
set -eu

plugin_state_dir=${HERDR_PLUGIN_STATE_DIR:?HERDR_PLUGIN_STATE_DIR is required}
socket_path=${HERDR_SOCKET_PATH:?HERDR_SOCKET_PATH is required}
session_name=$(basename "$(dirname "$socket_path")")
case "$session_name" in
  "" | *[!A-Za-z0-9._-]*) exit 0 ;;
esac
state_dir="$plugin_state_dir/sessions/$session_name"
pid_file="$state_dir/worker.pid"
if [ ! -f "$pid_file" ]; then
  exit 0
fi
worker_pid=$(sed -n '1p' "$pid_file")
if [ -n "$worker_pid" ] && kill -0 "$worker_pid" 2>/dev/null; then
  kill "$worker_pid"
fi
rm -f "$pid_file"
