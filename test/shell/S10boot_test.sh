#!/bin/sh
#
# S10boot is the first thing that runs. It repairs and mounts the only real
# filesystem on the device — the one holding the kernel, the binary, the
# config and every piece of persistent state. Nothing after it happens if it
# gets this wrong, and nothing after it can report that it did.

. "$(dirname "$0")/lib.sh"

echo "S10boot"

boot_fixture() {
  dir="$(mktemp -d)"
  mkdir -p "$dir/bin" "$dir/mnt"
  for cmd in fsck.vfat mount umount usleep; do
    cat > "$dir/bin/$cmd" <<EOF
#!/bin/sh
echo "$cmd \$*" >> "$dir/commands"
exit 0
EOF
    chmod +x "$dir/bin/$cmd"
  done
  echo "$dir"
}

boot_start() {
  dir="$1"
  MM_BOOT="$dir/mnt" \
  MM_BOOT_DEV="${MM_DEV_OVERRIDE:-/dev/null}" \
  MM_DEV_WAIT=1 \
  PATH="$dir/bin:$PATH" \
    sh "$REPO_ROOT/board/overlay/etc/init.d/S10boot" "${2:-start}" 2>&1
}

ran() {
  grep -q "$2" "$1/commands" 2>/dev/null
}

# --- repair before mount --------------------------------------------------

# The ordering is the whole point. FAT has no journal and this device is
# unplugged rather than shut down, so writing onto an inconsistency left by
# the last power cut is how a file's clusters get orphaned — which is how
# kernel.img once became zero bytes and the mirror stopped booting.
it "repairs the filesystem before mounting it"
d=$(boot_fixture)
boot_start "$d" >/dev/null
fsck_line=$(grep -n 'fsck' "$d/commands" | head -1 | cut -d: -f1)
mount_line=$(grep -n '^mount' "$d/commands" | head -1 | cut -d: -f1)
if [ -n "$fsck_line" ] && [ -n "$mount_line" ] && [ "$fsck_line" -lt "$mount_line" ]; then
  pass
else
  fail "fsck at line '$fsck_line', mount at '$mount_line' — repair must come first"
fi
rm -rf "$d"

it "repairs without asking questions nobody is there to answer"
d=$(boot_fixture)
boot_start "$d" >/dev/null
assert_contains "$(grep fsck "$d/commands")" "-a"
rm -rf "$d"

# --- mount options --------------------------------------------------------

# `flush` is what makes writes hit the card promptly on a device that gets
# unplugged. Losing it would widen every window this project has spent its
# time closing.
it "mounts with flush so writes are not left in cache"
d=$(boot_fixture)
boot_start "$d" >/dev/null
assert_contains "$(grep '^mount' "$d/commands")" "flush"
rm -rf "$d"

it "mounts read-write, or nothing can be saved at all"
d=$(boot_fixture)
boot_start "$d" >/dev/null
assert_contains "$(grep '^mount' "$d/commands")" "rw"
rm -rf "$d"

it "mounts it as vfat"
d=$(boot_fixture)
boot_start "$d" >/dev/null
assert_contains "$(grep '^mount' "$d/commands")" "vfat"
rm -rf "$d"

# --- failure --------------------------------------------------------------

# A mirror that comes up with no config and says so on screen is more useful
# than one that halts, because the screen is the only diagnostic channel
# most of the time.
it "carries on booting when the mount fails"
d=$(boot_fixture)
printf '#!/bin/sh\nexit 1\n' > "$d/bin/mount"; chmod +x "$d/bin/mount"
boot_start "$d" >/dev/null 2>&1
assert_equals "$?" "0"
rm -rf "$d"

it "says so when the mount fails"
d=$(boot_fixture)
printf '#!/bin/sh\nexit 1\n' > "$d/bin/mount"; chmod +x "$d/bin/mount"
out=$(boot_start "$d")
assert_contains "$out" "FAILED"
rm -rf "$d"

# fsck.vfat is absent from some builds. Its absence must not stop the mount,
# or a missing tool becomes a device that never boots.
it "mounts even when no repair tool is installed"
d=$(boot_fixture)
rm -f "$d/bin/fsck.vfat"
# A PATH with the ordinary tools but no fsck.vfat, which lives in sbin. The
# stubs still provide mount, so this isolates the missing-repair-tool branch
# rather than breaking the script's own dependencies.
MM_BOOT="$d/mnt" MM_BOOT_DEV=/dev/null MM_DEV_WAIT=1 PATH="$d/bin:/usr/bin:/bin" \
  sh "$REPO_ROOT/board/overlay/etc/init.d/S10boot" start >/dev/null 2>&1
if ran "$d" "^mount"; then pass; else fail "no repair tool meant no mount either"; fi
rm -rf "$d"

# --- the log directory ----------------------------------------------------

# Everything downstream appends to logs/ on the card. If it does not exist,
# the record of a failed boot is lost — which is the record that decides
# whether anyone has to drive anywhere.
it "creates the log directory"
d=$(boot_fixture)
boot_start "$d" >/dev/null
assert_file_exists_dir "$d/mnt/logs"
rm -rf "$d"

# --- log rotation ---------------------------------------------------------

# Nothing rotated the network log, and dropbear appends a line per connection
# attempt to it for as long as the device runs. On the partition that also
# holds the configuration and the kernel, a log nobody bounds is a mirror that
# eventually cannot save its own settings.
it "rotates a log that has grown too large"
d=$(boot_fixture)
mkdir -p "$d/mnt/logs"
dd if=/dev/zero of="$d/mnt/logs/network.log" bs=1024 count=1100 2>/dev/null
boot_start "$d" >/dev/null
assert_file_exists "$d/mnt/logs/network.log.1"
rm -rf "$d"

it "leaves a small log alone"
d=$(boot_fixture)
mkdir -p "$d/mnt/logs"
echo "a few lines" > "$d/mnt/logs/network.log"
boot_start "$d" >/dev/null
assert_file_missing "$d/mnt/logs/network.log.1"
rm -rf "$d"

it "rotates every log, not just the first"
d=$(boot_fixture)
mkdir -p "$d/mnt/logs"
dd if=/dev/zero of="$d/mnt/logs/network.log" bs=1024 count=1100 2>/dev/null
dd if=/dev/zero of="$d/mnt/logs/mm.log" bs=1024 count=1100 2>/dev/null
boot_start "$d" >/dev/null
if [ -f "$d/mnt/logs/network.log.1" ] && [ -f "$d/mnt/logs/mm.log.1" ]; then
  pass
else
  fail "not every oversized log was rotated"
fi
rm -rf "$d"

# --- shutdown and usage ---------------------------------------------------

it "unmounts on stop"
d=$(boot_fixture)
boot_start "$d" stop >/dev/null
if ran "$d" "umount"; then pass; else fail "never unmounted"; fi
rm -rf "$d"

it "rejects an unknown action"
d=$(boot_fixture)
boot_start "$d" nonsense >/dev/null 2>&1
assert_equals "$?" "1"
rm -rf "$d"

summary
