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

ASSUME_YES=0
if [ "${1:-}" = "--yes" ]; then
  ASSUME_YES=1
  shift
fi

DEV="${1:-}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STAGE="$REPO_ROOT/board/buildroot/output/images/boot"

red()   { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }

[ -n "$DEV" ] || { echo "usage: $0 /dev/sdX"; exit 1; }
[ -b "$DEV" ] || { red "not a block device: $DEV"; exit 1; }
[ -d "$STAGE" ] || { red "no staged image at $STAGE — run 'make image' first"; exit 1; }

# Must be a whole disk, not a partition — writing a partition table into a
# partition is the one mistake here that cannot be undone.
#
# Ask lsblk rather than pattern-matching the name: whole disks on mmc and
# nvme end in a digit (mmcblk0, nvme0n1) and their partitions carry a 'p'
# (mmcblk0p1), so the usual "ends in a digit means partition" rule rejects
# exactly the devices this script is for.
# lsblk pads column output with leading spaces, so every value read from it
# has to be trimmed before comparison.
lsb() { lsblk -ndo "$1" "$2" 2>/dev/null | tr -d '[:space:]'; }

devtype=$(lsb TYPE "$DEV")
if [ "$devtype" != "disk" ]; then
  red "$DEV is a '$devtype', not a whole disk"
  exit 1
fi

# Guard against writing over a system disk.
#
# RM alone is not enough: built-in SD readers commonly report RM=0 while
# still being perfectly removable, so a card in a laptop slot would be
# rejected. Accept a device that is either marked removable OR hotpluggable
# on an mmc/usb transport, and reject anything else.
rm_flag=$(lsb RM "$DEV")
hotplug=$(lsb HOTPLUG "$DEV")
tran=$(lsb TRAN "$DEV")

if [ "$rm_flag" != "1" ] && ! { [ "$hotplug" = "1" ] && { [ "$tran" = "mmc" ] || [ "$tran" = "usb" ]; }; }; then
  red "$DEV is neither removable nor a hotpluggable mmc/usb device."
  echo "Refusing to partition it. If you are certain, do it by hand."
  exit 1
fi

# Never touch a disk that anything is mounted from outside /media or /mnt.
while read -r mnt; do
  [ -n "$mnt" ] || continue
  case "$mnt" in
    /media/*|/mnt/*) ;;
    *)
      red "$DEV has a partition mounted at $mnt"
      echo "That does not look like a removable card. Refusing."
      exit 1
      ;;
  esac
done < <(lsblk -nro MOUNTPOINTS "$DEV" 2>/dev/null | grep -v '^$' || true)

echo "About to ERASE and repartition:"
lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS "$DEV" || true
echo

if [ "$ASSUME_YES" = 1 ]; then
  echo "(--yes given; skipping confirmation)"
else
  read -r -p "Type the device path again to confirm: " confirm
  [ "$confirm" = "$DEV" ] || { red "mismatch; aborting"; exit 1; }
fi

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
sudo partprobe "$DEV" 2>/dev/null || true

# Wait for udev to create the partition node. partprobe returns before the
# node exists, so checking once loses the race about half the time.
PART=""
for _ in $(seq 1 20); do
  for candidate in "${DEV}p1" "${DEV}1"; do
    if [ -b "$candidate" ]; then
      PART="$candidate"
      break 2
    fi
  done
  sleep 0.5
done
[ -n "$PART" ] || { red "cannot find the new partition on $DEV"; exit 1; }
echo "  partition is $PART"

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

# Verify by reading back from the card, not from the page cache.
#
# cp succeeding and a post-copy ls both lie: they read what the kernel has
# buffered, not what reached the flash. A write onto a FAT that was left
# inconsistent by an unclean shutdown can orphan an entire file's clusters,
# and the only way to find out is to unmount and read it back. This happened
# for real — kernel.img reported 11MB and was zero bytes on the card, giving
# a device that would not boot.
echo "Verifying..."
sudo mount -o ro "$PART" "$MNT"
bad=0
for f in "$STAGE"/*; do
  [ -f "$f" ] || continue
  name=$(basename "$f")
  if ! sudo cmp -s "$f" "$MNT/$name"; then
    red "  $name MISMATCH"
    bad=1
  fi
done
sudo umount "$MNT"
trap - EXIT
rmdir "$MNT"

if [ "$bad" != 0 ]; then
  red "Verification FAILED — do not boot this card."
  exit 1
fi
green "Verified: every file on the card matches the build."

green "Done."
cat <<'NOTES'

  If you have a wpa_supplicant.conf, drop it on the card now. Otherwise the
  mirror boots as an open access point called "MagicMirror-Setup" and you can
  configure WiFi from a phone.

NOTES
