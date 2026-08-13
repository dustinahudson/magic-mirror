#!/bin/sh
#
# The boot configuration and the init table have to agree about the console.
#
# They did not, and nothing checked. cmdline.txt named ttyAMA0 — the PL011,
# which on a Pi Zero W belongs to Bluetooth — so the kernel bound no console,
# /dev/console had no driver behind it, and the getty inittab respawns could
# not open it. Init did what respawn means and started another. Once a second,
# for the life of the device.
#
# The cost was not the wasted fork. It was the system log: 88% of it was this
# one message, and syslogd rotates at a couple of hundred kilobytes, so the
# real diagnostics were pushed out within hours — from the log the settings
# page offers as the place to look when something is wrong.

. "$(dirname "$0")/lib.sh"

echo "boot configuration"

CMDLINE="$REPO_ROOT/board/boot/cmdline.txt"
CONFIG="$REPO_ROOT/board/boot/config.txt"
INITTAB="$REPO_ROOT/board/overlay/etc/inittab"

console_from_cmdline() {
	tr ' ' '\n' < "$CMDLINE" | sed -n 's/^console=\([^,]*\).*/\1/p' | head -1
}

getty_tty_from_inittab() {
	grep -E '^[a-zA-Z0-9]*::respawn:.*getty' "$INITTAB" | sed -n 's/^\([^:]*\)::.*/\1/p' | head -1
}

it "the kernel console and the getty name the same tty"
assert_equals "$(getty_tty_from_inittab)" "$(console_from_cmdline)"

# ttyAMA0 is the PL011. On this board Bluetooth has it, and the UART on the
# GPIO header is the mini-UART. Naming the wrong one is what caused the loop.
it "does not name ttyAMA0, which this board does not put on the header"
case "$(console_from_cmdline)" in
	ttyAMA0) fail "cmdline.txt points the console at ttyAMA0" ;;
	*) pass ;;
esac

it "the getty does not target the generic console alias"
case "$(getty_tty_from_inittab)" in
	console) fail "inittab respawns a getty on 'console', which resolves only if the kernel bound one" ;;
	'') fail "no getty found in inittab" ;;
	*) pass ;;
esac

# enable_uart is what fixes the core clock so the mini-UART keeps its baud
# rate. Without it the console exists but talks nonsense.
it "the UART is enabled in config.txt"
if grep -qE '^enable_uart=1' "$CONFIG"; then
	pass
else
	fail "config.txt does not set enable_uart=1, so the mini-UART baud rate drifts"
fi

it "every respawn entry names a tty or nothing at all"
bad=""
while read -r line; do
	case "$line" in
		'#'*|'') continue ;;
	esac
	case "$line" in
		*::respawn:*) ;;
		*) continue ;;
	esac
	tty=$(echo "$line" | sed -n 's/^\([^:]*\)::.*/\1/p')
	# An empty field means "no controlling terminal", which is what the
	# application supervisor wants and is always safe.
	[ -z "$tty" ] && continue
	case "$tty" in
		tty*) ;;
		*) bad="$bad $tty" ;;
	esac
done < "$INITTAB"
assert_equals "$bad" ""

summary
