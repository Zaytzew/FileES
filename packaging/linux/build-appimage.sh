#!/bin/sh
# Packages a Linux client bundle (PLATFORM=linux-amd64
# packaging/build-client-bundle.sh) as a single-file AppImage: download,
# double-click, no terminal, no package manager for FileES itself.
#
# It does NOT bundle GTK4 or WebKitGTK. That was tried (linuxdeploy +
# linuxdeploy-plugin-gtk) and live-tested with strace: the GTK plugin
# re-deploys libwebkitgtk-6.0/libjavascriptcoregtk-6.0/libsoup-3.0 on its own
# pass regardless of --exclude-library, and even with those three deleted
# afterward, WebKitGTK's own helper processes (WebKitWebProcess,
# WebKitNetworkProcess) still exec from a hardcoded, non-relocatable
# /usr/libexec/webkitgtk-6.0/ - so bundling the .so next to a foreign
# system's helper binaries is an ABI-mismatch risk, not real portability.
# See reports/HISTORY_NATIVE_LINUX_ACCEPTANCE_2026-09-16.md's predecessor
# spike for the first version of this finding.
#
# The simpler, more honest design that follows from that finding: the daemon
# and GUI binaries carry no rpath (plain `go build` output - verified with
# readelf) and resolve every shared library through the ordinary system
# search path. If the system has webkitgtk6.0 installed (which pulls in
# gtk4), the AppImage needs to bundle nothing at all. That was live-tested
# too, with the plugin-free path: same WebKitWebProcess/WebKitNetworkProcess
# behaviour, no linuxdeploy involved.
#
# So this AppImage carries the plain client bundle unmodified under
# usr/share/filees-appimage/ and a custom AppRun that, like a Windows MSI,
# installs it (the same install-user.sh a manual download would run) the
# first time it is launched, then hands off to the persistently installed
# binary - so a second launch, an autostart, or a systemd restart all reach
# the same, already-self-updating installation, never a copy living inside
# the mounted AppImage.
#
#   packaging/linux/build-appimage.sh <bundle-dir> [output-file]
#
# bundle-dir is the output of PLATFORM=linux-amd64 build-client-bundle.sh.
# Requires appimagetool on PATH (or FILEES_APPIMAGETOOL):
# https://github.com/AppImage/appimagetool
set -eu

die() {
	echo "filees-build-appimage: $*" >&2
	exit 1
}

bundle="${1:?usage: build-appimage.sh <bundle-dir> [output-file]}"
[ -d "$bundle" ] || die "bundle directory not found: $bundle"
[ -f "$bundle/bin/filees-gui" ] || die "bundle has no bin/filees-gui: $bundle"
[ -f "$bundle/bin/filees-svn" ] || die "bundle has no bin/filees-svn: $bundle"
[ -f "$bundle/VERSION" ] || die "bundle has no VERSION: $bundle"
[ -f "$bundle/SHA256SUMS" ] || die "bundle has no SHA256SUMS: $bundle"

root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
version=$(sed -n '1p' "$bundle/VERSION")
out="${2:-${DIST:-$root/dist}/FileES-$version-x86_64.AppImage}"

appimagetool="${FILEES_APPIMAGETOOL:-appimagetool}"
command -v "$appimagetool" >/dev/null 2>&1 || die "appimagetool not found (set FILEES_APPIMAGETOOL): https://github.com/AppImage/appimagetool"

[ ! -e "$out" ] || die "output already exists; choose a fresh path: $out"
mkdir -p "$(dirname -- "$out")"

work=$(mktemp -d "${TMPDIR:-/tmp}/filees-appimage.XXXXXX")
trap 'rm -rf "$work"' EXIT HUP INT TERM
appdir="$work/AppDir"
inner="$appdir/usr/share/filees-appimage"
mkdir -p "$inner" "$appdir/usr/share/applications" "$appdir/usr/share/icons/hicolor/scalable/apps"

# The whole bundle, unmodified: install-user.sh needs SHA256SUMS and every
# file it names right next to it, exactly like a manual download would.
cp -a "$bundle/." "$inner/"

cp "$root/branded-assets/filees-space-symbol-square.svg" "$appdir/filees-gui.svg"
cp "$root/branded-assets/filees-space-symbol-square.svg" \
	"$appdir/usr/share/icons/hicolor/scalable/apps/filees-gui.svg"
sed 's#^Exec=.*#Exec=filees-gui#; s#^TryExec=.*#TryExec=filees-gui#' \
	"$root/packaging/linux/filees-gui.desktop" >"$appdir/filees-gui.desktop"
cp "$appdir/filees-gui.desktop" "$appdir/usr/share/applications/filees-gui.desktop"

cat >"$appdir/AppRun" <<'APPRUN'
#!/bin/sh
# Installer on first launch, like a Windows MSI: after that, every launch
# (a second double-click, an autostart entry, a systemd restart) reaches the
# same persistently installed binary, never a copy inside this AppImage.
set -eu
HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
inner="$HERE/usr/share/filees-appimage"
prefix="${PREFIX:-$HOME/.local}"
config="${XDG_CONFIG_HOME:-$HOME/.config}/filees/config.json"

if [ ! -f "$config" ]; then
	ENABLE_DAEMON=1 ENABLE_AUTOSTART=1 RESTART_DAEMON=0 sh "$inner/install-user.sh"
fi

exec "$prefix/bin/filees-gui" "$@"
APPRUN
chmod 0755 "$appdir/AppRun"

"$appimagetool" "$appdir" "$out" >/dev/null

[ -f "$out" ] || die "appimagetool did not produce $out"
chmod 0755 "$out"
echo "$out"
