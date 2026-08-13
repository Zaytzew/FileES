#!/bin/sh
# Populate an empty checkout of a separately created FILEES-BIN repository.
# Repository creation/configuration on the SVN server is intentionally outside
# this script because its filesystem path and ACL policy are deployment-owned.
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
FILEES_BIN_WC="${FILEES_BIN_WC:-$HOME/FILEES-BIN}"

die() {
	echo "filees-init-bin: $*" >&2
	exit 1
}

[ -n "${FILEES_RELEASE_PUBKEY:-}" ] || die "FILEES_RELEASE_PUBKEY is required"
[ -f "$FILEES_RELEASE_PUBKEY" ] || die "release public key not found: $FILEES_RELEASE_PUBKEY"
[ -d "$FILEES_BIN_WC/.svn" ] || die "not an SVN working copy: $FILEES_BIN_WC"
grep -Eq 'PLACEHOLDER|xxxx' "$FILEES_RELEASE_PUBKEY" && die "refusing placeholder release public key"

cd "$FILEES_BIN_WC"
[ -z "$(svn status)" ] || die "working copy has local or unversioned changes"
svn update --quiet
[ -z "$(svn list)" ] || die "repository root is not empty"

mkdir channels releases tools
cp "$FILEES_RELEASE_PUBKEY" FILEESrelease.pub
cp "$root/packaging/filees-bin/README.md" README.md
cp "$root/tools/release-sign-and-publish.sh" tools/
svn add README.md FILEESrelease.pub channels releases tools

echo "initialized FILEES-BIN skeleton in $FILEES_BIN_WC"
echo "review the public key and layout, then commit this initialization"
