#!/bin/sh
#
# S15clock restores the clock before the network exists. Without it the Pi
# boots at 1970, every TLS certificate reads as "not yet valid", and the
# calendar and weather fail for a reason invisible from the outside — the
# exact shape of problem that ends with the card on a desk.
#
# It also has to survive the zero-length clock file this hardware actually
# produces, without stopping the boot.

. "$(dirname "$0")/lib.sh"

echo "S15clock"

# A stub date on PATH: the real one needs root to set the system clock, and a
# test that changed the developer's clock would be a hostile test.
stub_date() {
  bin="$1"
  mkdir -p "$bin"
  cat > "$bin/date" <<EOF
#!/bin/sh
# Record any attempt to set the clock, then behave plausibly.
for arg in "\$@"; do
  case "\$arg" in
    -s) echo "SET-REQUESTED" >> "$bin/date-calls" ;;
  esac
done
case "\$1" in
  -u)
    shift
    case "\$1" in
      -s) shift; echo "\$1" >> "$bin/set-values"; exit 0 ;;
    esac
    ;;
esac
echo "2026-08-12 12:00:00"
EOF
  chmod +x "$bin/date"
}

run_clock() {
  clock_file="$1"
  action="${2:-start}"
  bin="$(dirname "$clock_file")/bin"
  stub_date "$bin"
  MM_CLOCK="$clock_file" PATH="$bin:$PATH" \
    sh "$REPO_ROOT/board/overlay/etc/init.d/S15clock" "$action" 2>&1
}

# --- the file this hardware produces --------------------------------------

# The failure that was found on a real card. It must be rejected, and it must
# not stop the boot, because everything after this script is the mirror.
it "rejects a zero-length clock without failing the boot"
dir=$(mktemp -d)
: > "$dir/clock"
out=$(run_clock "$dir/clock"); status=$?
if [ "$status" -ne 0 ]; then
  fail "exited $status; a bad clock must not stop the boot"
else
  assert_contains "$out" "malformed"
fi
rm -rf "$dir"

it "never hands a zero-length clock to date"
dir=$(mktemp -d)
: > "$dir/clock"
run_clock "$dir/clock" >/dev/null
assert_file_missing "$dir/bin/set-values"
rm -rf "$dir"

# --- absent and malformed -------------------------------------------------

it "says so when there is no saved time"
dir=$(mktemp -d)
out=$(run_clock "$dir/clock")
assert_contains "$out" "no saved time"
rm -rf "$dir"

it "rejects garbage"
dir=$(mktemp -d)
echo "hello there" > "$dir/clock"
out=$(run_clock "$dir/clock")
assert_contains "$out" "malformed"
rm -rf "$dir"

it "rejects a truncated timestamp"
dir=$(mktemp -d)
printf '20260812' > "$dir/clock"
out=$(run_clock "$dir/clock")
assert_contains "$out" "malformed"
rm -rf "$dir"

it "rejects a timestamp with the seconds missing"
dir=$(mktemp -d)
printf '202608122111\n' > "$dir/clock"
out=$(run_clock "$dir/clock")
assert_contains "$out" "malformed"
rm -rf "$dir"

it "rejects letters inside a well-shaped timestamp"
dir=$(mktemp -d)
printf '2026081221XX.33\n' > "$dir/clock"
out=$(run_clock "$dir/clock")
assert_contains "$out" "malformed"
rm -rf "$dir"

# --- the good path --------------------------------------------------------

it "restores a well-formed timestamp"
dir=$(mktemp -d)
printf '202608122111.33\n' > "$dir/clock"
out=$(run_clock "$dir/clock")
assert_contains "$out" "restored"
rm -rf "$dir"

it "passes the saved value through to date"
dir=$(mktemp -d)
printf '202608122111.33\n' > "$dir/clock"
run_clock "$dir/clock" >/dev/null
assert_contains "$(cat "$dir/bin/set-values" 2>/dev/null)" "202608122111.33"
rm -rf "$dir"

# --- shutdown -------------------------------------------------------------

# The stop path used to truncate in place, which is how a zero-length clock
# gets created in the first place.
it "writes the clock on the way down"
dir=$(mktemp -d)
run_clock "$dir/clock" stop >/dev/null
assert_file_exists "$dir/clock"
rm -rf "$dir"

it "leaves no temp file behind on the boot partition"
dir=$(mktemp -d)
run_clock "$dir/clock" stop >/dev/null
assert_file_missing "$dir/clock.tmp"
rm -rf "$dir"

# An interrupted write must leave the previous good value, not an empty file.
it "keeps the previous clock when the write fails"
dir=$(mktemp -d)
printf '202601010000.00\n' > "$dir/clock"
bin="$dir/bin"; mkdir -p "$bin"
printf '#!/bin/sh\nexit 1\n' > "$bin/date"; chmod +x "$bin/date"
MM_CLOCK="$dir/clock" PATH="$bin:$PATH" \
  sh "$REPO_ROOT/board/overlay/etc/init.d/S15clock" stop >/dev/null 2>&1
assert_contains "$(cat "$dir/clock")" "202601010000.00"
rm -rf "$dir"

summary
