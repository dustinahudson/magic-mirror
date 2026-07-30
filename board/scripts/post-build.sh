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
# Keep the radio driver and everything it depends on; drop the rest. The
# bcmrpi defconfig builds ~970 modules for hardware a Zero W does not have.
#
# brcmfmac stays a module on purpose: the recovery ladder reloads it to
# hard-reset a wedged radio without rebooting, which is only possible for a
# module.
#
# The dependency set is computed from modules.dep rather than hand-listed.
# An earlier version of this script hardcoded "brcmfmac* and brcmutil*" on
# the assumption that cfg80211 was built into the kernel. It was not — the
# CONFIG_CFG80211=y in linux.fragment did not take and it built as a module,
# which the prune then deleted. brcmfmac shipped intact but could not
# resolve a single cfg80211 symbol, so wlan0 never appeared and every layer
# above it failed. Asking depmod removes the whole class of mistake.

MODDIR=$(find "$TARGET/lib/modules" -maxdepth 1 -mindepth 1 -type d 2>/dev/null | head -1)
if [ -n "${MODDIR:-}" ]; then
    kver=$(basename "$MODDIR")
    total=$(find "$MODDIR" -name '*.ko*' | wc -l)

    if ! command -v depmod >/dev/null 2>&1; then
        say "WARNING no depmod on the host; leaving all $total modules in place"
    else
        # Build a dependency map over the FULL module set first. Pruning
        # before this point would be pruning blind.
        depmod -b "$TARGET" "$kver" 2>/dev/null || true

        keep="$(mktemp)"
        for want in brcmfmac brcmutil; do
            # modules.dep lines are "path/mod.ko: dep1.ko dep2.ko ...", and
            # depmod expands dependencies transitively — so the module plus
            # its listed deps is the complete closure.
            awk -v m="/${want}" '
                $1 ~ m { sub(/:$/, "", $1); for (i = 1; i <= NF; i++) print $i }
            ' "$MODDIR/modules.dep" >> "$keep" 2>/dev/null || true
        done
        sort -u "$keep" -o "$keep"

        if [ ! -s "$keep" ]; then
            say "WARNING brcmfmac not found in modules.dep; leaving all modules in place"
        else
            # Delete anything not in the closure.
            while IFS= read -r mod; do
                rel=${mod#"$MODDIR"/}
                grep -qxF "$rel" "$keep" || rm -f "$mod"
            done < <(find "$MODDIR" -name '*.ko*')

            find "$MODDIR" -type d -empty -delete 2>/dev/null || true
            kept=$(find "$MODDIR" -name '*.ko*' | wc -l)
            say "kernel modules: $total -> $kept"
            say "kept: $(find "$MODDIR" -name '*.ko*' -printf '%f ' 2>/dev/null)"

            # Fail loudly rather than shipping an image whose radio cannot
            # load. This is the check that would have caught the bug above.
            for required in brcmfmac cfg80211; do
                if ! find "$MODDIR" -name "${required}*.ko*" | grep -q . &&
                   ! grep -q "^${required} " "$TARGET/lib/modules/$kver/modules.builtin" 2>/dev/null &&
                   ! grep -q "/${required}.ko" "$TARGET/lib/modules/$kver/modules.builtin" 2>/dev/null; then
                    say "ERROR $required is neither a kept module nor built in — WiFi will not work"
                    exit 1
                fi
            done
        fi
        rm -f "$keep"

        # Regenerate over the pruned set.
        #
        # Not merely housekeeping: Buildroot leaves modules.dep empty here,
        # and modprobe refuses to load anything without it.
        if depmod -b "$TARGET" "$kver" 2>/dev/null; then
            deps=$(wc -l < "$MODDIR/modules.dep" 2>/dev/null || echo 0)
            say "regenerated modules.dep ($deps entries)"
        else
            say "WARNING depmod failed; modprobe brcmfmac will not work"
        fi
    fi
fi

# --- Documentation and locales -----------------------------------------
rm -rf "$TARGET/usr/share/man" "$TARGET/usr/share/doc" "$TARGET/usr/share/info"
rm -rf "$TARGET/usr/share/locale" "$TARGET/usr/share/i18n"

after=$(du -sk "$TARGET" | awk '{print $1}')
say "target filesystem: $((before / 1024))MB -> $((after / 1024))MB"
