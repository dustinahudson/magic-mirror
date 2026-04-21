#!/bin/bash
#
# Run init.sh / make / any command inside the magic-mirror build container.
#
# Usage:
#   ./scripts/docker-build.sh              # runs: ./init.sh && make
#   ./scripts/docker-build.sh make clean   # runs an arbitrary command
#   ./scripts/docker-build.sh bash         # interactive shell
#
# The source tree is bind-mounted at /src, so lib/ and deps/ persist on the host.
#

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
IMAGE_NAME="magic-mirror-build"

# Build the image if it's missing.
if ! docker image inspect "$IMAGE_NAME" > /dev/null 2>&1; then
    echo "Building $IMAGE_NAME image..."
    docker build -t "$IMAGE_NAME" "$PROJECT_DIR"
fi

if [ $# -eq 0 ]; then
    set -- bash -c './init.sh && make'
fi

# Only allocate a TTY when one is available (avoids failure when piped/CI).
TTY_FLAGS="-i"
if [ -t 0 ] && [ -t 1 ]; then
    TTY_FLAGS="-it"
fi

exec docker run --rm $TTY_FLAGS \
    -v "$PROJECT_DIR":/src \
    -w /src \
    -u "$(id -u):$(id -g)" \
    "$IMAGE_NAME" \
    "$@"
