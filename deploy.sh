#!/bin/bash
#
# Deploy Magic Mirror kernel to Raspberry Pi Zero W SD card
#
# Usage:
#   ./deploy.sh [mount_point]    Build and deploy to SD card (default: /media/$USER/boot)
#   ./deploy.sh --build          Build only, don't deploy
#   ./deploy.sh --setup <mount>  Full setup: boot files + firmware + config (for fresh SD cards)
#

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
KERNEL_IMG="$SCRIPT_DIR/kernel.img"
DEPS_DIR="$SCRIPT_DIR/deps"
CONFIG_DIR="$SCRIPT_DIR/config"

# Default values
SD_MOUNT="/media/$USER/boot"

# Firmware sources (pinned revisions for reproducibility)
BOOT_FW_REV="bc7f439c234e19371115e07b57c366df59cc1bc7"
BOOT_FW_URL="https://github.com/raspberrypi/firmware/raw/${BOOT_FW_REV}/boot"
WIFI_FW_REV="c9d3ae6584ab79d19a4f94ccf701e888f9f87a53"
WIFI_FW_URL="https://github.com/RPi-Distro/firmware-nonfree/raw/${WIFI_FW_REV}/debian/config/brcm80211"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

print_usage() {
    echo "Usage: $0 [OPTIONS]"
    echo ""
    echo "Options:"
    echo "  [mount_point]         Build and deploy to SD card (default: $SD_MOUNT)"
    echo "  --setup [mount_point] Full SD card setup (boot firmware + config + kernel)"
    echo "  --build               Build only, don't deploy"
    echo "  --help                Show this help"
}

build_kernel() {
    echo -e "${YELLOW}Building kernel...${NC}"
    cd "$SCRIPT_DIR"
    make

    if [ ! -f "$KERNEL_IMG" ]; then
        echo -e "${RED}Error: Build failed, kernel.img not found${NC}"
        exit 1
    fi

    SIZE=$(stat -c%s "$KERNEL_IMG")
    echo -e "${GREEN}Build complete: kernel.img ($(numfmt --to=iec $SIZE))${NC}"
}

