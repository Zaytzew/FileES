#!/bin/sh
# Sign one immutable FileES release and atomically promote its pre-reviewed
# channel candidate. Run exclusively on the release-signing machine; the
# secret key must never be copied to a build host, test VM, agent workspace or
# FILEES-BIN repository.
set -eu

FILEES_BIN_WC="${FILEES_BIN_WC:-$HOME/FILEES-BIN}"
SIGNIFY_BIN="${SIGNIFY_BIN:-signify}"
SIGNIFY_SEC_KEY="${SIGNIFY_SEC_KEY:-$HOME/.signify/filees-release.sec}"
SIGNIFY_PUB_KEY="${SIGNIFY_PUB_KEY:-$HOME/.signify/filees-release.pub}"
RELEASE_ID="${RELEASE_ID:-}"
CHANNEL="${CHANNEL:-}"

die() {
	echo "filees-release-sign: $*" >&2
	exit 1
}

case "$RELEASE_ID" in
	*[!A-Za-z0-9._-]*|'') die "invalid or missing RELEASE_ID" ;;
esac
case "$CHANNEL" in
	alpha|beta|stable) ;;
	'') die "missing CHANNEL (choose alpha, beta or stable)" ;;
	*) die "invalid CHANNEL: $CHANNEL (choose alpha, beta or stable)" ;;
esac

command -v "$SIGNIFY_BIN" >/dev/null 2>&1 || die "signify not found: $SIGNIFY_BIN"
[ -f "$SIGNIFY_SEC_KEY" ] || die "release secret key not found: $SIGNIFY_SEC_KEY"
[ -f "$SIGNIFY_PUB_KEY" ] || die "release public key not found: $SIGNIFY_PUB_KEY"
[ -d "$FILEES_BIN_WC/.svn" ] || die "not an SVN working copy: $FILEES_BIN_WC"

cd "$FILEES_BIN_WC"
[ -z "$(svn status)" ] || die "working copy has local or unversioned changes"
svn update --quiet
[ -z "$(svn status -u -q | sed -n '/^[[:space:]]*\*/p')" ] || die "working copy is not at repository HEAD"

release_root="releases/$RELEASE_ID"
[ -d "$release_root" ] || die "release directory not found: $release_root"
candidate="$release_root/channel.v2.json"
channel_path="channels/${CHANNEL}.v2.json"
if [ ! -f "$candidate" ]; then
	candidate="$release_root/channel.json"
	channel_path="channels/${CHANNEL}.json"
fi
# Compatibility with releases prepared before channel candidates became
# channel-neutral.  A legacy candidate can only promote its named channel.
if [ ! -f "$candidate" ]; then
	candidate="$release_root/channel-${CHANNEL}.v2.json"
	channel_path="channels/${CHANNEL}.v2.json"
fi
if [ ! -f "$candidate" ]; then
	candidate="$release_root/channel-${CHANNEL}.json"
	channel_path="channels/${CHANNEL}.json"
fi
[ -f "$candidate" ] || die "reviewed channel candidate not found under $release_root"
candidate_release=$(sed -n 's/.*"release_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$candidate" | head -1)
[ "$candidate_release" = "$RELEASE_ID" ] || die "candidate release_id does not match RELEASE_ID"

manifests=0
all_manifests_signed=true
for manifest_path in "$release_root"/*/manifest.json "$release_root"/*/*/manifest.json; do
	[ -f "$manifest_path" ] || continue
	manifests=$((manifests + 1))
	if [ ! -f "${manifest_path}.sig" ] || ! "$SIGNIFY_BIN" -V -q -p "$SIGNIFY_PUB_KEY" -m "$manifest_path" -x "${manifest_path}.sig"; then
		all_manifests_signed=false
	fi
done
[ "$manifests" -gt 0 ] || die "release has no component manifests: $release_root"

channel_current=false
if [ -f "$channel_path" ] && cmp -s "$candidate" "$channel_path" &&
	[ -f "${channel_path}.sig" ] &&
	"$SIGNIFY_BIN" -V -q -p "$SIGNIFY_PUB_KEY" -m "$channel_path" -x "${channel_path}.sig"; then
	channel_current=true
fi
if [ "$all_manifests_signed" = true ] && [ "$channel_current" = true ]; then
	echo "release $RELEASE_ID is already signed and promoted on channel $CHANNEL"
	exit 0
fi

tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/filees-release-sign.XXXXXXXX")
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM
commit_paths=""

sign_manifest() {
	path=$1
	sig="${path}.sig"
	if [ -f "$sig" ]; then
		"$SIGNIFY_BIN" -V -q -p "$SIGNIFY_PUB_KEY" -m "$path" -x "$sig" \
			|| die "existing committed signature is invalid: $sig"
		return
	fi
	label=$(printf '%s' "$path" | tr '/ ' '__')
	tmp_sig="$tmp_dir/${label}.sig"
	"$SIGNIFY_BIN" -S -s "$SIGNIFY_SEC_KEY" -m "$path" -x "$tmp_sig"
	"$SIGNIFY_BIN" -V -q -p "$SIGNIFY_PUB_KEY" -m "$path" -x "$tmp_sig" \
		|| die "fresh signature does not verify: $path"
	mv "$tmp_sig" "$sig"
	commit_paths="$commit_paths $sig"
	echo "signed + verified: $sig"
}

for manifest_path in "$release_root"/*/manifest.json "$release_root"/*/*/manifest.json; do
	[ -f "$manifest_path" ] || continue
	sign_manifest "$manifest_path"
done

# The channel is copied only after every immutable manifest has a verified
# signature. The channel document and all new manifest signatures are then one
# SVN commit, so HEAD never points at an unsigned release.
mkdir -p "$(dirname "$channel_path")"
tmp_channel_sig="$tmp_dir/channel.sig"
"$SIGNIFY_BIN" -S -s "$SIGNIFY_SEC_KEY" -m "$candidate" -x "$tmp_channel_sig"
"$SIGNIFY_BIN" -V -q -p "$SIGNIFY_PUB_KEY" -m "$candidate" -x "$tmp_channel_sig" \
	|| die "fresh channel signature does not verify"
cp "$candidate" "$channel_path"
mv "$tmp_channel_sig" "${channel_path}.sig"
"$SIGNIFY_BIN" -V -q -p "$SIGNIFY_PUB_KEY" -m "$channel_path" -x "${channel_path}.sig" \
	|| die "promoted channel signature does not verify"
commit_paths="$commit_paths $channel_path ${channel_path}.sig"

for path in $commit_paths; do
	# Do not use `svn status -q` here: quiet mode hides unversioned signatures,
	# which would leave them outside the atomic promotion commit.
	status=$(svn status "$path" | cut -c1)
	if [ "$status" = "?" ]; then
		svn add --parents --quiet "$path"
	fi
done

# shellcheck disable=SC2086 # commit_paths contains validated generated paths.
svn commit $commit_paths -m "Sign and promote FileES release $RELEASE_ID (channel $CHANNEL)

Detached signify signatures for $manifests component/platform manifest(s), plus
an atomic signed channel promotion verified on the signing machine."

echo "done: FileES release $RELEASE_ID, manifests=$manifests, channel=$CHANNEL"
