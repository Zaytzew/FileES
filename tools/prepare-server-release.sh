#!/bin/sh
# Build and stage one immutable server release in a FILEES-BIN WC.
# This host receives only the public release key. Signing and channel promotion
# happen later on the dedicated signing machine.
#
# PLATFORM is what the release carries: "openbsd-amd64" (default) or
# "linux-amd64". One release, one platform: the two servers have separate
# channels (alpha vs alpha-linux; owner, 2026-10-02), so neither waits for the
# other and each has its own sequence and security epoch. A Linux release is
# named r<N>-server-linux and its candidate is channel-linux.json, which the
# signing script promotes only to a *-linux channel. The Linux payload
# contains a native PAM module, so it is built on Linux (or with
# FILEES_PAM_MODULE pointing at a prebuilt pam_filees_tunnel.so).
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
FILEES_BIN_WC="${FILEES_BIN_WC:-$HOME/FILEES-BIN}"
RELEASE_ID="${RELEASE_ID:-}"
SEQUENCE="${SEQUENCE:-}"
SECURITY_EPOCH="${SECURITY_EPOCH:-1}"
[ -z "${PLATFORMS:-}" ] || { echo "filees-prepare-server-release: PLATFORMS is gone; set PLATFORM and prepare one release per platform" >&2; exit 1; }
PLATFORM="${PLATFORM:-openbsd-amd64}"
PLATFORMS=$PLATFORM

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
case "$PLATFORM" in
	openbsd-amd64)
		case "$RELEASE_ID" in *-linux) die "RELEASE_ID $RELEASE_ID names a linux release but PLATFORM is openbsd-amd64" ;; esac
		candidate_name=channel.json
		previous_channel=alpha.json
		;;
	linux-amd64)
		case "$RELEASE_ID" in *-server-linux) ;; *) die "a linux server release is named r<N>-server-linux, not $RELEASE_ID" ;; esac
		candidate_name=channel-linux.json
		previous_channel=alpha-linux.json
		;;
	*) die "unsupported platform: $PLATFORM" ;;
esac
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
for platform in $PLATFORMS; do
	[ ! -e "$FILEES_BIN_WC/releases/$RELEASE_ID" ] || die "release already exists: $FILEES_BIN_WC/releases/$RELEASE_ID"
done

for platform in $PLATFORMS; do
	release_root="$FILEES_BIN_WC/releases/$RELEASE_ID/$platform"
	cd "$root"
	FILEES_SOURCE_REVISION="$source_revision" FILEES_RELEASE_PUBKEY="$FILEES_RELEASE_PUBKEY" "$root/packaging/build-server.sh" "$platform"
	bundle="${DIST:-$root/dist}/filees-server-$platform"
	mkdir -p "$release_root/bin" "$release_root/examples" "$release_root/share/filees"
	cp "$bundle"/bin/* "$release_root/bin/"
	if [ "$platform" = linux-amd64 ]; then
		# The tunnel account's PAM module is part of the signed payload. It is
		# stored under a name Subversion does not ignore: "*.so" is in the
		# default global-ignores, so "svn add" would silently leave the module
		# out of FILEES-BIN and every Linux update would fail on fetching it.
		mkdir -p "$release_root/lib"
		cp "$bundle/lib/pam_filees_tunnel.so" "$release_root/lib/pam_filees_tunnel.module"
	fi
	# Manual pages are part of the signed release, so filees-install upgrades them
	# with the binaries they describe. Their targets are absolute /usr/local/man
	# paths: an installer older than a new target variable would leave it
	# unexpanded and write a relative path.
	for section in 5 7 8; do
		mkdir -p "$release_root/share/man/man$section"
		cp "$bundle/share/man/man$section/"* "$release_root/share/man/man$section/"
	done
	cp "$bundle/share/filees/capacity-alerts.example.json" "$release_root/share/filees/"
	cp "$bundle/share/filees/install.example.conf" "$release_root/examples/"
	cp "$bundle/share/filees/public-storage-migration.md" "$release_root/examples/"

	go run ./cmd/filees-release-manifest \
		-spec "packaging/server/${platform%-amd64}-binary-policy.json" \
		-payload "$release_root" \
		-output "$release_root/manifest.json" \
		-release-id "$RELEASE_ID" \
		-svn-revision "$source_revision" \
		-sequence "$SEQUENCE" \
		-security-epoch "$SECURITY_EPOCH"
done

# A payload file matching Subversion's default global-ignores would be signed
# in the manifest and then never reach the repository.
ignored=$(find "$FILEES_BIN_WC/releases/$RELEASE_ID" \( -name '*.o' -o -name '*.lo' -o -name '*.la' -o -name '*.al' \
	-o -name '.libs' -o -name '*.so' -o -name '*.so.[0-9]*' -o -name '*.a' -o -name '*.pyc' -o -name '*.pyo' \
	-o -name '__pycache__' -o -name '*.rej' -o -name '*~' -o -name '#*#' -o -name '.#*' -o -name '.*.swp' \
	-o -name '.DS_Store' -o -name '[Tt]humbs.db' \) -print)
[ -z "$ignored" ] || die "svn would ignore these payload files, leaving the release incomplete: $ignored"

candidate="$FILEES_BIN_WC/releases/$RELEASE_ID/$candidate_name"
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

COMPONENT=server RELEASE_ID="$RELEASE_ID" SEQUENCE="$SEQUENCE" SECURITY_EPOCH="$SECURITY_EPOCH" SOURCE_REVISION="$source_revision" \
	PREVIOUS_CHANNEL="$FILEES_BIN_WC/channels/$previous_channel" FILEES_BIN_WC="$FILEES_BIN_WC" SOURCE_WC="$root" \
	sh "$root/tools/release-notes-draft.sh"
echo "prepared immutable release $RELEASE_ID ($PLATFORMS) from source SVN r$source_revision"
echo "review, then svn add/commit only releases/$RELEASE_ID; do not change channels/ on this host"
