#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
dist=${DIST:-"$root/dist"}
target=${1:-all}
version=$(sed -n '1p' "$root/VERSION")
release_ldflags=""

if [ -n "${FILEES_RELEASE_PUBKEY:-}" ] || [ -n "${FILEES_RELEASE_KEY_ID:-}" ]; then
	[ -n "${FILEES_RELEASE_PUBKEY:-}" ] && [ -n "${FILEES_RELEASE_KEY_ID:-}" ] || {
		echo "FILEES_RELEASE_PUBKEY and FILEES_RELEASE_KEY_ID must be set together" >&2
		exit 2
	}
	[ -f "$FILEES_RELEASE_PUBKEY" ] || {
		echo "release public key not found: $FILEES_RELEASE_PUBKEY" >&2
		exit 2
	}
	case "$FILEES_RELEASE_KEY_ID" in
		[!A-Za-z0-9]*|*[!A-Za-z0-9._-]*|'') echo "invalid FILEES_RELEASE_KEY_ID" >&2; exit 2 ;;
	esac
	if grep -Eq 'PLACEHOLDER|xxxx' "$FILEES_RELEASE_PUBKEY"; then
		echo "refusing placeholder release public key" >&2
		exit 2
	fi
	release_pubkey_b64=$(base64 <"$FILEES_RELEASE_PUBKEY" | tr -d '\r\n')
	[ -n "$release_pubkey_b64" ] || {
		echo "release public key is empty" >&2
		exit 2
	}
	release_ldflags="-X main.injectedClientReleasePublicKeyB64=$release_pubkey_b64 -X main.injectedClientReleaseKeyID=$FILEES_RELEASE_KEY_ID"
fi

prepare_output() {
	name=$1
	mkdir -p "$dist"
	mktemp -d "$dist/.${name}.tmp.XXXXXX"
}

publish_output() {
	tmp=$1
	out=$2
	rm -rf "$out"
	mv "$tmp" "$out"
}

build_linux() {
	out="$dist/filees-client-linux-amd64"
	tmp=$(prepare_output "filees-client-linux-amd64")
	trap 'rm -rf "$tmp"' EXIT HUP INT TERM
	mkdir -p "$tmp/bin" "$tmp/share/applications" "$tmp/share/icons/hicolor/scalable/apps" "$tmp/share/systemd/user" "$tmp/share/filees"
	(
		cd "$root"
		CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags "-X main.version=$version $release_ldflags" -o "$tmp/bin/filees" ./cmd/filees
		CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags "-X main.version=$version" -o "$tmp/bin/filees-gui" ./cmd/filees-gui
	)
	(
		# tools/filees-pair-gui is a separate Go module (its own go.mod,
		# QR-encoding dependency) - built from its own directory rather than
		# via the root module's ./cmd/... package paths.
		cd "$root/tools/filees-pair-gui"
		CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags "-X main.version=$version" -o "$tmp/bin/filees-pair-gui" .
	)
	cp "$root/packaging/linux/filees-gui.desktop" "$tmp/share/applications/"
	cp "$root/packaging/linux/filees.service" "$tmp/share/systemd/user/"
	cp "$root/packaging/linux/config.example.json" "$tmp/share/filees/"
	# The static app/launcher icon is the neutral brand mark, not a tray
	# status icon: active.svg's green checkmark badge means "connected", and
	# baking that into the taskbar/launcher icon permanently makes the app
	# look stuck showing one connectivity state forever. Status badges belong
	# only in the dynamic system tray icon (internal/gui/tray).
	cp "$root/branded-assets/filees-space-symbol-square.svg" "$tmp/share/icons/hicolor/scalable/apps/filees-gui.svg"
	cp "$root/packaging/ACCEPTANCE.md" "$tmp/"
	cp "$root/packaging/linux/install-user.sh" "$tmp/"
	cp "$root/packaging/linux/uninstall-user.sh" "$tmp/"
	cp "$root/VERSION" "$tmp/"
	chmod +x "$tmp/install-user.sh" "$tmp/uninstall-user.sh"
	(
		cd "$tmp"
		find . -type f ! -name SHA256SUMS -print | LC_ALL=C sort | xargs sha256sum > SHA256SUMS
	)
	publish_output "$tmp" "$out"
	(
		cd "$root"
		go run ./cmd/filees-release-bundle -source "$out" -output "$out.tar.gz"
	)
	trap - EXIT HUP INT TERM
}

build_windows() {
	out="$dist/filees-gui-windows-amd64"
	tmp=$(prepare_output "filees-gui-windows-amd64")
	trap 'rm -rf "$tmp"' EXIT HUP INT TERM
	(
		cd "$root"
		CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags "-H=windowsgui -X main.version=$version" -o "$tmp/filees-gui.exe" ./cmd/filees-gui
	)
	(
		# -H=windowsgui here too: this is spawned by filees-gui.exe (also
		# windowsgui) as a background helper - without it, launching would
		# briefly flash a console window on Windows.
		cd "$root/tools/filees-pair-gui"
		CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags "-H=windowsgui -X main.version=$version" -o "$tmp/filees-pair-gui.exe" .
	)
	cp "$root/packaging/windows/filees-gui.exe.manifest" "$tmp/"
	cp "$root/packaging/windows/identity.json" "$tmp/"
	cp "$root/packaging/windows/filees-gui.wxs" "$tmp/"
	cp "$root/packaging/windows/License.rtf" "$tmp/"
	cp "$root/packaging/windows/build-msi.ps1" "$tmp/"
	# Same reasoning as the Linux .svg above: the static app icon must not
	# carry a tray status badge.
	cp "$root/branded-assets/filees-space-symbol-square.ico" "$tmp/filees-gui.ico"
	cp "$root/packaging/ACCEPTANCE.md" "$tmp/"
	cp "$root/VERSION" "$tmp/"
	publish_output "$tmp" "$out"
	trap - EXIT HUP INT TERM
}

case "$target" in
	linux-amd64) build_linux ;;
	windows-amd64) build_windows ;;
	all) build_linux; build_windows ;;
	*)
		echo "usage: $0 [linux-amd64|windows-amd64|all]" >&2
		exit 2
		;;
esac
