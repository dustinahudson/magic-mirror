#!/usr/bin/env bash
#
# Assemble the SD card contents after Buildroot finishes.
#
# Output is a directory of files destined for a single FAT32 partition. That
# layout is the reason migrating a v1 card is a file copy rather than a
# reflash: the partition table does not change, and wpa_supplicant.conf and
# config.json stay exactly where they already are.

set -euo pipefail

BOARD_DIR="${1:?post-image.sh needs BR2_EXTERNAL_MAGICMIRROR_PATH}"
BINARIES_DIR="${BINARIES_DIR:-output/images}"
BOOT_STAGE="$BINARIES_DIR/boot"

echo "post-image: staging boot partition into $BOOT_STAGE"

rm -rf "$BOOT_STAGE"
mkdir -p "$BOOT_STAGE"

# --- Pi firmware ---
for f in bootcode.bin start.elf fixup.dat; do
  if [ -f "$BINARIES_DIR/rpi-firmware/$f" ]; then
    cp "$BINARIES_DIR/rpi-firmware/$f" "$BOOT_STAGE/"
  else
    echo "post-image: WARNING missing firmware file $f" >&2
  fi
done

# --- Kernel, with the initramfs linked in ---
# One file is the whole OS. That is what makes an OS update a single atomic
# replacement with a single file to keep as the rollback copy.
if [ -f "$BINARIES_DIR/zImage" ]; then
  cp "$BINARIES_DIR/zImage" "$BOOT_STAGE/kernel.img"
else
  echo "post-image: ERROR no zImage in $BINARIES_DIR" >&2
  exit 1
fi

# --- Device tree ---
for dtb in "$BINARIES_DIR"/bcm2708-rpi-zero-w.dtb "$BINARIES_DIR"/*.dtb; do
  [ -f "$dtb" ] && cp "$dtb" "$BOOT_STAGE/"
done

# --- Firmware config ---
cp "$BOARD_DIR/board/boot/config.txt" "$BOOT_STAGE/"
cp "$BOARD_DIR/board/boot/cmdline.txt" "$BOOT_STAGE/"

# --- Application binary ---
# Built outside Buildroot (see the top-level Makefile) because it is not part
# of the rootfs: keeping the app on FAT is what lets an app update ship
# without rebuilding a kernel.
if [ -f "$BOARD_DIR/dist/magicmirror-armv6" ]; then
  cp "$BOARD_DIR/dist/magicmirror-armv6" "$BOOT_STAGE/mm.current"
  chmod +x "$BOOT_STAGE/mm.current"
else
  echo "post-image: WARNING no dist/magicmirror-armv6; run 'make binary' first" >&2
fi

# --- Report the size budget ---
echo
echo "post-image: boot partition contents"
if command -v du >/dev/null; then
  du -h "$BOOT_STAGE"/* 2>/dev/null | sort -h || true
  echo "---"
  du -sh "$BOOT_STAGE" | awk '{print "total: " $1}'
fi
echo
echo "post-image: ready. 'make card DEV=/dev/sdX' to write, or copy"
echo "            $BOOT_STAGE/* onto an existing FAT32 card."
