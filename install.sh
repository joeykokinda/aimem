#!/usr/bin/env sh
# Build aimem and put it on PATH.
#
# This is a convenience wrapper. The supported install is:
#     go install github.com/joeykokinda/aimem/cmd/aimem@latest
set -eu

here="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
install_dir="${AIMEM_INSTALL_DIR:-$HOME/.local/bin}"

command -v go >/dev/null 2>&1 || {
  echo "Go is required to build aimem. Install it, or download a release binary." >&2
  exit 1
}

version="$(git -C "$here" describe --tags --always --dirty 2>/dev/null || echo dev)"
mkdir -p "$install_dir"
# Remove any existing entry first. Earlier versions installed a symlink into this
# checkout, and building straight to that path would write through the link and drop a
# binary back into the source tree instead of replacing the link.
rm -f "$install_dir/aimem"
( cd "$here" && go build -buildvcs=false -ldflags "-X main.Version=$version" -o "$install_dir/aimem" ./cmd/aimem )

echo "Installed aimem $version -> $install_dir/aimem"
case ":$PATH:" in
  *":$install_dir:"*) ;;
  *) echo "Note: $install_dir is not on your PATH." ;;
esac

pointer="${XDG_CONFIG_HOME:-$HOME/.config}/aimem/vault"
vault="${AIMEM_VAULT:-}"
[ -n "$vault" ] || { [ -f "$pointer" ] && vault="$(cat "$pointer")"; } || true

if [ -n "$vault" ] && [ -f "$vault/.aimem.yml" ]; then
  echo "Vault: $vault"
  echo "Next:  aimem refresh"
else
  echo "Next:  aimem init --vault /path/to/your/vault    (new vault)"
  echo "       aimem use /path/to/existing/vault         (already has .aimem.yml)"
fi
