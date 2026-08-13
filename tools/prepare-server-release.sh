#!/bin/sh
# Build and stage one immutable OpenBSD server release in a FILEES-BIN WC.
# This host receives only the public release key. Signing and channel promotion
# happen later on the dedicated signing machine.
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
FILEES_BIN_WC="${FILEES_BIN_WC:-$HOME/FILEES-BIN}"
RELEASE_ID="${RELEASE_ID:-}"
SEQUENCE="${SEQUENCE:-}"
SECURITY_EPOCH="${SECURITY_EPOCH:-1}"

die() {
	echo "filees-prepare-server-release: $*" >&2
	exit 1
}

case "$RELEASE_ID" in
	*[!A-Za-z0-9._-]*|'') die "RELEASE_ID must contain only A-Z, a-z, 0-9, dot, underscore or dash" ;;
esac
case "$SEQUENCE" in *[!0-9]*|'') die "SEQUENCE must be a positive integer" ;; esac
case "$SECURITY_EPOCH" in *[!0-9]*|'') die "SECURITY_EPOCH must be a positive integer" ;; esac
[ "$SEQUENCE" -gt 0 ] || die "SEQUENCE must be greater than zero"
[ "$SECURITY_EPOCH" -gt 0 ] || die "SECURITY_EPOCH must be greater than zero"
[ -n "${FILEES_RELEASE_PUBKEY:-}" ] || die "FILEES_RELEASE_PUBKEY is required"
[ -f "$FILEES_RELEASE_PUBKEY" ] || die "release public key not found: $FILEES_RELEASE_PUBKEY"
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
[ -z "$(svn status -u -q | sed -n '/^[[:space:]]*\*/p')" ] || die "FILEES-BIN WC is not at repository HEAD"
release_root="$FILEES_BIN_WC/releases/$RELEASE_ID/openbsd-amd64"
[ ! -e "$release_root" ] || die "release already exists: $release_root"

FILEES_RELEASE_PUBKEY="$FILEES_RELEASE_PUBKEY" "$root/packaging/build-server.sh" openbsd-amd64
bundle="${DIST:-$root/dist}/filees-server-openbsd-amd64"
mkdir -p "$release_root/bin" "$release_root/examples"
cp "$bundle"/bin/* "$release_root/bin/"
cp "$bundle/share/filees/install.example.conf" "$release_root/examples/"

cd "$root"
go run ./cmd/filees-release-manifest \
	-spec packaging/server/openbsd-binary-policy.json \
	-payload "$release_root" \
	-output "$release_root/manifest.json" \
	-release-id "$RELEASE_ID" \
	-svn-revision "$source_revision" \
	-sequence "$SEQUENCE" \
	-security-epoch "$SECURITY_EPOCH"

candidate="$FILEES_BIN_WC/releases/$RELEASE_ID/channel-stable.json"
cat >"$candidate" <<EOF
{
  "schema_version": 1,
  "release_id": "$RELEASE_ID",
  "manifest": "releases/$RELEASE_ID/{platform}/manifest.json",
  "svn_revision": "$source_revision",
  "sequence": $SEQUENCE,
  "security_epoch": $SECURITY_EPOCH
}
EOF

echo "prepared immutable release $RELEASE_ID from source SVN r$source_revision"
echo "review, then svn add/commit only releases/$RELEASE_ID; do not change channels/ on this host"
