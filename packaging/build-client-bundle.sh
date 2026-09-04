#!/bin/sh
# Assembles the Windows client bundle: the layout clientupdate.DirectoryInstaller
# requires, and the layout the MSI is built from.
#
# It exists because there was no way to build one without staging a release into
# a FILEES-BIN working copy. Installing locally therefore meant assembling the
# bundle by hand, which is how a layout ends up defined in somebody's memory and
# then quietly disagreeing with the code that unpacks it.
#
# One producer, two consumers: prepare-client-release-windows.sh calls this and
# then packs and signs, build-msi.ps1 takes the same directory. A second copy of
# these six steps is exactly the drift the layout test exists to catch.
#
#   REVISION=834 packaging/build-client-bundle.sh [output-dir]
#
# REVISION defaults to the working copy's own, which is what any local build
# wants. A release passes it explicitly, because a release is built from a
# revision it has already checked rather than whatever happens to be checked
# out.
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
PLATFORM="${PLATFORM:-windows-amd64}"
out="${1:-${DIST:-$root/dist}/client-$PLATFORM}"

die() {
	echo "filees-build-client-bundle: $*" >&2
	exit 1
}

case "$PLATFORM" in
	windows-amd64) goos=windows; goarch=amd64; daemon=filees.exe; gui=filees-gui-wails.exe ;;
	*) die "unsupported platform: $PLATFORM" ;;
esac

revision="${REVISION:-}"
if [ -z "$revision" ]; then
	revision=$(cd "$root" && svn info --show-item revision 2>/dev/null | tr -d '\r\n') || true
fi
case "$revision" in
	*[!0-9]*|'') die "REVISION must be the numeric SVN revision this bundle is built from" ;;
esac

base_version=$(sed -n '1p' "$root/VERSION")
# Two forms of one number, and they must not be derived twice. base+rNNN is what
# the running client reports; base.NNN is what Windows installers can order.
client_version="$base_version.$revision"
stamp="$base_version+r$revision"

rm -rf "$out"
mkdir -p "$out/bin" "$out/autostart"

cd "$root"
# Only the interface gets -tags production and -H=windowsgui: the tag is a Wails
# convention that drops the dev server and devtools, and the daemon is a console
# program that must keep its console for `filees status` and friends.
GOOS=$goos GOARCH=$goarch go build -trimpath -buildvcs=false \
	-ldflags "-X main.version=$stamp" \
	-o "$out/bin/$daemon" ./cmd/filees
GOOS=$goos GOARCH=$goarch go build -tags production -trimpath -buildvcs=false \
	-ldflags "-H=windowsgui -X main.version=$stamp" \
	-o "$out/bin/$gui" ./cmd/filees-gui-wails

cp "$root/packaging/windows/autostart-supervisor.ps1" "$out/autostart/start-filees.ps1"
cp "$root/packaging/windows/autostart-launch.vbs" "$out/autostart/start-filees.vbs"
printf '%s\n' "$client_version" >"$out/VERSION"

# SHA256SUMS is how a human tells one bundle from another after the fact. The
# installer does not read it; a release nobody can identify afterwards is still
# not a release.
( cd "$out" && find . -type f ! -name SHA256SUMS -print | LC_ALL=C sort | \
	while IFS= read -r file; do
		sha256sum "$file" 2>/dev/null || shasum -a 256 "$file"
	done >SHA256SUMS )

# Verified against the list the installer actually requires, by name.
#
# Not decoration: a build that silently produced nothing - a cross-compile that
# wrote to the wrong place, a rename on one side only - would otherwise be
# packed, signed and published, and the first sign of trouble would be a client
# refusing an update it was just handed.
#
# These are the literal paths clientupdate.RequiredBundleFiles returns, and
# packaging's layout test checks that these two lists still say the same thing.
for required in \
	VERSION \
	SHA256SUMS \
	bin/filees.exe \
	bin/filees-gui-wails.exe \
	autostart/start-filees.ps1 \
	autostart/start-filees.vbs
do
	[ -f "$out/$required" ] || die "bundle is missing $required after the build"
done

echo "$out"
echo "$client_version"
