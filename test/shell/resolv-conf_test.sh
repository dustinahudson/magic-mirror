#!/bin/sh
#
# The resolv.conf hook.
#
# Two separate problems, both invisible until somebody is trying to diagnose a
# network fault by hand on a device nobody can visit: a file BusyBox cannot
# parse, and a DNS timeout long enough to fail a fetch on its own.

. "$(dirname "$0")/lib.sh"

echo "udhcpc resolv.conf hook"

HOOK="$REPO_ROOT/board/overlay/usr/share/udhcpc/default.script.d/10-resolv-conf"

# run_hook ACTION — run the hook against a throwaway resolv.conf.
#
# The hook reads its lease from the environment, exactly as udhcpc invokes it,
# and writes to a path we point at through a temporary HOME-like root.
run_hook() {
	dir="$1"
	action="$2"
	env_dns="$3"
	env_search="$4"
	env_domain="$5"

	# The hook hardcodes /etc/resolv.conf, so run it against a copy with the
	# path rewritten — the logic under test is the content it produces.
	sed "s|^RESOLV_CONF=/etc/resolv.conf|RESOLV_CONF=$dir/resolv.conf|" "$HOOK" > "$dir/hook"
	chmod +x "$dir/hook"

	dns="$env_dns" search="$env_search" domain="$env_domain" \
		sh "$dir/hook" "$action"
}

# --- the file BusyBox could not read ---------------------------------------

it "writes a nameserver line with nothing after the address"
d=$(mktemp -d)
run_hook "$d" bound "192.168.1.1" "" "lan"
line=$(grep '^nameserver' "$d/resolv.conf")
assert_equals "$line" "nameserver 192.168.1.1"
rm -rf "$d"

it "writes a search line with nothing after the domain"
d=$(mktemp -d)
run_hook "$d" bound "192.168.1.1" "" "lan"
assert_equals "$(grep '^search' "$d/resolv.conf")" "search lan"
rm -rf "$d"

# The exact shape that produced "bad address '192.168.1.1 # wlan0'".
it "leaves no trailing comment anywhere in the file"
d=$(mktemp -d)
run_hook "$d" bound "192.168.1.1 8.8.8.8" "lan" "lan"
if grep -qE '^[^#].*#' "$d/resolv.conf"; then
	fail "a line carries a trailing comment:" "$(cat "$d/resolv.conf")"
else
	pass
fi
rm -rf "$d"

it "replaces the tagged lines the stock script wrote rather than adding to them"
d=$(mktemp -d)
printf 'search lan # wlan0\nnameserver 192.168.1.1 # wlan0\n' > "$d/resolv.conf"
run_hook "$d" bound "192.168.1.1" "" "lan"
assert_equals "$(grep -c '# wlan0' "$d/resolv.conf")" "0"
rm -rf "$d"

# --- the ten second stall --------------------------------------------------

# Go defaults to five seconds and two attempts, which is the 10.00s the
# weather fetches failed at. A lookup waits for both A and AAAA, so one
# unanswered record type takes the whole dial deadline with it.
it "bounds how long a lookup may stall"
d=$(mktemp -d)
run_hook "$d" bound "192.168.1.1" "" "lan"
assert_contains "$(cat "$d/resolv.conf")" "options timeout:2 attempts:2"
rm -rf "$d"

# --- leases ----------------------------------------------------------------

it "keeps every nameserver the lease offered"
d=$(mktemp -d)
run_hook "$d" bound "192.168.1.1 1.1.1.1 8.8.8.8" "" ""
assert_equals "$(grep -c '^nameserver' "$d/resolv.conf")" "3"
rm -rf "$d"

it "prefers the RFC 3397 search list over the domain"
d=$(mktemp -d)
run_hook "$d" bound "192.168.1.1" "example.com" "ignored.invalid"
assert_equals "$(grep '^search' "$d/resolv.conf")" "search example.com"
rm -rf "$d"

it "omits the search line when the lease offers neither"
d=$(mktemp -d)
run_hook "$d" bound "192.168.1.1" "" ""
assert_equals "$(grep -c '^search' "$d/resolv.conf")" "0"
rm -rf "$d"

it "rewrites on renew as well as bound"
d=$(mktemp -d)
run_hook "$d" renew "10.0.0.1" "" "home"
assert_equals "$(grep '^nameserver' "$d/resolv.conf")" "nameserver 10.0.0.1"
rm -rf "$d"

# --- not clobbering a working file -----------------------------------------

# A lease with no nameservers must not leave the device with a resolv.conf
# holding nothing but an options line, which resolves nothing at all.
it "leaves the existing file alone when a lease carries no nameservers"
d=$(mktemp -d)
printf 'nameserver 192.168.1.1\n' > "$d/resolv.conf"
run_hook "$d" bound "" "" ""
assert_contains "$(cat "$d/resolv.conf")" "nameserver 192.168.1.1"
rm -rf "$d"

it "ignores actions that are not a lease"
d=$(mktemp -d)
printf 'nameserver 192.168.1.1\n' > "$d/resolv.conf"
run_hook "$d" deconfig "9.9.9.9" "" ""
assert_equals "$(cat "$d/resolv.conf")" "nameserver 192.168.1.1"
rm -rf "$d"

it "leaves no temp file behind"
d=$(mktemp -d)
run_hook "$d" bound "192.168.1.1" "" "lan"
assert_file_missing "$d/resolv.conf.tmp"
rm -rf "$d"

summary
