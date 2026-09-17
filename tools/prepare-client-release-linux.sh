#!/bin/sh
# Build and stage one immutable Linux desktop-client release in a FILEES-BIN WC.
#
# Mirrors tools/prepare-client-release-windows.sh deliberately, including what
# it will not do: this host holds only the public release key, so nothing
# here signs anything and nothing here touches channels/. Signing and channel
# promotion happen on the signing machine, by the owner.
#
# The AppImage plays the role the MSI plays on Windows: kind "installer" in
# the manifest, the easy first download, not the self-update path (the
# tar.gz bundle stays kind "bundle" for that, unchanged).
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
FILEES_BIN_WC="${FILEES_BIN_WC:-$HOME/FILEES-BIN}"
RELEASE_ID="${RELEASE_ID:-}"
SEQUENCE="${SEQUENCE:-}"
SECURITY_EPOCH="${SECURITY_EPOCH:-1}"
CHANNEL="${CHANNEL:-alpha}"
KEY_ID="${KEY_ID:-}"
FILEES_RELEASE_PUBKEY="${FILEES_RELEASE_PUBKEY:-$FILEES_BIN_WC/FILEESrelease.pub}"
FILEES_RELEASE_REPO_URL="${FILEES_RELEASE_REPO_URL:-svn://cloud.atmprojekt.pl/FILEES-BIN}"
PLATFORM=linux-amd64
COMPONENT=desktop

die() {
	echo "filees-prepare-client-release-linux: $*" >&2
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
[ -f "$FILEES_RELEASE_PUBKEY" ] || die "release public key not found: $FILEES_RELEASE_PUBKEY"

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
# Same two forms as Windows, same reason: base+rNNN is what the running
# client reports, base.NNN is the comparable ordering an installer can use.
base_version=$(sed -n '1p' "$root/VERSION")
client_version="$base_version.$source_revision"

staging="${DIST:-$root/dist}/client-$PLATFORM-$RELEASE_ID"

# Same producer as a local build, same layout test guards it.
REVISION="$source_revision" PLATFORM="$PLATFORM" \
	FILEES_RELEASE_PUBKEY="$FILEES_RELEASE_PUBKEY" \
	FILEES_RELEASE_KEY_ID="$KEY_ID" \
	FILEES_RELEASE_REPO_URL="$FILEES_RELEASE_REPO_URL" \
	FILEES_RELEASE_CHANNEL="$CHANNEL" \
	"$root/packaging/build-client-bundle.sh" "$staging" >/dev/null

mkdir -p "$release_root"
cleanup_release() { rm -rf "$release_root"; }
trap cleanup_release EXIT HUP INT TERM
bundle="$release_root/filees-client-$PLATFORM.tar.gz"
go run ./cmd/filees-release-bundle -source "$staging" -output "$bundle"
installer="$release_root/FileES-$client_version-x86_64.AppImage"
command -v appimagetool >/dev/null 2>&1 || [ -n "${FILEES_APPIMAGETOOL:-}" ] || \
	die "appimagetool is required to build the AppImage (set FILEES_APPIMAGETOOL)"
sh "$root/packaging/linux/build-appimage.sh" "$staging" "$installer" >/dev/null
[ -f "$installer" ] || die "AppImage builder did not create $installer"

# Same reasoning as Windows: the channel envelope covers every platform at
# once, so publishing Linux alone must not strand an older Windows manifest.
merge=""
if [ -f "$FILEES_BIN_WC/channels/$CHANNEL.v2.json" ]; then
	merge="$FILEES_BIN_WC/channels/$CHANNEL.v2.json"
fi

go run ./cmd/filees-client-release \
	-bundle "$bundle" \
	-installer "$installer" \
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

trap - EXIT HUP INT TERM

echo
echo "prepared client release $RELEASE_ID ($client_version) from source SVN r$source_revision"
echo "review, then svn add/commit only releases/$RELEASE_ID"
echo "the manifest binds both the self-update bundle and FileES-$client_version-x86_64.AppImage"
echo "on the signing machine, sign and promote:"
echo "  releases/$RELEASE_ID/$COMPONENT/$PLATFORM/manifest.json -> manifest.json.sig"
echo "  releases/$RELEASE_ID/channel.v2.json                    -> channels/$CHANNEL.v2.json (+ .sig)"
echo "do not change channels/ on this host"
