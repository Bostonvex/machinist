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
deepcode_bin=${DEEPCODE_BIN_PATH:-}
if [ -z "$deepcode_bin" ]; then
  if command -v deepcode >/dev/null 2>&1; then
    deepcode_bin=$(command -v deepcode)
  elif [ -x "${HOME:?}/.local/bin/deepcode" ]; then
    deepcode_bin="${HOME}/.local/bin/deepcode"
  else
    printf 'deepcode was not found; add it to PATH or set DEEPCODE_BIN_PATH\n' >&2
    exit 127
  fi
fi
node_bin=${NODE_BIN_PATH:-node}
project_root=$(pwd -P)
before=$($node_bin "$script_dir/deepcode-session.mjs" snapshot "$project_root")
deepcode_base_url=${DEEPCODE_BASE_URL:-${MACHINIST_DGX_DEEPCODE_BASE_URL:-http://127.0.0.1:18000/v1}}

export DEEPCODE_API_KEY=${DEEPCODE_API_KEY:-local}
export DEEPCODE_BASE_URL=$deepcode_base_url
export DEEPCODE_TELEMETRY_ENABLED=${DEEPCODE_TELEMETRY_ENABLED:-0}
export DEEPCODE_THINKING_ENABLED=${DEEPCODE_THINKING_ENABLED:-false}
if [ -n "$model" ]; then
  export DEEPCODE_MODEL=$model
fi

"$deepcode_bin" --exec --prompt "$prompt"
status=$?
"$node_bin" "$script_dir/deepcode-session.mjs" usage "$project_root" "$before" || true
exit "$status"
