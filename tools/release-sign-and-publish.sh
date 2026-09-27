#!/bin/sh
# Sign one immutable FileES release and atomically promote its pre-reviewed
# channel candidate. Run exclusively on the release-signing machine; the
# secret key must never be copied to a build host, test VM, agent workspace or
# FILEES-BIN repository.
set -eu

tools_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

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

# Publication has its own atomic commit. Retention runs only after it succeeds;
# a retention failure must not be reported as a failed signature/publication.
prune_published_history() {
	if ! FILEES_BIN_WC="$FILEES_BIN_WC" KEEP=5 APPLY=1 sh "$tools_dir/prune-release-history.sh"; then
		die "release is already published; history cleanup failed (rerun the promoter to retry)"
	fi
}

case "$RELEASE_ID" in
	*[!A-Za-z0-9._-]*|'') die "invalid or missing RELEASE_ID" ;;
esac
case "$CHANNEL" in
	alpha|beta|stable|android) ;;
	'') die "missing CHANNEL (choose alpha, beta, stable or android)" ;;
	*) die "invalid CHANNEL: $CHANNEL (choose alpha, beta, stable or android)" ;;
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
# Android is its own track. channel-android.json must never be promoted by
# the server's channel.json rule, and a desktop envelope must never be
# promoted as the phone channel.
if [ "$CHANNEL" = android ]; then
	[ ! -f "$release_root/channel.json" ] && [ ! -f "$release_root/channel.v2.json" ] \
		|| die "android release $RELEASE_ID also carries a server or desktop channel candidate"
	candidate="$release_root/channel-android.json"
	channel_path="channels/android.json"
else
	[ ! -f "$release_root/channel-android.json" ] \
		|| die "release $RELEASE_ID is an android release; sign it with CHANNEL=android"
	candidate="$release_root/channel.v2.json"
	channel_path="channels/${CHANNEL}.v2.json"
fi
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

# A release built for one channel is published on that channel only: alpha
# carries every feature, beta and stable are separate nocfapi builds of the
# same revision (owner's decision, 2026-09-24). Releases prepared before
# built-for-channel existed keep the old promotion rule below.
built_for=""
if [ -f "$release_root/built-for-channel" ]; then
	built_for=$(tr -d ' \r\n' <"$release_root/built-for-channel")
	[ "$built_for" = "$CHANNEL" ] || die "release $RELEASE_ID was built for channel $built_for, not $CHANNEL; build a separate release for $CHANNEL"
fi

# Alpha is where a new payload is signed. Beta/stable otherwise only promote an
# already signed, reviewed artifact: never turn a mistyped CHANNEL into a first
# release of untested binaries. A release built for this very channel is the
# exception - it cannot be anything else. Keep existing signatures byte-for-byte.
if [ "$CHANNEL" != alpha ] && [ "$all_manifests_signed" != true ] && [ "$built_for" != "$CHANNEL" ]; then
	die "$CHANNEL promotion requires existing valid signatures for every manifest; publish and accept the release on alpha first"
fi

channel_current=false
if [ -f "$channel_path" ] && cmp -s "$candidate" "$channel_path" &&
	[ -f "${channel_path}.sig" ] &&
	"$SIGNIFY_BIN" -V -q -p "$SIGNIFY_PUB_KEY" -m "$channel_path" -x "${channel_path}.sig"; then
	channel_current=true
fi
if [ "$all_manifests_signed" = true ] && [ "$channel_current" = true ]; then
	echo "release $RELEASE_ID is already signed and promoted on channel $CHANNEL"
	prune_published_history
	exit 0
fi

tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/filees-release-sign.XXXXXXXX")
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM
commit_paths=""

pending=""

# signify asks for the passphrase once per signature, and a mistyped one used
# to end the run with the earlier signatures already moved into the working
# copy - which then refused every later run as having local changes (owner,
# 2026-09-25). Every signature is now made in $tmp_dir, a mistyped passphrase
# is asked again, and the working copy changes only once all of them exist.
sign_to() {
	message=$1
	out=$2
	attempt=1
	until "$SIGNIFY_BIN" -S -s "$SIGNIFY_SEC_KEY" -m "$message" -x "$out"; do
		[ "$attempt" -lt 3 ] || die "not signed after 3 attempts: $message (working copy unchanged)"
		attempt=$((attempt + 1))
		echo "filees-release-sign: signing failed, enter the passphrase again ($attempt/3)" >&2
	done
	"$SIGNIFY_BIN" -V -q -p "$SIGNIFY_PUB_KEY" -m "$message" -x "$out" \
		|| die "fresh signature does not verify: $message (working copy unchanged)"
}

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
	sign_to "$path" "$tmp_sig"
	pending="$pending $tmp_sig=$sig"
	echo "signed + verified: $sig"
}

for manifest_path in "$release_root"/*/manifest.json "$release_root"/*/*/manifest.json; do
	[ -f "$manifest_path" ] || continue
	sign_manifest "$manifest_path"
done
tmp_channel_sig="$tmp_dir/channel.sig"
sign_to "$candidate" "$tmp_channel_sig"

# Every signature exists and verifies; only now does the working copy change.
# The channel is copied only after every immutable manifest has a verified
# signature. The channel document and all new manifest signatures are then one
# SVN commit, so HEAD never points at an unsigned release.
for pair in $pending; do
	mv "${pair%%=*}" "${pair#*=}"
	commit_paths="$commit_paths ${pair#*=}"
done
mkdir -p "$(dirname "$channel_path")"
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

prune_published_history
