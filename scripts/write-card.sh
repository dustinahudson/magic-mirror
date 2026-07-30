#!/usr/bin/env bash
#
# Write a fresh Magic Mirror card.
#
# Creates a single FAT32 partition and copies the staged boot files onto it.
# One partition on purpose: it is what makes a v1 migration a file copy, and
# it means there is no second filesystem to corrupt when the power is pulled.
#
# For an existing v1 card, use scripts/migrate.sh instead — it keeps your
# WiFi credentials and config in place.
#
# Usage:
#   scripts/write-card.sh /dev/sdX

set -euo pipefail

DEV="${1:-}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STAGE="$REPO_ROOT/board/buildroot/output/images/boot"

red()   { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }

[ -n "$DEV" ] || { echo "usage: $0 /dev/sdX"; exit 1; }
[ -b "$DEV" ] || { red "not a block device: $DEV"; exit 1; }
[ -d "$STAGE" ] || { red "no staged image at $STAGE — run 'make image' first"; exit 1; }

# Refuse anything that looks like a system disk. Writing a partition table
# over the wrong device is the one mistake here that cannot be undone.
case "$DEV" in
  *[0-9]) red "$DEV looks like a partition, not a whole disk"; exit 1 ;;
esac

if [ "$(lsblk -ndo RM "$DEV" 2>/dev/null || echo 0)" != "1" ]; then
  red "$DEV is not marked removable."
  echo "Refusing to partition it. If you are certain, do it by hand."
  exit 1
fi

echo "About to ERASE and repartition:"
lsblk -o NAME,SIZE,MODEL,TRAN "$DEV" || true
echo
read -r -p "Type the device path again to confirm: " confirm
[ "$confirm" = "$DEV" ] || { red "mismatch; aborting"; exit 1; }

# Unmount anything already mounted from this disk.
for part in "$DEV"?*; do
  [ -b "$part" ] || continue
  sudo umount "$part" 2>/dev/null || true
done

echo "Partitioning…"
# Type 0x0c is FAT32 LBA, which is what the Pi bootloader requires.
sudo sfdisk --quiet --wipe always "$DEV" <<'PARTS'
label: dos
start=2048, type=c
PARTS
sudo partprobe "$DEV" 2>/dev/null || sleep 2

PART="${DEV}1"
[ -b "$PART" ] || PART="${DEV}p1"
[ -b "$PART" ] || { red "cannot find the new partition on $DEV"; exit 1; }

echo "Formatting $PART as FAT32…"
sudo mkfs.vfat -F 32 -n BOOT "$PART"

MNT="$(mktemp -d)"
trap 'sudo umount "$MNT" 2>/dev/null || true; rmdir "$MNT" 2>/dev/null || true' EXIT
sudo mount "$PART" "$MNT"

echo "Copying boot files…"
sudo cp -r "$STAGE"/. "$MNT"/
sudo chmod +x "$MNT/mm.current" 2>/dev/null || true

# Seed a config so the first boot has something to show, without overwriting
# one the user already staged.
if [ -f "$REPO_ROOT/config.json" ] && [ ! -f "$MNT/config.json" ]; then
  sudo cp "$REPO_ROOT/config.json" "$MNT/config.json"
  echo "  seeded config.json from the repo root"
fi

sync
sudo umount "$MNT"
trap - EXIT
rmdir "$MNT"

green "Done."
cat <<'NOTES'

  If you have a wpa_supplicant.conf, drop it on the card now. Otherwise the
  mirror boots as an open access point called "MagicMirror-Setup" and you can
  configure WiFi from a phone.

NOTES
