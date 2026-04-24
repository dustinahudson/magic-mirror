#!/usr/bin/env bash
#
# Publish a new release: bump version, build, tag, push, upload kernel.img.
#
# Usage:
#   ./scripts/release.sh              # minor bump (default)
#   ./scripts/release.sh major
#   ./scripts/release.sh patch
#
set -euo pipefail

BUMP="${1:-minor}"
case "$BUMP" in
    major|minor|patch) ;;
    *) echo "usage: $0 [major|minor|patch]" >&2; exit 1 ;;
esac

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_DIR"

command -v gh >/dev/null || { echo "gh CLI is required" >&2; exit 1; }
gh auth status >/dev/null 2>&1 || { echo "gh not authenticated — run 'gh auth login'" >&2; exit 1; }

if ! git diff-index --quiet HEAD --; then
    echo "working tree is not clean; commit or stash first" >&2
    git status --short >&2
    exit 1
fi

CURRENT="$(grep -oE "APP_VERSION='\"v[0-9]+\.[0-9]+\.[0-9]+\"'" Makefile \
           | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+')"
[[ -n "$CURRENT" ]] || { echo "could not find APP_VERSION in Makefile" >&2; exit 1; }

IFS=. read -r MAJ MIN PAT <<< "${CURRENT#v}"
case "$BUMP" in
    major) MAJ=$((MAJ + 1)); MIN=0; PAT=0 ;;
    minor) MIN=$((MIN + 1)); PAT=0 ;;
    patch) PAT=$((PAT + 1)) ;;
esac
NEW="v${MAJ}.${MIN}.${PAT}"

echo "  current: ${CURRENT}"
echo "  new:     ${NEW}  (${BUMP} bump)"
read -r -p "continue? [y/N] " yn
[[ "$yn" =~ ^[Yy]$ ]] || { echo "aborted"; exit 1; }

sed -i "s|APP_VERSION='\"${CURRENT}\"'|APP_VERSION='\"${NEW}\"'|" Makefile

echo "==> building"
./scripts/docker-build.sh make

[[ -f kernel.img ]] || { echo "build did not produce kernel.img" >&2; exit 1; }

echo "==> committing, tagging, pushing"
git add Makefile
git commit -m "version ${NEW#v}"
git tag -a "${NEW}" -m "${NEW}"
git push origin HEAD
git push origin "${NEW}"

echo "==> creating GitHub release"
gh release create "${NEW}" \
    --title "${NEW}" \
    --generate-notes \
    kernel.img

echo "==> done: ${NEW}"
gh release view "${NEW}" --web >/dev/null 2>&1 || true
