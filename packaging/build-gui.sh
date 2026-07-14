#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
dist=${DIST:-"$root/dist"}
target=${1:-all}

build_linux() {
	out="$dist/filees-gui-linux-amd64"
	mkdir -p "$out/bin" "$out/share/applications" "$out/share/icons/hicolor/scalable/apps"
	(
		cd "$root"
		CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -o "$out/bin/filees-gui" ./cmd/filees-gui
	)
	cp "$root/packaging/linux/filees-gui.desktop" "$out/share/applications/"
	cp "$root/internal/gui/tray/assets/svg/active.svg" "$out/share/icons/hicolor/scalable/apps/filees-gui.svg"
}

build_windows() {
	out="$dist/filees-gui-windows-amd64"
	mkdir -p "$out"
	(
		cd "$root"
		CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags "-H=windowsgui" -o "$out/filees-gui.exe" ./cmd/filees-gui
	)
	cp "$root/packaging/windows/filees-gui.exe.manifest" "$out/"
	cp "$root/packaging/windows/identity.json" "$out/"
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