# Download all required firmware to deps/ if missing
download_deps() {
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
        return 0
    fi

    echo -e "${YELLOW}Downloading firmware dependencies...${NC}"

    # Boot firmware from raspberrypi/firmware
    for f in bootcode.bin start.elf fixup.dat bcm2708-rpi-zero-w.dtb; do
        if [ ! -f "$DEPS_DIR/$f" ]; then
            echo "  Downloading $f..."
            wget -q -O "$DEPS_DIR/$f" "${BOOT_FW_URL}/$f"
        fi
    done

    # WiFi firmware from RPi-Distro/firmware-nonfree
    # All chipset variants needed by Circle's WLAN driver
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

# Resolve the block device for a mount point
get_block_device() {
    df --output=source "$1" 2>/dev/null | tail -1
}

# Get the parent disk device for a partition (e.g. /dev/mmcblk0p1 -> /dev/mmcblk0)
get_disk_device() {
    local part="$1"
    echo "$part" | sed -E 's/(mmcblk[0-9]+)p[0-9]+$/\1/; s/(sd[a-z]+)[0-9]+$/\1/'
}

# Get the partition number (e.g. /dev/mmcblk0p1 -> 1, /dev/sda1 -> 1)
get_part_number() {
    local part="$1"
    echo "$part" | grep -oE '[0-9]+$'
}

# Ensure partition is FAT32 with correct MBR type (0x0C).
# Reformats if needed. Updates REMOUNTED_PATH if mount point changes.
ensure_fat32() {
    local mount_point="$1"
    local dev
    dev=$(get_block_device "$mount_point")
    local disk_dev
    disk_dev=$(get_disk_device "$dev")
    local part_num
    part_num=$(get_part_number "$dev")

    local fstype
    fstype=$(df --output=fstype "$mount_point" 2>/dev/null | tail -1)

    # Check MBR partition type via udev
    local part_type
    part_type=$(udevadm info --query=property "$dev" 2>/dev/null | grep ID_PART_ENTRY_TYPE= | cut -d= -f2)

    local needs_format=false
    local needs_part_type=false

    if [ "$fstype" != "vfat" ]; then
        needs_format=true
    fi

    # Pi bootloader requires partition type 0xb (FAT32) or 0xc (FAT32 LBA)
    if [ -n "$part_type" ] && [ "$part_type" != "0xc" ] && [ "$part_type" != "0xb" ]; then
        needs_part_type=true
    fi

    if ! $needs_format && ! $needs_part_type; then
        return 0
    fi

    if $needs_format; then
        echo -e "${YELLOW}SD card at $mount_point is $fstype (need FAT32)${NC}"
    fi
    if $needs_part_type; then
        echo -e "${YELLOW}MBR partition type is $part_type (need 0xc for FAT32 LBA)${NC}"
    fi

    read -p "Fix $dev for Pi boot? ALL DATA WILL BE LOST. [y/N] " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        echo -e "${RED}Cannot deploy: SD card not set up for Pi boot${NC}"
        exit 1
    fi

    sudo umount "$mount_point"

    # Fix MBR partition type to 0x0C (FAT32 LBA)
    if $needs_part_type; then
        echo -e "${YELLOW}Setting partition type to FAT32 LBA (0x0C)...${NC}"
        sudo sfdisk --part-type "$disk_dev" "$part_num" c
    fi

    # Format as FAT32
    echo -e "${YELLOW}Formatting $dev as FAT32...${NC}"
    sudo mkfs.vfat -F 32 -n BOOT "$dev"

    # Remount
    local new_mount
    udisksctl mount -b "$dev" --no-user-interaction 2>/dev/null || true
    # Find where it got mounted
    new_mount=$(udisksctl info -b "$dev" 2>/dev/null | grep MountPoints | awk '{print $2}')
    if [ -z "$new_mount" ] || [ ! -d "$new_mount" ]; then
        new_mount="/media/$USER/BOOT"
    fi

    if [ ! -d "$new_mount" ]; then
        echo -e "${RED}Error: Could not remount SD card after formatting${NC}"
        echo "Try mounting manually: udisksctl mount -b $dev"
        exit 1
    fi

    echo -e "${GREEN}Formatted and remounted at $new_mount${NC}"
    REMOUNTED_PATH="$new_mount"
}

# Copy Pi boot firmware and generate config files
setup_boot_files() {
    local mount_point="$1"

    echo -e "${YELLOW}Setting up boot partition...${NC}"

    # Boot firmware
    for f in bootcode.bin start.elf fixup.dat bcm2708-rpi-zero-w.dtb; do
        cp "$DEPS_DIR/$f" "$mount_point/"
        echo "  Copied $f"
    done

    # config.txt
    cat > "$mount_point/config.txt" << 'EOF'
# Magic Mirror configuration for Raspberry Pi Zero W

# Basic settings
arm_64bit=0
kernel=kernel.img

# HDMI settings
hdmi_force_hotplug=1
hdmi_group=1
hdmi_mode=16
# 16 = 1080p 60Hz

# Disable overscan
disable_overscan=1

# GPU memory
gpu_mem=64

# Enable UART for serial output
enable_uart=1

# WiFi - enable SDIO bus for BCM43430
dtoverlay=sdio
EOF
    echo "  Created config.txt"

    # cmdline.txt
    echo "console=serial0,115200" > "$mount_point/cmdline.txt"
    echo "  Created cmdline.txt"

    # WiFi firmware
    mkdir -p "$mount_point/firmware"
    for f in "$DEPS_DIR"/brcmfmac43*; do
        cp "$f" "$mount_point/firmware/"
        echo "  Copied firmware/$(basename "$f")"
    done

    # App config
    mkdir -p "$mount_point/config"
    cp "$CONFIG_DIR/config.json" "$mount_point/config/"
    echo "  Copied config/config.json"

    # wpa_supplicant.conf
    if [ -f "$SCRIPT_DIR/wpa_supplicant.conf" ]; then
        cp "$SCRIPT_DIR/wpa_supplicant.conf" "$mount_point/"
        echo "  Copied wpa_supplicant.conf"
    elif [ -f "$mount_point/wpa_supplicant.conf" ]; then
        echo "  Using existing wpa_supplicant.conf on SD card"
    else
        echo -e "${YELLOW}  Warning: No wpa_supplicant.conf found${NC}"
        echo "  Copy wpa_supplicant.conf.example to wpa_supplicant.conf and edit it:"
        echo "    cp wpa_supplicant.conf.example wpa_supplicant.conf"
    fi

    echo -e "${GREEN}Boot partition setup complete${NC}"
}

deploy_sd() {
    local mount_point="$1"
    local full_setup="$2"

    # Check if mount point exists
    if [ ! -d "$mount_point" ]; then
        echo -e "${RED}Error: SD card not mounted at $mount_point${NC}"
        echo "Mount your SD card or specify mount point: $0 /path/to/mount"
        exit 1
    fi

    # Ensure the SD card is FAT32
    REMOUNTED_PATH=""
    ensure_fat32 "$mount_point"
    if [ -n "$REMOUNTED_PATH" ]; then
        mount_point="$REMOUNTED_PATH"
    fi

    # On full setup, always copy everything
    if [ "$full_setup" = "true" ]; then
        setup_boot_files "$mount_point"
    else
        # For regular deploy, set up boot files if missing
        if [ ! -f "$mount_point/bootcode.bin" ]; then
            echo -e "${YELLOW}Fresh SD card detected, running full setup...${NC}"
            setup_boot_files "$mount_point"
        fi
    fi

    echo -e "${YELLOW}Deploying kernel...${NC}"
    cp "$KERNEL_IMG" "$mount_point/"
    sync

    echo -e "${GREEN}Deployed to SD card. Safe to eject.${NC}"
}

# Parse arguments
case "${1:-}" in
    --build)
        build_kernel
        ;;
    --setup)
        download_deps
        build_kernel
        deploy_sd "${2:-$SD_MOUNT}" "true"
        ;;
    --help|-h)
        print_usage
        ;;
    "")
        download_deps
        build_kernel
        deploy_sd "$SD_MOUNT"
        ;;
    *)
        download_deps
        build_kernel
        deploy_sd "$1"
        ;;
esac
