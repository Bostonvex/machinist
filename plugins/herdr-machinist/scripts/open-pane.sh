#!/bin/sh
set -eu

entrypoint=${1:?plugin pane entrypoint is required}
herdr_bin=${HERDR_BIN_PATH:-herdr}
exec "$herdr_bin" plugin pane open --plugin bostonvex.machinist --entrypoint "$entrypoint"
