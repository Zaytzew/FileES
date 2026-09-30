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

# The channel this release was built for (built-for-channel). The signing
# script refuses to publish it on another one: alpha and beta are two builds
# of one revision, never one build promoted (owner's decision, 2026-09-24).
built_for="$FILEES_BIN_WC/releases/$RELEASE_ID/built-for-channel"
if [ -f "$built_for" ] && [ "$(tr -d ' \r\n' <"$built_for")" != "$CHANNEL" ]; then
	die "release $RELEASE_ID was built for another channel than $CHANNEL"
fi

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
# A platform already staged for this same release is the envelope to extend:
# the live channel still names the previous release, and merging with it would
# be refused rather than combine the two platforms of one release. The producer
# never overwrites an envelope, so the second platform writes a fresh one from
# a copy of the candidate and replaces it only after that succeeded.
candidate="$FILEES_BIN_WC/releases/$RELEASE_ID/channel.v2.json"
merge=""
channel_out="$candidate"
scratch=""
if [ -f "$candidate" ]; then
	scratch=$(mktemp -d "${TMPDIR:-/tmp}/filees-envelope.XXXXXX")
	cp "$candidate" "$scratch/merge.json"
	merge="$scratch/merge.json"
	channel_out="$scratch/channel.v2.json"
elif [ -f "$FILEES_BIN_WC/channels/$CHANNEL.v2.json" ] &&
	grep -q "\"release_id\"[[:space:]]*:[[:space:]]*\"$RELEASE_ID\"" "$FILEES_BIN_WC/channels/$CHANNEL.v2.json"; then
	# Only a live channel that already names this very release is something
	# to extend - that is a resumed release, not a previous one. Merging a
	# channel that names an older release used to work only because the
	# channel then carried a single platform, which this release replaced.
	# Once a release covers two platforms, the same merge strands the other
	# platform's manifest under a new identity and the producer refuses, so
	# the first platform of a new release starts a fresh envelope.
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
	-channel-out "$channel_out" \
	${merge:+-merge-channel "$merge"}

if [ -n "$scratch" ]; then
	mv "$channel_out" "$candidate"
	rm -rf "$scratch"
fi

trap - EXIT HUP INT TERM
printf '%s\n' "$CHANNEL" >"$built_for"

COMPONENT=desktop RELEASE_ID="$RELEASE_ID" SEQUENCE="$SEQUENCE" SECURITY_EPOCH="$SECURITY_EPOCH" SOURCE_REVISION="$source_revision" \
	PREVIOUS_CHANNEL="$FILEES_BIN_WC/channels/$CHANNEL.v2.json" FILEES_BIN_WC="$FILEES_BIN_WC" SOURCE_WC="$root" \
	sh "$root/tools/release-notes-draft.sh"
echo
echo "prepared client release $RELEASE_ID ($client_version) from source SVN r$source_revision"
echo "review, then svn add/commit only releases/$RELEASE_ID"
echo "the manifest binds both the self-update bundle and FileES-$client_version-x86_64.AppImage"
echo "on the signing machine, sign and promote:"
echo "  releases/$RELEASE_ID/$COMPONENT/$PLATFORM/manifest.json -> manifest.json.sig"
echo "  releases/$RELEASE_ID/channel.v2.json                    -> channels/$CHANNEL.v2.json (+ .sig)"
echo "do not change channels/ on this host"
