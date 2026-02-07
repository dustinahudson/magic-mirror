#!/bin/bash
#
# Initialize build environment for Magic Mirror
#
# Sets up all dependencies needed to build:
#   - circle-stdlib (Circle OS + newlib + mbedTLS)
#   - Circle addon libraries (wlan, lvgl, fatfs, SDCard)
#   - stb_truetype (font rendering)
#   - date library (date/time utilities)
#   - Pi boot + WiFi firmware
#
# Usage: ./init.sh
#

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LIB_DIR="$SCRIPT_DIR/lib"
DEPS_DIR="$SCRIPT_DIR/deps"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

# Pinned versions
CIRCLE_STDLIB_URL="https://github.com/smuehlst/circle-stdlib.git"
CIRCLE_STDLIB_COMMIT="83efe0b"  # Step 50.0.1 — LVGL 9.2.2, Circle 50.0.1
STB_TRUETYPE_URL="https://raw.githubusercontent.com/nothings/stb/master/stb_truetype.h"
DATE_URL="https://github.com/HowardHinnant/date.git"

# Firmware sources (must match deploy.sh)
BOOT_FW_REV="bc7f439c234e19371115e07b57c366df59cc1bc7"
BOOT_FW_URL="https://github.com/raspberrypi/firmware/raw/${BOOT_FW_REV}/boot"
WIFI_FW_REV="c9d3ae6584ab79d19a4f94ccf701e888f9f87a53"
WIFI_FW_URL="https://github.com/RPi-Distro/firmware-nonfree/raw/${WIFI_FW_REV}/debian/config/brcm80211"

# ---------------------------------------------------------------------------
# 1. circle-stdlib (git submodule)
# ---------------------------------------------------------------------------
setup_circle_stdlib() {
    echo -e "${YELLOW}=== Setting up circle-stdlib ===${NC}"

    if [ ! -f "$LIB_DIR/circle-stdlib/configure" ]; then
        echo "Adding circle-stdlib submodule..."
        # Use submodule update if already registered, otherwise add fresh
        if git -C "$SCRIPT_DIR" config --get "submodule.lib/circle-stdlib.url" > /dev/null 2>&1; then
            echo "Submodule already registered, initializing..."
            git -C "$SCRIPT_DIR" submodule update --init lib/circle-stdlib
        else
            git -C "$SCRIPT_DIR" submodule add "$CIRCLE_STDLIB_URL" lib/circle-stdlib
            git -C "$SCRIPT_DIR" submodule update --init lib/circle-stdlib
        fi
    else
        echo "circle-stdlib already present"
    fi

    # Pin to known working commit
    echo "Checking out pinned version ($CIRCLE_STDLIB_COMMIT)..."
    git -C "$LIB_DIR/circle-stdlib" checkout "$CIRCLE_STDLIB_COMMIT"

    # Initialize nested submodules (circle, circle-newlib, mbedtls)
    echo "Initializing nested submodules..."
    git -C "$LIB_DIR/circle-stdlib" submodule update --init --recursive

    echo -e "${GREEN}circle-stdlib ready${NC}"
}

# ---------------------------------------------------------------------------
# 2. Configure circle-stdlib for Pi Zero W
# ---------------------------------------------------------------------------
configure_circle_stdlib() {
    local stdlib_dir="$LIB_DIR/circle-stdlib"

    if [ -f "$stdlib_dir/Config.mk" ]; then
        echo -e "${YELLOW}circle-stdlib already configured, skipping${NC}"
        return 0
    fi

    echo -e "${YELLOW}=== Configuring circle-stdlib (Pi Zero W, RASPPI=1) ===${NC}"
    cd "$stdlib_dir"
    ./configure -r 1 -p arm-none-eabi-
    cd "$SCRIPT_DIR"

    echo -e "${GREEN}circle-stdlib configured${NC}"
}

# ---------------------------------------------------------------------------
# 2b. Patch Circle sysconfig.h for 4MB kernel max size
# ---------------------------------------------------------------------------
patch_sysconfig() {
    local sysconfig="$LIB_DIR/circle-stdlib/libs/circle/include/circle/sysconfig.h"

    if [ ! -f "$sysconfig" ]; then
        echo -e "${RED}sysconfig.h not found${NC}"
        return 1
    fi

    echo -e "${YELLOW}=== Patching sysconfig.h (KERNEL_MAX_SIZE=4MB) ===${NC}"
    sed -i 's/#define KERNEL_MAX_SIZE\t\t(2 \* MEGABYTE)/#define KERNEL_MAX_SIZE\t\t(4 * MEGABYTE)/' "$sysconfig"

    echo -e "${GREEN}sysconfig.h patched${NC}"
}

