#!/usr/bin/env bash
set -uo pipefail

# Resolved through aimem so this follows whatever vault the machine is pointed at,
# rather than carrying a second copy of the path.
if command -v aimem >/dev/null 2>&1; then
  vault="$(aimem config --json | sed -n 's/.*"root": "\([^"]*\)".*/\1/p' | head -1)"
fi
vault="${AIMEM_VAULT:-${vault:-}}"
[ -n "$vault" ] || { echo "no vault configured; run: aimem use /path/to/vault" >&2; exit 1; }
plugins_dir="$vault/.obsidian/plugins"
mkdir -p "$plugins_dir"

# repo:plugin_id
targets=(
  "blacksmithgu/obsidian-dataview:dataview"
  "obsidian-tasks-group/obsidian-tasks:obsidian-tasks-plugin"
  "SilentVoid13/Templater:templater-obsidian"
  "Vinzent03/obsidian-git:obsidian-git"
)

for target in "${targets[@]}"; do
  repo="${target%%:*}"
  plugin_id="${target##*:}"
  echo "=== $plugin_id ($repo) ==="

  release_json=$(curl -sL -m 60 "https://api.github.com/repos/$repo/releases/latest")
  tag=$(echo "$release_json" | jq -r '.tag_name // empty')
  if [ -z "$tag" ]; then
    echo "FAIL: no release found for $repo"
    echo "$release_json" | head -3
    continue
  fi
  echo "tag: $tag"

  dest="$plugins_dir/$plugin_id"
  mkdir -p "$dest"

  ok=1
  for asset in main.js manifest.json styles.css; do
    url=$(echo "$release_json" | jq -r --arg a "$asset" '.assets[] | select(.name == $a) | .browser_download_url')
    if [ -z "$url" ]; then
      if [ "$asset" = "styles.css" ]; then
        echo "  (no styles.css, fine)"
        continue
      fi
      echo "  FAIL: missing asset $asset"
      ok=0
      continue
    fi
    if curl -sL -m 120 -o "$dest/$asset" "$url"; then
      echo "  got $asset ($(stat -c%s "$dest/$asset") bytes)"
    else
      echo "  FAIL downloading $asset"
      ok=0
    fi
  done

  if [ "$ok" -eq 1 ]; then
    echo "  installed $plugin_id"
  fi
done

echo
echo "=== result tree ==="
find "$plugins_dir" -type f | sort
