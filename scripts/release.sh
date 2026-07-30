#!/usr/bin/env bash
#
# Publish a release to GitHub.
#
# Two kinds:
#
#   test    a GitHub pre-release. Only mirrors set to "Test versions" under
#           General -> Software updates will install it.
#   stable  a normal release. Every mirror on the default channel takes it,
#           and so does anything else watching this repo.
#
# Test is the default and stable needs --stable, because the difference
# between them is who finds out when a build is bad.
#
# The pre-release flag is not decoration. Two separate consumers respect it:
#
#   - This app's updater skips pre-releases unless the mirror opted in.
#   - The v1 Circle OS firmware polls /releases/latest, which GitHub defines
#     as excluding pre-releases and drafts, so a test build is invisible to
#     it entirely.
#
# The second is why --stable stops to ask. v1 takes whichever asset the API
# lists first — not the one named kernel.img — and unlinks its existing
# kernel before writing the replacement. A stable v2 release therefore hands
# a v1 device a file that is not a Circle image, having already deleted the
# one that worked. Recovery is pulling the card.
#
set -euo pipefail

usage() {
	cat >&2 <<'EOF'
usage: scripts/release.sh <tag> [--stable] [--os] [--notes <text>]

  <tag>       version to publish, e.g. v0.15.0-rc.1 or v0.15.0
  --stable    publish as a full release rather than a test build
  --os        also build and attach kernel.img (slow; needs Buildroot)
  --notes     release notes body (default: generated from commits)

examples:
  scripts/release.sh v0.15.0-rc.1         # test build, opted-in mirrors only
  scripts/release.sh v0.15.0-rc.2 --os    # test build including a new kernel
  scripts/release.sh v0.15.0 --stable     # everyone
EOF
	exit 2
}

[ $# -ge 1 ] || usage

TAG="$1"
shift
STABLE=0
WITH_OS=0
NOTES=""

while [ $# -gt 0 ]; do
	case "$1" in
	--stable) STABLE=1 ;;
	--os) WITH_OS=1 ;;
	--notes)
		shift
		NOTES="${1:-}"
		;;
	-h | --help) usage ;;
	*)
		echo "unknown option: $1" >&2
		usage
		;;
	esac
	shift
done

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# A tag the updater cannot order is a tag no device will ever install. Reject
# it here rather than discovering it later as silence in the field.
if ! [[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]]; then
	echo "error: tag '$TAG' is not vMAJOR.MINOR.PATCH[-PRERELEASE]" >&2
	exit 1
fi

HAS_SUFFIX=0
[[ "$TAG" == *-* ]] && HAS_SUFFIX=1

# The tag and the flag must agree, or the release's label lies about who gets
# it — which is the whole safety property.
if [ "$STABLE" = 1 ] && [ "$HAS_SUFFIX" = 1 ]; then
	echo "error: $TAG carries a pre-release suffix but --stable was given." >&2
	echo "       Publish it as a test build, or tag it without the suffix." >&2
	exit 1
fi
if [ "$STABLE" = 0 ] && [ "$HAS_SUFFIX" = 0 ]; then
	echo "error: $TAG has no pre-release suffix, so it reads as a release." >&2
	echo "       Use ${TAG}-rc.1 for a test build, or pass --stable deliberately." >&2
	exit 1
fi

command -v gh >/dev/null || {
	echo "error: gh CLI not found" >&2
	exit 1
}
gh auth status >/dev/null 2>&1 || {
	echo "error: gh not authenticated — run 'gh auth login'" >&2
	exit 1
}

if ! git diff-index --quiet HEAD --; then
	echo "error: working tree is not clean; commit or stash first" >&2
	git status --short >&2
	exit 1
fi

REPO="$(gh repo view --json nameWithOwner -q .nameWithOwner)"

if [ "$STABLE" = 1 ]; then
	cat >&2 <<EOF

  Publishing $TAG as a STABLE release of $REPO.

  Every mirror on the default channel installs it within the hour.

  So does any device still running v1 Circle OS — and it will not survive
  the attempt. v1 downloads whichever asset the API lists first, deletes
  its kernel, then writes that file in its place. Recovery is pulling the
  SD card.

  If a v1 device is still in the field, stop and re-image it by hand.

EOF
	read -r -p "  Publish anyway? [y/N] " reply
	[[ "$reply" =~ ^[Yy]$ ]] || {
		echo "aborted." >&2
		exit 1
	}
fi

echo "==> building $TAG"
rm -rf dist
make binary VERSION="$TAG" >/dev/null

NAMES=(magicmirror-armv6)

if [ "$WITH_OS" = 1 ]; then
	echo "==> building kernel (this takes a while)"
	make image VERSION="$TAG" >/dev/null
	cp board/buildroot/output/images/boot/kernel.img dist/kernel.img
	NAMES+=(kernel.img)
fi

# Checksums are what the updater refuses to install without.
echo "==> checksums"
(cd dist && sha256sum "${NAMES[@]}" >SHA256SUMS)
sed 's/^/    /' dist/SHA256SUMS

ASSETS=()
for n in "${NAMES[@]}" SHA256SUMS; do ASSETS+=("dist/$n"); done

if ! git rev-parse "$TAG" >/dev/null 2>&1; then
	echo "==> tagging $TAG"
	git tag -a "$TAG" -m "$TAG"
fi
git push origin "$TAG"

FLAGS=(--title "$TAG")
if [ "$STABLE" = 1 ]; then
	FLAGS+=(--latest)
else
	# The flag that keeps this build away from everyone who did not ask for
	# it, including every v1 device.
	FLAGS+=(--prerelease)
fi
if [ -n "$NOTES" ]; then
	FLAGS+=(--notes "$NOTES")
else
	FLAGS+=(--generate-notes)
fi

echo "==> publishing"
gh release create "$TAG" "${ASSETS[@]}" "${FLAGS[@]}"

echo
if [ "$STABLE" = 1 ]; then
	echo "published $TAG — every mirror on the default channel will take it."
else
	echo "published $TAG as a pre-release."
	echo "Only mirrors set to 'Test versions' under General -> Software updates"
	echo "will install it. Everything else, including v1 devices, ignores it."
fi
