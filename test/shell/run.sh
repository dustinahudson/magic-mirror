#!/bin/sh
#
# Run every init-script test.
#
# These scripts run before the application exists, decide whether it ever
# starts, and are the one part of the system where a defect always costs a
# physical trip to the mirror. `make test` covers the Go; this covers the
# code that decides whether the Go ever runs.

set -e
cd "$(dirname "$0")"

failed=0
for t in ./*_test.sh; do
  [ -f "$t" ] || continue
  if ! sh "$t"; then
    failed=1
  fi
  echo
done

if [ "$failed" -ne 0 ]; then
  printf '\033[31mshell tests failed\033[0m\n'
  exit 1
fi
printf '\033[32mall shell tests passed\033[0m\n'
