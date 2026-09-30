#!/usr/bin/env sh
# Unit tests, then an end-to-end run against a throwaway vault built from scratch.
#
# The end-to-end half exists because every unit test uses a vault the test helper built.
# This one goes through `aimem init` the way a new user would, which is the path most
# likely to rot unnoticed.
set -eu

here="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
cd "$here"

echo "==> go vet"
go vet ./...

echo "==> gofmt"
unformatted="$(gofmt -l . || true)"
[ -z "$unformatted" ] || { echo "needs gofmt:"; echo "$unformatted"; exit 1; }

echo "==> go test"
go test ./...

echo "==> end to end"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
binary="$work/aimem"
go build -buildvcs=false -o "$binary" ./cmd/aimem

vault="$work/vault"
# Isolate every directory aimem writes to outside the vault. Without XDG_CONFIG_HOME the
# end-to-end `aimem init` below would overwrite the developer's real vault pointer.
export XDG_STATE_HOME="$work/state"
export XDG_CONFIG_HOME="$work/config"
unset AIMEM_VAULT OBBY_VAULT AIMEM_INDEX

"$binary" init --vault "$vault" >/dev/null
[ -f "$vault/.aimem.yml" ] || { echo "init did not write a config"; exit 1; }
[ -f "$XDG_CONFIG_HOME/aimem/vault" ] || { echo "init did not save a vault pointer"; exit 1; }

mkdir -p "$vault/Projects" "$vault/Daily"
cat > "$vault/Projects/Example.md" <<'NOTE'
---
type: project
status: active
tags: [demo]
---
# Example

## Goal
An end-to-end fixture note.
NOTE
cat > "$vault/Meta/README.md" <<'NOTE'
---
type: reference
status: evergreen
---
# Meta

Links to [[Example]].
NOTE
printf -- '- shipped the thing for [[Example]]\n- private line with no link\n' \
  > "$vault/Daily/2026-09-28.md"

"$binary" refresh --vault "$vault" >/dev/null
grep -q 'Example' "$vault/Meta/BRAIN.md" || { echo "BRAIN.md is missing the note"; exit 1; }
grep -q 'shipped the thing' "$vault/Meta/Activity.md" || { echo "timeline is missing the entry"; exit 1; }
grep -q 'private line' "$vault/Meta/Activity.md" && { echo "LEAK: an unlinked journal line reached the timeline"; exit 1; }

"$binary" find --vault "$vault" example | grep -q Example || { echo "find did not locate the note"; exit 1; }
"$binary" show --vault "$vault" Example | grep -q 'end-to-end fixture' || { echo "show did not print the note"; exit 1; }
"$binary" context --vault "$vault" --json | grep -q '"title": "Example"' || { echo "json context is wrong"; exit 1; }
"$binary" validate --vault "$vault" >/dev/null || { echo "validate failed on a clean vault"; exit 1; }
"$binary" doctor --vault "$vault" >/dev/null || { echo "doctor failed on a clean vault"; exit 1; }

# The MCP server must answer a handshake and refuse writes by default.
printf '%s\n%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  | "$binary" mcp --vault "$vault" > "$work/mcp.out"
grep -q 'vault_search' "$work/mcp.out" || { echo "mcp did not list its tools"; exit 1; }
grep -q 'vault_remember' "$work/mcp.out" && { echo "read-only mcp advertised the write tool"; exit 1; }

echo
echo "All tests passed."
