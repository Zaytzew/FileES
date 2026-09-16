#!/bin/sh
# Assembles a client bundle: the layout the matching clientupdate installer
# requires, and (Windows only) the layout the MSI is built from.
#
# It exists because there was no way to build one without staging a release into
# a FILEES-BIN working copy. Installing locally therefore meant assembling the
# bundle by hand, which is how a layout ends up defined in somebody's memory and
# then quietly disagreeing with the code that unpacks it.
#
# One producer, two consumers per platform: a prepare-client-release-*.sh calls
# this and then packs and signs; Windows also has build-msi.ps1 take the same
# directory. A second copy of these steps is exactly the drift the layout test
# exists to catch (packaging/client_bundle_layout_test.go).
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
	linux-amd64) goos=linux; goarch=amd64; daemon=filees; gui=filees-gui ;;
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
release_ldflags=""

# A distribution build contains public routing and verification material, not
# credentials. All four values are atomic: a binary that knows a channel but
# cannot authenticate it (or vice versa) is not an auto-updating build.
if [ -n "${FILEES_RELEASE_PUBKEY:-}" ] || [ -n "${FILEES_RELEASE_KEY_ID:-}" ] || \
	[ -n "${FILEES_RELEASE_REPO_URL:-}" ] || [ -n "${FILEES_RELEASE_CHANNEL:-}" ]; then
	[ -n "${FILEES_RELEASE_PUBKEY:-}" ] && [ -n "${FILEES_RELEASE_KEY_ID:-}" ] && \
		[ -n "${FILEES_RELEASE_REPO_URL:-}" ] && [ -n "${FILEES_RELEASE_CHANNEL:-}" ] || \
		die "FILEES_RELEASE_PUBKEY, FILEES_RELEASE_KEY_ID, FILEES_RELEASE_REPO_URL and FILEES_RELEASE_CHANNEL must be set together"
	[ -f "$FILEES_RELEASE_PUBKEY" ] || die "release public key not found: $FILEES_RELEASE_PUBKEY"
	case "$FILEES_RELEASE_KEY_ID" in [!A-Za-z0-9]*|*[!A-Za-z0-9._-]*|'') die "invalid FILEES_RELEASE_KEY_ID" ;; esac
	case "$FILEES_RELEASE_CHANNEL" in [!A-Za-z0-9]*|*[!A-Za-z0-9._-]*|'') die "invalid FILEES_RELEASE_CHANNEL" ;; esac
	case "$FILEES_RELEASE_REPO_URL" in *[[:space:]]*|'') die "invalid FILEES_RELEASE_REPO_URL" ;; esac
	case "$FILEES_RELEASE_REPO_URL" in svn://*|svn+ssh://*|https://*) ;; *) die "FILEES_RELEASE_REPO_URL must use svn, svn+ssh or https" ;; esac
	grep -Eq 'PLACEHOLDER|xxxx' "$FILEES_RELEASE_PUBKEY" && die "refusing placeholder release public key"
	release_pubkey_b64=$(base64 <"$FILEES_RELEASE_PUBKEY" | tr -d '\r\n')
	[ -n "$release_pubkey_b64" ] || die "release public key is empty"
	release_ldflags="-X main.injectedClientReleasePublicKeyB64=$release_pubkey_b64 -X main.injectedClientReleaseKeyID=$FILEES_RELEASE_KEY_ID -X main.injectedClientReleaseRepoURL=$FILEES_RELEASE_REPO_URL -X main.injectedClientReleaseChannel=$FILEES_RELEASE_CHANNEL"
fi

# Do not recursively erase an arbitrary caller-supplied output path.
[ ! -e "$out" ] || die "output already exists; choose a fresh bundle directory: $out"

cd "$root"

