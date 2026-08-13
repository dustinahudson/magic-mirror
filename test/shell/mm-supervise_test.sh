#!/bin/sh
#
# mm-supervise owns the rollback. It is the only thing standing between
# "we shipped a binary that crashes on this hardware" and a mirror that has
# to be taken off a wall — and it had no tests.

. "$(dirname "$0")/lib.sh"

echo "mm-supervise"

# --- counting -------------------------------------------------------------

it "counts a failed start"
BOOT=$(fake_boot 1)
supervise "$BOOT" >/dev/null
assert_equals "$(cat "$BOOT/failures")" "1"
rm -rf "$BOOT"

it "keeps counting across consecutive failures"
BOOT=$(fake_boot 1)
supervise "$BOOT" >/dev/null
supervise "$BOOT" >/dev/null
assert_equals "$(cat "$BOOT/failures")" "2"
rm -rf "$BOOT"

it "clears the counter when the app exits cleanly"
BOOT=$(fake_boot 1)
supervise "$BOOT" >/dev/null   # one failure on the books
BOOT2=$(fake_boot 0)
cp "$BOOT/failures" "$BOOT2/failures"
supervise "$BOOT2" >/dev/null
assert_equals "$(cat "$BOOT2/failures")" "0"
rm -rf "$BOOT" "$BOOT2"

# A clean exit is how the app requests a restart after installing an update.
# Counting it as a failure would roll back the build that just installed.
it "treats a clean exit as a requested restart, not a failure"
BOOT=$(fake_boot 0)
supervise "$BOOT" >/dev/null
assert_equals "$(cat "$BOOT/failures")" "0"
rm -rf "$BOOT"

# The app clears this itself once it has been up long enough. Removing it on
# every start is what makes "never reached healthy" detectable at all.
it "removes a stale health marker before starting"
BOOT=$(fake_boot 1)
echo "healthy at some earlier run" > "$BOOT/health"
supervise "$BOOT" >/dev/null
assert_file_missing "$BOOT/health"
rm -rf "$BOOT"

it "lets a healthy app clear its own counter"
BOOT=$(fake_boot healthy)
supervise "$BOOT" >/dev/null
assert_equals "$(cat "$BOOT/failures")" "0"
rm -rf "$BOOT"

# --- rollback -------------------------------------------------------------

it "does not roll back before the limit"
BOOT=$(fake_boot 1)
printf 'previous binary\n' > "$BOOT/mm.previous"
chmod +x "$BOOT/mm.previous"
echo 1 > "$BOOT/failures"
supervise "$BOOT" >/dev/null
assert_file_exists "$BOOT/mm.previous"
rm -rf "$BOOT"

it "rolls back after three failures that never reached healthy"
BOOT=$(fake_boot 1)
printf '#!/bin/sh\nexit 0\n' > "$BOOT/mm.previous"
chmod +x "$BOOT/mm.previous"
echo 3 > "$BOOT/failures"
out=$(supervise "$BOOT")
assert_contains "$out" "rolling back to previous binary"
rm -rf "$BOOT"

# The bad build is kept rather than deleted. It is the evidence for why the
# device rolled back, and deleting it means the next person has nothing.
it "keeps the failed binary as mm.failed"
BOOT=$(fake_boot 1)
printf '#!/bin/sh\nexit 0\n' > "$BOOT/mm.previous"
chmod +x "$BOOT/mm.previous"
echo 3 > "$BOOT/failures"
supervise "$BOOT" >/dev/null
assert_file_exists "$BOOT/mm.failed"
rm -rf "$BOOT"

# The rolled-back build must start from a clean slate. Inheriting a counter
# already at the limit would roll back again on the very next start — and
# there is nothing left to roll back to.
it "resets the counter after rolling back"
BOOT=$(fake_boot 1)
# A previous build that also fails, so the reset is observable: a clean exit
# would zero the counter for its own reasons and prove nothing.
printf '#!/bin/sh\nexit 1\n' > "$BOOT/mm.previous"
chmod +x "$BOOT/mm.previous"
echo 3 > "$BOOT/failures"
supervise "$BOOT" >/dev/null
# Reset to zero by the rollback, then incremented once by this attempt.
assert_equals "$(cat "$BOOT/failures")" "1"
rm -rf "$BOOT"

# Rolling back to a binary that is not there would leave the device with no
# application at all: strictly worse than a crashing one, which at least
# still serves its web UI between crashes.
it "does not roll back when there is no previous binary"
BOOT=$(fake_boot 1)
echo 5 > "$BOOT/failures"
out=$(supervise "$BOOT")
case "$out" in
  *"rolling back"*) fail "rolled back with no previous binary to roll back to" ;;
  *) assert_file_exists "$BOOT/mm.current" ;;
esac
rm -rf "$BOOT"

# --- degenerate states ----------------------------------------------------

it "survives a missing application"
BOOT=$(fake_boot 1)
rm -f "$BOOT/mm.current"
out=$(supervise "$BOOT")
assert_contains "$out" "no executable"
rm -rf "$BOOT"

# The zero-length file this hardware produces. Read as a number it must not
# make the arithmetic explode, or the supervisor dies before the app starts.
it "survives a zero-length failure counter"
BOOT=$(fake_boot 1)
: > "$BOOT/failures"
supervise "$BOOT" >/dev/null 2>&1
count=$(cat "$BOOT/failures")
case "$count" in
  ''|*[!0-9]*) fail "counter is not a number after an empty read: '$count'" ;;
  *) pass ;;
esac
rm -rf "$BOOT"

it "survives a corrupt failure counter"
BOOT=$(fake_boot 1)
printf 'not a number\n' > "$BOOT/failures"
supervise "$BOOT" >/dev/null 2>&1
assert_file_exists "$BOOT/mm.current"
rm -rf "$BOOT"

# --- logging --------------------------------------------------------------

it "records each attempt in the log"
BOOT=$(fake_boot 1)
supervise "$BOOT" >/dev/null
assert_contains "$(cat "$BOOT/logs/mm.log")" "starting mm.current (attempt 1)"
rm -rf "$BOOT"

it "records the exit status"
BOOT=$(fake_boot 7)
supervise "$BOOT" >/dev/null
assert_contains "$(cat "$BOOT/logs/mm.log")" "exited with status 7"
rm -rf "$BOOT"

# A log that grows without bound fills a FAT partition, and a full partition
# is a mirror that cannot save its config or write its own diagnostics.
it "rotates the log once it gets too big"
BOOT=$(fake_boot 1)
dd if=/dev/zero of="$BOOT/logs/mm.log" bs=1024 count=1100 2>/dev/null
MM_BOOT="$BOOT" MM_RESPAWN_PAUSE=0 MM_NO_BINARY_PAUSE=0 \
  sh "$REPO_ROOT/board/overlay/sbin/mm-supervise" >/dev/null 2>&1
assert_file_exists "$BOOT/logs/mm.log.1"
rm -rf "$BOOT"

it "passes the app the flags it needs to be reachable"
BOOT=$(fake_boot 0)
supervise "$BOOT" >/dev/null
invocation=$(cat "$BOOT/app-invocations")
missing=""
for flag in -config -fb -wifi -watchdog -state -wpa-conf; do
  case "$invocation" in
    *"$flag"*) ;;
    *) missing="$missing $flag" ;;
  esac
done
assert_equals "$missing" ""
rm -rf "$BOOT"

summary