# ---------------------------------------------------------------------------
# 3. Build circle-stdlib (newlib, mbedTLS, Circle core)
# ---------------------------------------------------------------------------
build_circle_stdlib() {
    local stdlib_dir="$LIB_DIR/circle-stdlib"

    if [ -f "$stdlib_dir/install/arm-none-circle/lib/libc.a" ]; then
        echo -e "${YELLOW}circle-stdlib already built, skipping${NC}"
        return 0
    fi

    echo -e "${YELLOW}=== Building circle-stdlib ===${NC}"
    make -C "$stdlib_dir"

    echo -e "${GREEN}circle-stdlib built${NC}"
}

# ---------------------------------------------------------------------------
# 3b. Build mbedTLS + circle-mbedtls wrapper
# ---------------------------------------------------------------------------
build_mbedtls() {
    local stdlib_dir="$LIB_DIR/circle-stdlib"

    if [ -f "$stdlib_dir/src/circle-mbedtls/libcircle-mbedtls.a" ]; then
        echo -e "${YELLOW}mbedTLS already built, skipping${NC}"
        return 0
    fi

    echo -e "${YELLOW}=== Building mbedTLS ===${NC}"
    make -C "$stdlib_dir" mbedtls

    echo -e "${GREEN}mbedTLS built${NC}"
}

# ---------------------------------------------------------------------------
# 4. Build Circle addon libraries
# ---------------------------------------------------------------------------
build_addon_libs() {
    local circle_dir="$LIB_DIR/circle-stdlib/libs/circle"
    local addon_dir="$circle_dir/addon"

    echo -e "${YELLOW}=== Building Circle addon libraries ===${NC}"

    # Enable required LVGL fonts before building
    local lv_conf="$addon_dir/lvgl/lv_conf.h"
    if [ -f "$lv_conf" ]; then
        sed -i 's/#define LV_FONT_MONTSERRAT_32 0/#define LV_FONT_MONTSERRAT_32 1/' "$lv_conf"
        sed -i 's/#define LV_FONT_MONTSERRAT_48 0/#define LV_FONT_MONTSERRAT_48 1/' "$lv_conf"
    fi

    # wlan (includes hostap/wpa_supplicant)
    if [ ! -f "$addon_dir/wlan/libwlan.a" ]; then
        echo "Building wlan..."
        cd "$addon_dir/wlan"
        ./makeall --nosample
        cd "$SCRIPT_DIR"
    else
        echo "wlan already built"
    fi

    # lvgl
    if [ ! -f "$addon_dir/lvgl/liblvgl.a" ]; then
        echo "Building lvgl..."
        make -C "$addon_dir/lvgl"
    else
        echo "lvgl already built"
    fi

    # fatfs
    if [ ! -f "$addon_dir/fatfs/libfatfs.a" ]; then
        echo "Building fatfs..."
        make -C "$addon_dir/fatfs"
    else
        echo "fatfs already built"
    fi

    # SDCard
    if [ ! -f "$addon_dir/SDCard/libsdcard.a" ]; then
        echo "Building SDCard..."
        make -C "$addon_dir/SDCard"
    else
        echo "SDCard already built"
    fi

    echo -e "${GREEN}Addon libraries built${NC}"
}

# ---------------------------------------------------------------------------
# 5. Download stb_truetype.h
# ---------------------------------------------------------------------------
download_stb() {
    echo -e "${YELLOW}=== Setting up stb_truetype ===${NC}"

    if [ -f "$LIB_DIR/stb/stb_truetype.h" ]; then
        echo "stb_truetype.h already present"
        return 0
    fi

    mkdir -p "$LIB_DIR/stb"
    echo "Downloading stb_truetype.h..."
    wget -q -O "$LIB_DIR/stb/stb_truetype.h" "$STB_TRUETYPE_URL"

    echo -e "${GREEN}stb_truetype ready${NC}"
}

