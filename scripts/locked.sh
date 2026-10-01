#!/usr/bin/env bash
# Encrypted vault section.
#
#   locked.sh init      one-time: create the encrypted store and set a passphrase
#   locked.sh unlock    mount plaintext at the locked folder (prompts for the passphrase)
#   locked.sh lock      unmount; contents return to ciphertext
#   locked.sh status    is it mounted right now
#
# The ciphertext lives OUTSIDE the vault (default ~/.aimem-locked), so the vault's git
# repo and Obsidian's indexer never see it. The locked folder, named by folders.locked in
# the vault config, exists as plaintext only while mounted.
#
# What this does and does not guarantee:
#   - Locked: the plaintext does not exist anywhere on disk. No agent, process, or
#     backup can read it. This is real encryption, not a permission flag.
#   - Unlocked: anything running as your user can read it, including an agent. Unlock
#     deliberately, work, then lock again. Do not leave it mounted.
set -euo pipefail

cipher_dir="${AIMEM_LOCKED_STORE:-$HOME/.aimem-locked}"

# The mount point comes from the vault config so this cannot drift from the folder aimem
# refuses to read. Restating "09-Locked" here would be a second source of truth.
command -v aimem >/dev/null 2>&1 || { echo "aimem is not on PATH" >&2; exit 1; }
mount_point="$(aimem config --paths locked)"
[ -n "$mount_point" ] || { echo "no locked folder is configured for this vault" >&2; exit 1; }

require_gocryptfs() {
  if ! command -v gocryptfs >/dev/null 2>&1; then
    echo "gocryptfs is not installed. Install it with:" >&2
    echo "    pacman -S gocryptfs   |   apt install gocryptfs   |   brew install gocryptfs" >&2
    exit 1
  fi
}

is_mounted() {
  mountpoint -q "$mount_point" 2>/dev/null
}

case "${1:-status}" in
  init)
    require_gocryptfs
    if [ -f "$cipher_dir/gocryptfs.conf" ]; then
      echo "Already initialized at $cipher_dir. Nothing to do."
      echo "To unlock: $0 unlock"
      exit 0
    fi
    mkdir -p "$cipher_dir" "$mount_point"
    echo "Creating the encrypted store at $cipher_dir."
    echo "Choose a strong passphrase. There is no recovery if you lose it."
    echo
    gocryptfs -init "$cipher_dir"
    echo
    echo "Initialized. Back up $cipher_dir/gocryptfs.conf and the printed master key"
    echo "somewhere offline: losing both means losing the contents permanently."
    echo
    echo "Unlock with: $0 unlock"
    ;;

  unlock)
    require_gocryptfs
    if [ ! -f "$cipher_dir/gocryptfs.conf" ]; then
      echo "Not initialized yet. Run: $0 init" >&2
      exit 1
    fi
    if is_mounted; then
      echo "Already unlocked at $mount_point"
      exit 0
    fi
    mkdir -p "$mount_point"
    gocryptfs "$cipher_dir" "$mount_point"
    echo
    echo "UNLOCKED at $mount_point"
    echo "Anything running as your user can now read it, agents included."
    echo "Lock it again when you are done: $0 lock"
    ;;

  lock)
    if ! is_mounted; then
      echo "Already locked. $mount_point holds no plaintext."
      exit 0
    fi
    if command -v fusermount3 >/dev/null 2>&1; then
      fusermount3 -u "$mount_point"
    else
      fusermount -u "$mount_point"
    fi
    echo "LOCKED. $mount_point is empty; contents exist only as ciphertext in $cipher_dir."
    ;;

  status)
    if is_mounted; then
      echo "UNLOCKED  $mount_point is readable right now"
      echo "          lock it with: $0 lock"
    elif [ -f "$cipher_dir/gocryptfs.conf" ]; then
      echo "LOCKED    ciphertext at $cipher_dir, nothing readable"
    else
      echo "NOT SET UP  run: $0 init"
    fi
    ;;

  *)
    echo "usage: $0 {init|unlock|lock|status}" >&2
    exit 2
    ;;
esac
