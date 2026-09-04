#!/bin/sh
set -u

model=""
case "${1:-}" in
  --model=*) model=${1#--model=} ;;
  "") ;;
  *) printf 'usage: %s [--model=MODEL]\n' "$0" >&2; exit 2 ;;
esac

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
deepcode_bin=${DEEPCODE_BIN_PATH:-deepcode}
node_bin=${NODE_BIN_PATH:-node}
project_root=$(pwd -P)

export DEEPCODE_API_KEY=${DEEPCODE_API_KEY:-local}
export DEEPCODE_TELEMETRY_ENABLED=${DEEPCODE_TELEMETRY_ENABLED:-0}
export DEEPCODE_THINKING_ENABLED=${DEEPCODE_THINKING_ENABLED:-false}
if [ -n "$model" ]; then
  export DEEPCODE_MODEL=$model
fi

"$node_bin" "$script_dir/deepcode-session.mjs" observe "$project_root" &
observer_pid=$!
cleanup() {
  kill "$observer_pid" 2>/dev/null || true
  wait "$observer_pid" 2>/dev/null || true
}
trap cleanup EXIT HUP INT TERM

"$deepcode_bin"
status=$?
exit "$status"
