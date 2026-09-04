#!/bin/sh
# Build and stage one immutable Windows desktop-client release in a FILEES-BIN WC.
#
# Mirrors tools/prepare-server-release.sh deliberately, including what it will
# not do: this host holds only the public release key, so nothing here signs
# anything and nothing here touches channels/. Signing and channel promotion
# happen on the signing machine.
#
# Until this existed, a new Windows build reached a machine because somebody
# copied files onto it. That is not a channel, and an alpha that needs a person
# present to ship a fix is not one either.
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
FILEES_BIN_WC="${FILEES_BIN_WC:-$HOME/FILEES-BIN}"
RELEASE_ID="${RELEASE_ID:-}"
SEQUENCE="${SEQUENCE:-}"
SECURITY_EPOCH="${SECURITY_EPOCH:-1}"
CHANNEL="${CHANNEL:-alpha}"
KEY_ID="${KEY_ID:-}"
PLATFORM=windows-amd64
COMPONENT=desktop

die() {
	echo "filees-prepare-client-release: $*" >&2
	exit 1
}

case "$RELEASE_ID" in
	*[!A-Za-z0-9._-]*|'') die "RELEASE_ID must contain only A-Z, a-z, 0-9, dot, underscore or dash" ;;
esac
case "$SEQUENCE" in *[!0-9]*|'') die "SEQUENCE must be a positive integer" ;; esac
case "$SECURITY_EPOCH" in *[!0-9]*|'') die "SECURITY_EPOCH must be a positive integer" ;; esac
case "$KEY_ID" in *[!A-Za-z0-9._-]*|'') die "KEY_ID must name the key that will sign this release" ;; esac
[ "$SEQUENCE" -gt 0 ] || die "SEQUENCE must be greater than zero"
[ -d "$root/.svn" ] || die "source is not an SVN working copy: $root"
[ -d "$FILEES_BIN_WC/.svn" ] || die "not an SVN working copy: $FILEES_BIN_WC"

cd "$root"
[ -z "$(svn status -q)" ] || die "source WC has versioned changes"
svn update --quiet
[ -z "$(svn status -u -q | sed -n '/^[[:space:]]*\*/p')" ] || die "source WC is not at repository HEAD"
source_revision=$(svn info --show-item revision | tr -d '\r\n')
case "$source_revision" in
	*[!0-9]*|'') die "SVN returned an invalid source revision: $source_revision" ;;
esac

cd "$FILEES_BIN_WC"
[ -z "$(svn status)" ] || die "FILEES-BIN WC has local or unversioned changes"
svn update --quiet
release_root="$FILEES_BIN_WC/releases/$RELEASE_ID/$COMPONENT/$PLATFORM"
[ ! -e "$release_root" ] || die "release already exists: $release_root"

cd "$root"
# The client version carries the revision, so a build can always be matched to
# a commit. base+rNNN is what the running client reports; the fourth numeric
# field is what Windows installers can compare, and they must not disagree.
base_version=$(sed -n '1p' "$root/VERSION")
client_version="$base_version.$source_revision"

staging="${DIST:-$root/dist}/client-$PLATFORM"

# One producer for the bundle layout, shared with a local MSI build.
#
# These steps used to live here, which meant the only way to get a bundle was to
# stage a release into a FILEES-BIN working copy - so installing locally meant
# assembling one by hand, and a second copy of the layout is exactly the drift
# the layout test exists to catch.
#
# The revision is passed explicitly rather than left to the script's own lookup:
# a release is built from the revision this script has already checked, not from
# whatever happens to be checked out by the time the build runs.
REVISION="$source_revision" PLATFORM="$PLATFORM" \
	"$root/packaging/build-client-bundle.sh" "$staging" >/dev/null

mkdir -p "$release_root"
bundle="$release_root/filees-client-$PLATFORM.tar.gz"
go run ./cmd/filees-release-bundle -source "$staging" -output "$bundle"

# The channel envelope covers every platform at once. The producer may retain
# another platform only when it already belongs to this exact release identity;
# passing the live channel here therefore fails closed if publishing Windows
# alone would strand an older Linux manifest under the new envelope.
merge=""
if [ -f "$FILEES_BIN_WC/channels/$CHANNEL.v2.json" ]; then
	merge="$FILEES_BIN_WC/channels/$CHANNEL.v2.json"
fi

go run ./cmd/filees-client-release \
	-bundle "$bundle" \
	-component "$COMPONENT" \
	-platform "$PLATFORM" \
	-release-id "$RELEASE_ID" \
	-version "$client_version" \
	-sequence "$SEQUENCE" \
	-security-epoch "$SECURITY_EPOCH" \
	-key-id "$KEY_ID" \
	-release-root "$release_root" \
	-channel-out "$FILEES_BIN_WC/releases/$RELEASE_ID/channel.v2.json" \
	${merge:+-merge-channel "$merge"}

echo
echo "prepared client release $RELEASE_ID ($client_version) from source SVN r$source_revision"
echo "review, then svn add/commit only releases/$RELEASE_ID"
echo "on the signing machine, sign and promote:"
echo "  releases/$RELEASE_ID/$COMPONENT/$PLATFORM/manifest.json -> manifest.json.sig"
echo "  releases/$RELEASE_ID/channel.v2.json                    -> channels/$CHANNEL.v2.json (+ .sig)"
echo "do not change channels/ on this host"
