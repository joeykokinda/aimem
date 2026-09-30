#!/usr/bin/env sh
# Temporarily expose the PRIVATE half of the vault to Claude Code.
#
# This is deliberately a *different* mechanism from `aimem mcp`, not a flag on it. The
# aimem server is read-only and shared-tier-only by construction; if widening it were one
# argument away, the boundary would be a default rather than a property. Reaching for a
# separate command, and having to revoke it, is the point.
#
# Revoke when done:  ./revoke-private.sh
set -eu

command -v aimem >/dev/null 2>&1 || { echo "aimem is not on PATH" >&2; exit 1; }
command -v claude >/dev/null 2>&1 || { echo "the claude CLI is not on PATH" >&2; exit 1; }

name="${AIMEM_PRIVATE_SERVER:-aimem-full}"
if claude mcp get "$name" >/dev/null 2>&1; then
  echo "$name is already registered. Nothing to do."
  echo "Revoke with: $(dirname "$0")/revoke-private.sh"
  exit 0
fi

# Folders come from the vault config rather than being restated here, so this cannot
# drift from the tiers everything else enforces. The locked folder is never included,
# even when it happens to be mounted.
folders="$(aimem config --paths readable; aimem config --paths private)"
[ -n "$folders" ] || { echo "could not resolve vault folders" >&2; exit 1; }

echo "About to expose these folders, including private ones:"
echo "$folders" | sed 's/^/  /'
printf 'Continue? [y/N] '
read -r reply
case "$reply" in y|Y|yes|YES) ;; *) echo "Aborted."; exit 0 ;; esac

# shellcheck disable=SC2086
claude mcp add "$name" --scope user -- \
  npx -y @modelcontextprotocol/server-filesystem $folders

echo
echo "GRANTED: agents can now read the private folders."
echo "Restart your Claude Code session for it to take effect."
echo "Revoke when done: $(dirname "$0")/revoke-private.sh"
