#!/usr/bin/env sh
# Remove the temporary private-folder grant created by grant-private.sh.
set -eu

name="${AIMEM_PRIVATE_SERVER:-aimem-full}"
for candidate in "$name"; do
  if claude mcp get "$candidate" >/dev/null 2>&1; then
    claude mcp remove "$candidate" >/dev/null 2>&1 || claude mcp remove "$candidate" -s user
    echo "REVOKED: $candidate removed."
  fi
done
echo "Restart your Claude Code session for it to take effect."
