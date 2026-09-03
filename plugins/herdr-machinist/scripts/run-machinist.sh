#!/bin/sh
set -eu

if [ -n "${MACHINIST_BIN:-}" ] && [ -x "$MACHINIST_BIN" ]; then
  exec "$MACHINIST_BIN" "$@"
fi
if command -v machinist >/dev/null 2>&1; then
  exec machinist "$@"
fi
if [ -x "$HOME/.local/bin/machinist" ]; then
  exec "$HOME/.local/bin/machinist" "$@"
fi
printf '%s\n' 'Machinist is not installed. Run scripts/setup-macos.sh from the repository.' >&2
exit 127