# ---------------------------------------------------------------------------
# 6. Download date library
# ---------------------------------------------------------------------------
download_date() {
    echo -e "${YELLOW}=== Setting up date library ===${NC}"

    if [ -d "$LIB_DIR/date/include" ]; then
        echo "date library already present"
        return 0
    fi

    echo "Cloning date library..."
    git clone --depth 1 "$DATE_URL" "$LIB_DIR/date"

    echo -e "${GREEN}date library ready${NC}"
}

# ---------------------------------------------------------------------------
# 7. Download firmware dependencies
# ---------------------------------------------------------------------------
download_firmware_deps() {
    echo -e "${YELLOW}=== Downloading firmware dependencies ===${NC}"

    mkdir -p "$DEPS_DIR"

    local need_download=false
    for f in bootcode.bin start.elf fixup.dat bcm2708-rpi-zero-w.dtb \
             brcmfmac43430-sdio.bin brcmfmac43430-sdio.txt brcmfmac43430-sdio.clm_blob; do
        if [ ! -f "$DEPS_DIR/$f" ]; then
            need_download=true
            break
        fi
    done

    if ! $need_download; then
        echo "Firmware dependencies already present"
        return 0
    fi

    # Boot firmware
    for f in bootcode.bin start.elf fixup.dat bcm2708-rpi-zero-w.dtb; do
        if [ ! -f "$DEPS_DIR/$f" ]; then
            echo "  Downloading $f..."
            wget -q -O "$DEPS_DIR/$f" "${BOOT_FW_URL}/$f"
        fi
    done

    # WiFi firmware
    local -A WIFI_FILES=(
        [brcmfmac43430-sdio.bin]="cypress/cyfmac43430-sdio.bin"
        [brcmfmac43430-sdio.txt]="brcm/brcmfmac43430-sdio.txt"
        [brcmfmac43430-sdio.clm_blob]="cypress/cyfmac43430-sdio.clm_blob"
        [brcmfmac43436-sdio.bin]="brcm/brcmfmac43436-sdio.bin"
        [brcmfmac43436-sdio.txt]="brcm/brcmfmac43436-sdio.txt"
        [brcmfmac43436-sdio.clm_blob]="brcm/brcmfmac43436-sdio.clm_blob"
        [brcmfmac43436s-sdio.bin]="brcm/brcmfmac43436s-sdio.bin"
        [brcmfmac43436s-sdio.txt]="brcm/brcmfmac43436s-sdio.txt"
        [brcmfmac43455-sdio.bin]="cypress/cyfmac43455-sdio-minimal.bin"
        [brcmfmac43455-sdio.txt]="brcm/brcmfmac43455-sdio.txt"
        [brcmfmac43455-sdio.clm_blob]="cypress/cyfmac43455-sdio.clm_blob"
        [brcmfmac43456-sdio.bin]="brcm/brcmfmac43456-sdio.bin"
        [brcmfmac43456-sdio.txt]="brcm/brcmfmac43456-sdio.txt"
        [brcmfmac43456-sdio.clm_blob]="brcm/brcmfmac43456-sdio.clm_blob"
        [brcmfmac43455-sdio.raspberrypi,5-model-b.bin]="cypress/cyfmac43455-sdio-minimal.bin"
        [brcmfmac43455-sdio.raspberrypi,5-model-b.txt]="brcm/brcmfmac43455-sdio.txt"
        [brcmfmac43455-sdio.raspberrypi,5-model-b.clm_blob]="cypress/cyfmac43455-sdio.clm_blob"
    )

    for dest in "${!WIFI_FILES[@]}"; do
        if [ ! -f "$DEPS_DIR/$dest" ]; then
            echo "  Downloading $dest..."
            wget -q -O "$DEPS_DIR/$dest" "${WIFI_FW_URL}/${WIFI_FILES[$dest]}"
        fi
    done

    echo -e "${GREEN}Firmware dependencies ready${NC}"
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
echo -e "${YELLOW}Initializing Magic Mirror build environment...${NC}"
echo ""

setup_circle_stdlib
configure_circle_stdlib
patch_sysconfig
build_circle_stdlib
build_mbedtls
build_addon_libs
download_stb
download_date
download_firmware_deps

echo ""
echo -e "${GREEN}=== Build environment ready ===${NC}"
echo "Run 'make' to build kernel.img"