case "$PLATFORM" in
windows-amd64)
	[ -n "${FILEES_NATIVE_RUNTIME:-}" ] || die "FILEES_NATIVE_RUNTIME must name the runtime produced by packaging/windows/stage-native-runtime.ps1"
	[ -d "$FILEES_NATIVE_RUNTIME" ] || die "native runtime directory not found: $FILEES_NATIVE_RUNTIME"
	mkdir -p "$out/bin" "$out/autostart"

	# Embed through an overlay, never overwrite generated assets in the source WC.
	# The old four-file updater and MSI therefore receive the complete runtime in
	# the same daemon image. Runtime extraction uses a content-addressed cache.
	native_build=$(mktemp -d "${TMPDIR:-/tmp}/filees-native-build.XXXXXX")
	trap 'rm -rf "$native_build"' EXIT HUP INT TERM
	go run ./cmd/filees-native-package "$root" "$FILEES_NATIVE_RUNTIME" "$native_build/packed" >/dev/null
	# Only the interface gets -tags production and -H=windowsgui: the tag is a Wails
	# convention that drops the dev server and devtools, and the daemon is a console
	# program that must keep its console for `filees status` and friends.
	GOOS=$goos GOARCH=$goarch go build -tags native_svn_bundle -overlay "$native_build/packed/overlay.json" -trimpath -buildvcs=false \
		-ldflags "-X main.version=$stamp $release_ldflags" \
		-o "$out/bin/$daemon" ./cmd/filees
	GOOS=$goos GOARCH=$goarch go build -tags production -trimpath -buildvcs=false \
		-ldflags "-H=windowsgui -X main.version=$stamp" \
		-o "$out/bin/$gui" ./cmd/filees-gui-wails

	cp "$root/packaging/windows/autostart-supervisor.ps1" "$out/autostart/start-filees.ps1"
	cp "$root/packaging/windows/autostart-launch.vbs" "$out/autostart/start-filees.vbs"
	;;
linux-amd64)
	mkdir -p "$out/bin" "$out/share/icons/hicolor/scalable/apps" "$out/share/applications" \
		"$out/share/systemd/user" "$out/share/filees"

	# Unlike Windows, the native SVN helper is not embedded in the daemon
	# image: it ships as its own file, and the daemon finds it through
	# FILEES_NATIVE_SVN (set by install-user.sh in the systemd unit it
	# generates). "Developer builds and non-Windows releases keep explicit
	# helper selection" - internal/nativeruntime/payload_external.go.
	native_build=$(mktemp -d "${TMPDIR:-/tmp}/filees-native-build.XXXXXX")
	trap 'rm -rf "$native_build"' EXIT HUP INT TERM
	DIST="$native_build" sh "$root/packaging/build-native-svn.sh" >/dev/null
	cp "$native_build/filees-svn" "$out/bin/filees-svn"

	GOOS=$goos GOARCH=$goarch go build -trimpath -buildvcs=false \
		-ldflags "-X main.version=$stamp $release_ldflags" \
		-o "$out/bin/$daemon" ./cmd/filees
	# GTK4/WebKitGTK 6 is the default Wails Linux target; no build tag needed
	# (packaging/build-pair.sh builds the same way). "production" drops the
	# dev server and devtools, same as every other platform.
	GOOS=$goos GOARCH=$goarch go build -tags production -trimpath -buildvcs=false \
		-ldflags "-X main.version=$stamp" \
		-o "$out/bin/$gui" ./cmd/filees-gui-wails

	cp "$root/packaging/linux/install-user.sh" "$out/install-user.sh"
	cp "$root/packaging/linux/uninstall-user.sh" "$out/uninstall-user.sh"
	chmod 0755 "$out/install-user.sh" "$out/uninstall-user.sh"
	cp "$root/packaging/linux/filees-gui.desktop" "$out/share/applications/filees-gui.desktop"
	cp "$root/packaging/linux/filees.service" "$out/share/systemd/user/filees.service"
	cp "$root/packaging/linux/config.example.json" "$out/share/filees/config.example.json"
	# The static app/launcher icon is the neutral brand mark, not a tray status
	# icon: it must not encode connection state, only identify the app.
	cp "$root/branded-assets/filees-space-symbol-square.svg" "$out/share/icons/hicolor/scalable/apps/filees-gui.svg"
	;;
esac

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
# Windows: the literal paths clientupdate.RequiredBundleFiles returns. Linux:
# clientupdate.RequiredLinuxBundleFiles. packaging's layout test checks that
# these lists and this script still say the same thing.
case "$PLATFORM" in
windows-amd64)
	required_list="VERSION SHA256SUMS bin/filees.exe bin/filees-gui-wails.exe autostart/start-filees.ps1 autostart/start-filees.vbs"
	;;
linux-amd64)
	required_list="install-user.sh SHA256SUMS VERSION bin/filees bin/filees-gui bin/filees-svn share/icons/hicolor/scalable/apps/filees-gui.svg share/applications/filees-gui.desktop share/systemd/user/filees.service share/filees/config.example.json"
	;;
esac
for required in $required_list; do
	[ -f "$out/$required" ] || die "bundle is missing $required after the build"
done

echo "$out"
echo "$client_version"
