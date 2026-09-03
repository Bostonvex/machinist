#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
"$script_dir/stop-worker.sh"
exec "$script_dir/start-worker.sh"
