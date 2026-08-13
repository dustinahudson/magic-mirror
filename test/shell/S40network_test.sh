#!/bin/sh
#
# S40network performs the initial association. If it gets this wrong the
# mirror has no network, which means no web UI, no update channel and no
# logs anyone can read — the complete set of remote lifelines, gone at once.
# Everything after that is a car journey.

. "$(dirname "$0")/lib.sh"

echo "S40network"

# net_fixture — a fake boot partition, a fake sysfs and a bin of stubs.
#
# $1: "present" to make the interface exist in the fake sysfs, anything else
#     to model a radio that never showed up.
net_fixture() {
  dir="$(mktemp -d)"
  mkdir -p "$dir/boot/logs" "$dir/sys" "$dir/bin" "$dir/run"
  if [ "$1" = "present" ]; then
    mkdir -p "$dir/sys/wlan-test"
  fi
  : > "$dir/modules"

  # Every external command the script reaches for, recording its arguments
  # so the test can assert on what the device was actually told to do.
  for cmd in modprobe ip wpa_supplicant udhcpc ntpd usleep killall dmesg sync; do
    cat > "$dir/bin/$cmd" <<EOF
#!/bin/sh
echo "$cmd \$*" >> "$dir/commands"
exit 0
EOF
    chmod +x "$dir/bin/$cmd"
  done

  # ip is special: the clock loop greps its output for an address, and
  # without one it would sit in a thirty second retry loop.
  cat > "$dir/bin/ip" <<EOF
#!/bin/sh
echo "ip \$*" >> "$dir/commands"
case "\$*" in
  *"addr show"*) echo "    inet 192.168.1.50/24 brd 192.168.1.255" ;;
esac
exit 0
EOF
  chmod +x "$dir/bin/ip"

  echo "$dir"
}

net_start() {
  dir="$1"
  MM_BOOT="$dir/boot" \
  MM_SYSNET="$dir/sys" \
  MM_IFACE="wlan-test" \
  MM_MODULES="$dir/modules" \
  MM_RUNTIME_CONF="$dir/run/wpa_supplicant.conf" \
  PATH="$dir/bin:$PATH" \
    sh "$REPO_ROOT/board/overlay/etc/init.d/S40network" "${2:-start}" 2>&1
}

ran() {
  grep -q "^$2" "$1/commands" 2>/dev/null
}

# --- the radio ------------------------------------------------------------

# Nothing else loads the driver. Without this, wlan0 never appears and every
# layer above it fails with no obvious cause.
it "loads the driver when it is not already present"
d=$(net_fixture present)
net_start "$d" >/dev/null
if ran "$d" "modprobe brcmfmac"; then pass; else fail "never ran modprobe"; fi
rm -rf "$d"

it "does not reload a driver that is already loaded"
d=$(net_fixture present)
echo "brcmfmac 123456 0 - Live 0x00000000" > "$d/modules"
net_start "$d" >/dev/null
if ran "$d" "modprobe"; then fail "reloaded a driver that was already live"; else pass; fi
rm -rf "$d"

# The most misleading possible message, and one this script used to print:
# "wlan0 never appeared" while the radio was up and running firmware.
it "reports diagnostics when the interface never appears"
d=$(net_fixture absent)
out=$(net_start "$d")
assert_contains "$out" "never appeared"
rm -rf "$d"

it "dumps module and device state when the interface is missing"
d=$(net_fixture absent)
out=$(net_start "$d")
assert_contains "$out" "net devices:"
rm -rf "$d"

# Boot must continue. This script failing is not a reason for the mirror to
# stop existing — it still has a screen, and it still has a setup portal.
it "does not stop the boot when the interface is missing"
d=$(net_fixture absent)
net_start "$d" >/dev/null 2>&1
assert_equals "$?" "0"
rm -rf "$d"

it "brings the interface up"
d=$(net_fixture present)
net_start "$d" >/dev/null
if ran "$d" "ip link set wlan-test up"; then pass; else fail "never brought the interface up"; fi
rm -rf "$d"

# --- credentials ----------------------------------------------------------

# The path taken on a fresh card. The app watches for exactly this and opens
# the setup portal, so it is a supported state rather than an error.
it "leaves the interface up for provisioning when there are no credentials"
d=$(net_fixture present)
out=$(net_start "$d")
assert_contains "$out" "leaving wlan-test up for provisioning"
rm -rf "$d"

it "does not start a supplicant with no credentials"
d=$(net_fixture present)
net_start "$d" >/dev/null
if ran "$d" "wpa_supplicant"; then fail "started a supplicant with nothing to authenticate with"; else pass; fi
rm -rf "$d"

it "starts the supplicant when credentials exist"
d=$(net_fixture present)
printf 'network={\n\tssid="somewhere"\n}\n' > "$d/boot/wpa_supplicant.conf"
net_start "$d" >/dev/null
if ran "$d" "wpa_supplicant"; then pass; else fail "never started the supplicant"; fi
rm -rf "$d"

# The supplicant reads the runtime copy, not the card. That is what makes a
# credential swap on the card take effect at the next boot and not before —
# behaviour worth pinning down, because a swap that took effect immediately
# would drop the link of whoever was performing it.
it "copies the credentials to the runtime location"
d=$(net_fixture present)
printf 'network={\n\tssid="somewhere"\n}\n' > "$d/boot/wpa_supplicant.conf"
net_start "$d" >/dev/null
assert_file_exists "$d/run/wpa_supplicant.conf"
rm -rf "$d"

it "points the supplicant at the runtime copy rather than the card"
d=$(net_fixture present)
printf 'network={\n\tssid="somewhere"\n}\n' > "$d/boot/wpa_supplicant.conf"
net_start "$d" >/dev/null
assert_contains "$(grep '^wpa_supplicant' "$d/commands")" "$d/run/wpa_supplicant.conf"
rm -rf "$d"

it "asks for an address"
d=$(net_fixture present)
printf 'network={\n\tssid="somewhere"\n}\n' > "$d/boot/wpa_supplicant.conf"
net_start "$d" >/dev/null
if ran "$d" "udhcpc"; then pass; else fail "never started dhcp"; fi
rm -rf "$d"

# --- the log --------------------------------------------------------------

# This log is on the card and survives the power cut that produced it, which
# makes it the only account of a failed boot anyone ever gets to read.
it "writes its account to the boot partition"
d=$(net_fixture present)
net_start "$d" >/dev/null
assert_file_exists "$d/boot/logs/network.log"
rm -rf "$d"

# --- shutdown -------------------------------------------------------------

it "tears the link down on stop"
d=$(net_fixture present)
net_start "$d" stop >/dev/null
if ran "$d" "ip link set wlan-test down"; then pass; else fail "left the interface up"; fi
rm -rf "$d"

it "stops the supplicant and dhcp client on stop"
d=$(net_fixture present)
net_start "$d" stop >/dev/null
if grep -q 'killall' "$d/commands" 2>/dev/null; then pass; else fail "left daemons running"; fi
rm -rf "$d"

# --- usage ----------------------------------------------------------------

it "rejects an unknown action"
d=$(net_fixture present)
net_start "$d" nonsense >/dev/null 2>&1
assert_equals "$?" "1"
rm -rf "$d"

summary
