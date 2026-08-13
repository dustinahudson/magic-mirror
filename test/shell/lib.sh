#!/bin/sh
#
# A very small test library for the init scripts.
#
# These scripts run before the application exists and decide whether it ever
# starts, so a bug in one of them is always diagnosed the same way: someone
# drives to the mirror and brings the SD card home. That makes them the code
# with the highest cost per defect in the project, and until now the only code
# with no tests at all.
#
# Deliberately plain POSIX sh with no dependencies. The scripts under test run
# under BusyBox ash on the device, so the harness must not require anything
# BusyBox lacks either.

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
export REPO_ROOT

TESTS_RUN=0
TESTS_FAILED=0
CURRENT_TEST=""

red()   { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }

# it NAME — start a test case.
it() {
  CURRENT_TEST="$1"
  TESTS_RUN=$((TESTS_RUN + 1))
}

fail() {
  red "  FAIL: $CURRENT_TEST"
  printf '        %s\n' "$@"
  TESTS_FAILED=$((TESTS_FAILED + 1))
}

pass() {
  printf '  ok   %s\n' "$CURRENT_TEST"
}

assert_equals() {
  if [ "$1" = "$2" ]; then
    pass
  else
    fail "expected: $2" "     got: $1"
  fi
}

assert_contains() {
  case "$1" in
    *"$2"*) pass ;;
    *) fail "expected to contain: $2" "                 got: $1" ;;
  esac
}

assert_file_exists() {
  if [ -f "$1" ]; then
    pass
  else
    fail "expected file to exist: $1"
  fi
}

assert_file_missing() {
  if [ ! -e "$1" ]; then
    pass
  else
    fail "expected file to be gone: $1"
  fi
}

# summary — print totals and set the exit status.
summary() {
  echo
  if [ "$TESTS_FAILED" -eq 0 ]; then
    green "$TESTS_RUN passed"
    return 0
  fi
  red "$TESTS_FAILED of $TESTS_RUN failed"
  return 1
}

# fake_boot — build a throwaway /boot with the given app behaviour.
#
# $1 is the exit status the stub application should return, or "healthy" to
# have it write the health marker and clear the counter the way the real one
# does after sixty seconds.
fake_boot() {
  behaviour="$1"
  dir="$(mktemp -d)"
  mkdir -p "$dir/logs"

  cat > "$dir/mm.current" <<EOF
#!/bin/sh
echo "stub app ran: \$*" >> "$dir/app-invocations"
EOF
  case "$behaviour" in
    healthy)
      cat >> "$dir/mm.current" <<EOF
echo "healthy at now" > "$dir/health"
echo 0 > "$dir/failures"
exit 0
EOF
      ;;
    *)
      echo "exit $behaviour" >> "$dir/mm.current"
      ;;
  esac
  chmod +x "$dir/mm.current"

  echo "$dir"
}

# supervise BOOT_DIR — run mm-supervise against a fake boot partition.
supervise() {
  MM_BOOT="$1" \
  MM_RESPAWN_PAUSE=0 \
  MM_NO_BINARY_PAUSE=0 \
    sh "$REPO_ROOT/board/overlay/sbin/mm-supervise" 2>&1
}

assert_file_exists_dir() {
  if [ -d "$1" ]; then
    pass
  else
    fail "expected directory to exist: $1"
  fi
}
