#!/bin/sh
# Prepare a standalone CLI candidate; signing and publication are separate.
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
FILEES_BIN_WC=${FILEES_BIN_WC:-"$HOME/FILEES-BIN"}
SOURCE_REVISION=${SOURCE_REVISION:-}
KEY_ID=${KEY_ID:-release-2026-a}
NOTES_FILE=${NOTES_FILE:-}
die() { echo "filees-prepare-cli: $*" >&2; exit 1; }
case "$SOURCE_REVISION" in r*) number=${SOURCE_REVISION#r} ;; *) die "SOURCE_REVISION=rNNNN is required" ;; esac
case "$number" in *[!0-9]*|'') die "invalid source revision" ;; esac
[ "$number" -gt 0 ] || die "source revision must be positive"
[ -d "$FILEES_BIN_WC/.svn" ] || die "not a FILEES-BIN working copy"
[ -z "$(svn status "$FILEES_BIN_WC")" ] || die "FILEES-BIN has local changes"
[ -f "$NOTES_FILE" ] || die "NOTES_FILE must name reviewed bilingual release notes"
if [ -d "$root/.svn" ]; then
	[ "$(svnversion "$root")" = "$number" ] || die "source must be a clean, uniform working copy of $SOURCE_REVISION; an exact SVN export also works"
fi
release_id="$SOURCE_REVISION-cli"
[ ! -e "$FILEES_BIN_WC/releases/$release_id" ] || die "refusing to replace an existing release"
version=$(tr -d '\r\n' < "$root/cmd/filees-client/VERSION")
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
stage="$work/$release_id"
mkdir -p "$stage"
cd "$root"
go build -trimpath -buildvcs=false -o "$work/manifests" ./cmd/filees-client-release
go build -trimpath -buildvcs=false -o "$work/notes" ./cmd/filees-release-notes
"$work/notes" lint -release-id "$release_id" -sequence "$number" "$NOTES_FILE"
grep -Eq '"component"[[:space:]]*:[[:space:]]*"cli"' "$NOTES_FILE" || die "notes must belong to cli"
for platform in linux-amd64 openbsd-amd64; do
	SOURCE_REVISION="$SOURCE_REVISION" DIST="$work/dist" sh cmd/filees-client/build.sh "$platform"
	destination="$stage/cli/$platform"
	mkdir -p "$destination"
	cp "$work/dist/filees-client-$platform.tar.gz" "$destination/"
	if [ -f "$stage/channel-cli.json" ]; then
		"$work/manifests" -bundle "$destination/filees-client-$platform.tar.gz" \
			-component cli -platform "$platform" -release-id "$release_id" \
			-version "$version+$SOURCE_REVISION" -sequence "$number" -security-epoch 1 \
			-key-id "$KEY_ID" -release-root "$destination" \
			-merge-channel "$stage/channel-cli.json" -channel-out "$stage/channel-cli-next.json"
		mv "$stage/channel-cli-next.json" "$stage/channel-cli.json"
	else
		"$work/manifests" -bundle "$destination/filees-client-$platform.tar.gz" \
			-component cli -platform "$platform" -release-id "$release_id" \
			-version "$version+$SOURCE_REVISION" -sequence "$number" -security-epoch 1 \
			-key-id "$KEY_ID" -release-root "$destination" -channel-out "$stage/channel-cli.json"
	fi
done
cp "$NOTES_FILE" "$stage/notes.json"
printf 'cli\n' > "$stage/built-for-channel"
# Complete both platforms before making the candidate visible in the WC.
mkdir -p "$FILEES_BIN_WC/releases"
cp -R "$stage" "$FILEES_BIN_WC/releases/"
printf 'Unsigned candidate: %s/releases/%s\n' "$FILEES_BIN_WC" "$release_id"
printf 'Commit it, then on the signing machine: RELEASE_ID=%s CHANNEL=cli sh tools/release-sign-and-publish.sh\n' "$release_id"
