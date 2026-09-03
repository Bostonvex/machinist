#!/bin/sh
set -eu

state_dir=${HERDR_PLUGIN_STATE_DIR:?HERDR_PLUGIN_STATE_DIR is required}
pid_file="$state_dir/worker.pid"
if [ ! -f "$pid_file" ]; then
  exit 0
fi
worker_pid=$(sed -n '1p' "$pid_file")
if [ -n "$worker_pid" ] && kill -0 "$worker_pid" 2>/dev/null; then
  kill "$worker_pid"
fi
rm -f "$pid_file"
