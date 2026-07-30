#!/usr/bin/env bash
#
# Prune the target filesystem before it becomes an initramfs.
#
# The rootfs lives in RAM and ships inside kernel.img, so every megabyte here
# is paid for twice: once in the image you write to the card, and again in
# the 512MB the Pi actually has. A stock build of this configuration is 27MB,
# and almost all of that is things a Pi Zero W cannot use.
#
# Two offenders:
#
#   - linux-firmware ships blobs for every Broadcom part (48 files for the
#     43430 family alone, plus a separate Cypress tree). This board has one
#     radio and needs three files.
#   - The bcmrpi defconfig builds hundreds of modules for hardware that does
#     not exist on a Zero W.
#
# Pruning here rather than by disabling hundreds of Kconfig symbols: the
# keep-list is short and legible, and it cannot silently drift out of date
# the way a long list of "# CONFIG_x is not set" would.

set -euo pipefail

TARGET="${1:?post-build.sh expects the target directory as \$1}"

say() { printf 'post-build: %s\n' "$*"; }

before=$(du -sk "$TARGET" | awk '{print $1}')

# --- Firmware -----------------------------------------------------------
#
# The Pi Zero W's radio is a BCM43430 on SDIO. brcmfmac wants the firmware
# blob, the regulatory CLM blob, and the board-specific NVRAM txt whose name
# matches the device tree compatible string.

FW="$TARGET/lib/firmware"
if [ -d "$FW/brcm" ]; then
    keep_dir="$(mktemp -d)"
    for f in \
        brcmfmac43430-sdio.bin \
        brcmfmac43430-sdio.clm_blob \
        'brcmfmac43430-sdio.raspberrypi,model-zero-w.txt'
    do
        if [ -f "$FW/brcm/$f" ]; then
            cp "$FW/brcm/$f" "$keep_dir/"
        else
            say "WARNING expected firmware file missing: brcm/$f"
        fi
    done

    rm -rf "$FW/brcm"
    mkdir -p "$FW/brcm"
    cp "$keep_dir"/* "$FW/brcm/" 2>/dev/null || true
    rm -rf "$keep_dir"
    say "pruned brcm firmware to $(ls -1 "$FW/brcm" | wc -l) files"
fi

# Cypress blobs are for the 43455/43012 parts on later boards.
rm -rf "$FW/cypress"

# --- Kernel modules -----------------------------------------------------
#
# Keep brcmfmac and brcmutil, drop the rest. The bcmrpi defconfig builds
# ~970 modules for hardware a Zero W does not have; the radio needs two.
#
# brcmfmac stays a module on purpose: the recovery ladder reloads it to
# hard-reset a wedged radio without rebooting, which is only possible for a
# module. cfg80211 and mac80211 are built in, so brcmfmac's only module-level
# dependency is brcmutil.
#
# Matching is extension-agnostic. This kernel compresses modules to .ko.xz,
# and an earlier version of this script looked for bare .ko and silently
# matched nothing at all.

MODDIR=$(find "$TARGET/lib/modules" -maxdepth 1 -mindepth 1 -type d 2>/dev/null | head -1)
if [ -n "${MODDIR:-}" ]; then
    total=$(find "$MODDIR" -name '*.ko*' | wc -l)

    kept=0
    while IFS= read -r mod; do
        base=$(basename "$mod")
        case "$base" in
            brcmfmac*|brcmutil*) kept=$((kept + 1)) ;;
            *) rm -f "$mod" ;;
        esac
    done < <(find "$MODDIR" -name '*.ko*')

    find "$MODDIR" -type d -empty -delete 2>/dev/null || true

    if [ "$kept" -eq 0 ]; then
        say "WARNING kept no modules — brcmfmac is missing, so WiFi will not come up"
    else
        say "kernel modules: $total -> $kept (brcmfmac, brcmutil)"
    fi

    # Regenerate modules.dep.
    #
    # Not merely housekeeping after the prune: Buildroot leaves modules.dep
    # empty here, and modprobe refuses to load anything without it — so the
    # driver-reload rung of the recovery ladder would fail even with the
    # module present.
    kver=$(basename "$MODDIR")
    if command -v depmod >/dev/null 2>&1; then
        if depmod -b "$TARGET" "$kver" 2>/dev/null; then
            deps=$(wc -l < "$MODDIR/modules.dep" 2>/dev/null || echo 0)
            say "regenerated modules.dep ($deps entries)"
        else
            say "WARNING depmod failed; modprobe brcmfmac will not work"
        fi
    else
        say "WARNING no depmod on the host; modprobe brcmfmac will not work"
    fi
fi

# --- Documentation and locales -----------------------------------------
rm -rf "$TARGET/usr/share/man" "$TARGET/usr/share/doc" "$TARGET/usr/share/info"
rm -rf "$TARGET/usr/share/locale" "$TARGET/usr/share/i18n"

after=$(du -sk "$TARGET" | awk '{print $1}')
say "target filesystem: $((before / 1024))MB -> $((after / 1024))MB"
