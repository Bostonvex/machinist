#!/bin/sh
set -u

model=""
case "${1:-}" in
  --model=*) model=${1#--model=} ;;
  "") ;;
  *) printf 'usage: %s [--model=MODEL]\n' "$0" >&2; exit 2 ;;
esac

prompt=$(LC_ALL=C sed -n '1,$p')
if [ -z "$prompt" ]; then
  printf '%s\n' 'DeepCode requires a prompt on standard input.' >&2
  exit 2
fi

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
deepcode_bin=${DEEPCODE_BIN_PATH:-deepcode}
node_bin=${NODE_BIN_PATH:-node}
project_root=$(pwd -P)
before=$($node_bin "$script_dir/deepcode-session.mjs" snapshot "$project_root")

export DEEPCODE_API_KEY=${DEEPCODE_API_KEY:-local}
export DEEPCODE_TELEMETRY_ENABLED=${DEEPCODE_TELEMETRY_ENABLED:-0}
export DEEPCODE_THINKING_ENABLED=${DEEPCODE_THINKING_ENABLED:-false}
if [ -n "$model" ]; then
  export DEEPCODE_MODEL=$model
fi

"$deepcode_bin" --exec --prompt "$prompt"
status=$?
"$node_bin" "$script_dir/deepcode-session.mjs" usage "$project_root" "$before" || true
exit "$status"
