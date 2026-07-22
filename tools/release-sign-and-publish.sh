#!/bin/sh
# Sign every platform manifest referenced by one FileES channel, verify each
# new signature immediately, then commit only detached signatures. Run this
# exclusively on the release-signing machine; the secret key must never be
# copied to a build host, test VM, agent workspace or FILESS-BIN repository.
set -eu

FILESS_BIN_WC="${FILESS_BIN_WC:-$HOME/FILESS-BIN}"
SIGNIFY_BIN="${SIGNIFY_BIN:-signify}"
SIGNIFY_SEC_KEY="${SIGNIFY_SEC_KEY:-$HOME/.signify/filees-release.sec}"
SIGNIFY_PUB_KEY="${SIGNIFY_PUB_KEY:-$HOME/.signify/filees-release.pub}"
CHANNEL="${CHANNEL:-stable}"

die() {
	echo "filees-release-sign: $*" >&2
	exit 1
}

case "$CHANNEL" in
	*[!A-Za-z0-9._-]*|'') die "invalid channel name" ;;
esac

command -v "$SIGNIFY_BIN" >/dev/null 2>&1 || die "signify not found: $SIGNIFY_BIN"
[ -f "$SIGNIFY_SEC_KEY" ] || die "release secret key not found: $SIGNIFY_SEC_KEY"
[ -f "$SIGNIFY_PUB_KEY" ] || die "release public key not found: $SIGNIFY_PUB_KEY"
[ -d "$FILESS_BIN_WC/.svn" ] || die "not an SVN working copy: $FILESS_BIN_WC"

cd "$FILESS_BIN_WC"
[ -z "$(svn status)" ] || die "working copy has local or unversioned changes"
svn update --quiet
[ -z "$(svn status -u | sed -n '/^[[:space:]]*\*/p')" ] || die "working copy is not at repository HEAD"

channel_path="channels/${CHANNEL}.v2.json"
if [ ! -f "$channel_path" ]; then
	channel_path="channels/${CHANNEL}.json"
fi
[ -f "$channel_path" ] || die "channel file not found: $channel_path"
release_id=$(sed -n 's/.*"release_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$channel_path" | head -1)
case "$release_id" in
	*[!A-Za-z0-9._-]*|'') die "invalid or missing release_id in $channel_path" ;;
esac

release_root="releases/${release_id}"
[ -d "$release_root" ] || die "release directory not found: $release_root"

manifests=0
all_signed=true
for manifest_path in "$release_root"/*/manifest.json "$release_root"/*/*/manifest.json; do
	[ -f "$manifest_path" ] || continue
	manifests=$((manifests + 1))
	if [ ! -f "${manifest_path}.sig" ] || ! "$SIGNIFY_BIN" -V -q -p "$SIGNIFY_PUB_KEY" -m "$manifest_path" -x "${manifest_path}.sig"; then
		all_signed=false
	fi
done
[ "$manifests" -gt 0 ] || die "release has no component manifests: $release_root"
if [ ! -f "${channel_path}.sig" ] || ! "$SIGNIFY_BIN" -V -q -p "$SIGNIFY_PUB_KEY" -m "$channel_path" -x "${channel_path}.sig"; then
	all_signed=false
fi
if [ "$all_signed" = true ]; then
	echo "release ${release_id} is already signed and verified for ${manifests} manifest(s)"
	exit 0
fi

tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/filees-release-sign.XXXXXXXX")
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM
signed_paths=""

sign_one() {
	path=$1
	label=$(printf '%s' "$path" | tr '/ ' '__')
	tmp_sig="$tmp_dir/${label}.sig"
	"$SIGNIFY_BIN" -S -s "$SIGNIFY_SEC_KEY" -m "$path" -x "$tmp_sig"
	"$SIGNIFY_BIN" -V -q -p "$SIGNIFY_PUB_KEY" -m "$path" -x "$tmp_sig" \
		|| die "fresh signature does not verify: $path"
	mv "$tmp_sig" "${path}.sig"
	signed_paths="$signed_paths ${path}.sig"
	echo "signed + verified: ${path}.sig"
}

for manifest_path in "$release_root"/*/manifest.json "$release_root"/*/*/manifest.json; do
	[ -f "$manifest_path" ] || continue
	sign_one "$manifest_path"
done
sign_one "$channel_path"

for path in $signed_paths; do
	status=$(svn status -q "$path" | cut -c1)
	if [ "$status" = "?" ]; then
		svn add --quiet "$path"
	fi
done

# shellcheck disable=SC2086 # signed_paths is an internally generated path list.
svn commit $signed_paths -m "Sign FileES release ${release_id} (channel ${CHANNEL})

Detached signify signatures for ${manifests} component/platform manifest(s) and the
channel, verified against the release public key on the signing machine."

echo "done: FileES release ${release_id}, manifests=${manifests}, channel=${CHANNEL}"
