#!/bin/sh
# Stage one immutable Android companion release in a FILEES-BIN working copy.
#
# This is its own track, like the OpenBSD server: it does not write
# channel.json (that is the server) or channel.v2.json (that is the desktop).
# The candidate is channel-android.json. Nothing here signs, and nothing here
# touches channels/.
#
# The release APK is built elsewhere. This script validates its identity,
# hashes it and writes the unsigned channel candidate. Debug APKs are rejected.
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
FILEES_BIN_WC="${FILEES_BIN_WC:-$HOME/FILEES-BIN}"
RELEASE_ID="${RELEASE_ID:-}"
SEQUENCE="${SEQUENCE:-}"
SECURITY_EPOCH="${SECURITY_EPOCH:-1}"
KEY_ID="${KEY_ID:-}"
APK="${APK:-}"

die() {
	echo "filees-prepare-android-release: $*" >&2
	exit 1
}

case "$RELEASE_ID" in
	*[!A-Za-z0-9._-]*|'') die "RELEASE_ID must contain only A-Z, a-z, 0-9, dot, underscore or dash" ;;
esac
case "$SEQUENCE" in *[!0-9]*|'') die "SEQUENCE must be a positive integer" ;; esac
case "$SECURITY_EPOCH" in *[!0-9]*|'') die "SECURITY_EPOCH must be a positive integer" ;; esac
case "$KEY_ID" in *[!A-Za-z0-9._-]*|'') die "KEY_ID must name the key that will sign this release" ;; esac
[ "$SEQUENCE" -gt 0 ] || die "SEQUENCE must be greater than zero"
[ "$SECURITY_EPOCH" -gt 0 ] || die "SECURITY_EPOCH must be greater than zero"
[ -n "$APK" ] && [ -f "$APK" ] || die "APK must be the built package"
[ -d "$root/.svn" ] || die "source is not an SVN working copy: $root"
[ -d "$FILEES_BIN_WC/.svn" ] || die "not an SVN working copy: $FILEES_BIN_WC"

cd "$root"
[ -z "$(svn status -q)" ] || die "source WC has versioned changes"
svn update --quiet
[ -z "$(svn status -u -q | sed -n '/^[[:space:]]*\*/p')" ] || die "source WC is not at repository HEAD"
source_revision=$(svn info --show-item revision | tr -d '\r\n')
base_version=$(sed -n '1p' "$root/VERSION" | tr -d '\r\n')
# The signed manifest version cannot carry '+'. The APK's own versionName may.
version="$base_version.$source_revision"
# Read the binary AndroidManifest, not the filename or Gradle source. Reject
# stale/debug APKs before making any change in the distribution working copy.
EXPECTED_ANDROID_VERSION="$base_version+r$source_revision" EXPECTED_ANDROID_CODE="$source_revision" \
  sh "$root/tools/check-android-apk.sh" "$APK"
case "$version" in *[!A-Za-z0-9._-]*|'') die "version $version is not a release identifier" ;; esac

cd "$FILEES_BIN_WC"
[ -z "$(svn status)" ] || die "FILEES-BIN WC has local or unversioned changes"
svn update --quiet
release_root="$FILEES_BIN_WC/releases/$RELEASE_ID"
[ ! -e "$release_root" ] || die "release already exists: $release_root (android releases do not share an id with desktop or server)"

apk_name="filees-mobile-$version.apk"
size=$(wc -c <"$APK" | tr -d ' ')
hash=$(sha256sum "$APK" | awk '{print $1}')
case "$hash" in
	*[!0-9a-f]*|'') die "sha256sum did not return a hash" ;;
esac

mkdir -p "$release_root/android"
cp "$APK" "$release_root/android/$apk_name"
cat >"$release_root/android/manifest.json" <<EOF
{
  "schema_version": 1,
  "release_id": "$RELEASE_ID",
  "platform": "android",
  "sequence": $SEQUENCE,
  "security_epoch": $SECURITY_EPOCH,
  "version": "$version",
  "apk": {
    "source": "$apk_name",
    "sha256": "$hash",
    "size": $size
  }
}
EOF
cat >"$release_root/channel-android.json" <<EOF
{
  "schema_version": 1,
  "release_id": "$RELEASE_ID",
  "manifest": "releases/$RELEASE_ID/android/manifest.json",
  "sequence": $SEQUENCE,
  "security_epoch": $SECURITY_EPOCH
}
EOF
printf '%s\n' android >"$release_root/built-for-channel"

echo
echo "prepared android release $RELEASE_ID ($version) from source SVN r$source_revision"
echo "apk $apk_name  sha256 $hash  size $size"
echo "review, then svn add/commit only releases/$RELEASE_ID"
echo "do not change channels/ on this host"
echo "on the signing machine:"
echo "  RELEASE_ID=$RELEASE_ID CHANNEL=android sh tools/release-sign-and-publish.sh"
echo "that promotes channel-android.json to channels/android.json, not the server or desktop channel"
