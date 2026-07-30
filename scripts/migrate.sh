#!/usr/bin/env bash
#
# Migrate a v1 (Circle OS) Magic Mirror card to v2 (Buildroot + Go).
#
# This is a file copy onto the card you already have, not a reflash. The
# partition table does not change, and wpa_supplicant.conf and config.json
# stay exactly where they are — so you never re-enter WiFi credentials.
#
# It cannot be done over the air. v1's UpdateService can only replace a
# single file named kernel.img (src/services/update_service.cpp:186-224); it
# cannot write config.txt, and it deletes the old kernel *before* installing
# the new one, so a Linux image that failed to boot would leave nothing to
# fall back to. See "Migrating from v1" in DESIGN.md.
#
# Usage:
#   scripts/migrate.sh /media/$USER/BOOT           # migrate in place
#   scripts/migrate.sh --dry-run /media/$USER/BOOT # show what would happen

set -euo pipefail

DRY_RUN=0
if [ "${1:-}" = "--dry-run" ]; then
  DRY_RUN=1
  shift
fi

MOUNT="${1:-}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STAGE="$REPO_ROOT/board/buildroot/output/images/boot"

red()   { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
warn()  { printf '\033[33m%s\033[0m\n' "$*"; }

usage() {
  echo "usage: $0 [--dry-run] /path/to/mounted/BOOT"
  exit 1
}

[ -n "$MOUNT" ] || usage
[ -d "$MOUNT" ] || { red "not a directory: $MOUNT"; exit 1; }

run() {
  if [ "$DRY_RUN" = 1 ]; then
    echo "  would: $*"
  else
    "$@"
  fi
}

echo "Magic Mirror v1 -> v2 migration"
echo "  card:  $MOUNT"
echo "  stage: $STAGE"
[ "$DRY_RUN" = 1 ] && warn "  (dry run — nothing will be written)"
echo

# --- Sanity checks -----------------------------------------------------

if [ ! -d "$STAGE" ]; then
  red "No staged boot files at $STAGE"
  echo "Run 'make image' first (or 'make binary' and copy by hand)."
  exit 1
fi

for required in kernel.img config.txt cmdline.txt; do
  [ -f "$STAGE/$required" ] || { red "staging is missing $required"; exit 1; }
done

if [ ! -f "$STAGE/mm.current" ]; then
  red "staging has no mm.current — run 'make binary' first"
  exit 1
fi

# Refuse to touch something that is not a Magic Mirror card. Writing a
# kernel over an unrelated FAT volume would be a bad afternoon.
if [ ! -f "$MOUNT/config.txt" ] && [ ! -f "$MOUNT/kernel.img" ]; then
  red "$MOUNT does not look like a Pi boot partition"
  echo "Expected config.txt or kernel.img at the top level."
  exit 1
fi

FREE_KB=$(df -Pk "$MOUNT" | awk 'NR==2 {print $4}')
NEED_KB=$(du -sk "$STAGE" | awk '{print $1}')
if [ "$FREE_KB" -lt "$NEED_KB" ]; then
  warn "only ${FREE_KB}KB free, staging is ${NEED_KB}KB"
  warn "the old kernel.img will be replaced, which should free enough"
fi

# --- Back up what we are about to overwrite ----------------------------

BACKUP="$MOUNT/v1-backup"
echo "Backing up v1 boot files to $BACKUP/"
run mkdir -p "$BACKUP"
for f in kernel.img config.txt cmdline.txt version.txt; do
  if [ -f "$MOUNT/$f" ]; then
    run cp -p "$MOUNT/$f" "$BACKUP/$f"
    echo "  saved $f"
  fi
done

# --- Preserve user data ------------------------------------------------

echo
echo "Preserving your settings"

# v1 kept config.json in a config/ subdirectory; v2 reads it from the root.
# The v2 loader understands v1's schema (see applyV1Compat), so the file
# moves without being rewritten.
if [ -f "$MOUNT/config/config.json" ] && [ ! -f "$MOUNT/config.json" ]; then
  run cp -p "$MOUNT/config/config.json" "$MOUNT/config.json"
  green "  moved config/config.json -> config.json (v1 schema is still readable)"
elif [ -f "$MOUNT/config.json" ]; then
  green "  config.json already at the root — left alone"
else
  warn "  no config.json found; the mirror will boot with defaults"
fi

if [ -f "$MOUNT/wpa_supplicant.conf" ]; then
  green "  wpa_supplicant.conf found — left exactly where it is"
else
  warn "  no wpa_supplicant.conf; the mirror will come up without WiFi"
fi

# --- Install v2 --------------------------------------------------------

echo
echo "Installing v2"

# config.txt must be replaced, not merged. v1's carries dtoverlay=sdio for
# Circle's WiFi stack, which under Linux points SDIO at different GPIOs than
# the onboard radio uses and is expected to break WiFi entirely.
for f in config.txt cmdline.txt; do
  run cp "$STAGE/$f" "$MOUNT/$f"
  echo "  installed $f"
done

for f in bootcode.bin start.elf fixup.dat; do
  if [ -f "$STAGE/$f" ]; then
    run cp "$STAGE/$f" "$MOUNT/$f"
    echo "  installed $f"
  fi
done

for dtb in "$STAGE"/*.dtb; do
  [ -f "$dtb" ] || continue
  run cp "$dtb" "$MOUNT/"
  echo "  installed $(basename "$dtb")"
done

run cp "$STAGE/kernel.img" "$MOUNT/kernel.img"
echo "  installed kernel.img (Linux + initramfs)"

run cp "$STAGE/mm.current" "$MOUNT/mm.current"
run chmod +x "$MOUNT/mm.current" 2>/dev/null || true
echo "  installed mm.current"

# Clear any inherited rollback state so the first v2 boot starts clean.
run rm -f "$MOUNT/failures" "$MOUNT/health" "$MOUNT/mm.previous"

if [ "$DRY_RUN" = 0 ]; then
  sync
fi

# --- Done --------------------------------------------------------------

echo
green "Migration complete."
cat <<'NOTES'

  Eject the card and boot it. Expect a black screen for roughly ten
  seconds — that is Linux booting with the rainbow, penguin and console
  output all suppressed — then the mirror.

  The status bar shows the IP address; the config page is at
  http://<that-ip>/

  If it does not come up, put the card back in a reader and restore from
  v1-backup/ to get the Circle OS build back:

      cp v1-backup/kernel.img v1-backup/config.txt v1-backup/cmdline.txt .

NOTES
