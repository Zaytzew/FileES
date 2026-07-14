#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
dist=${DIST:-"$root/dist"}
target=${1:-all}
version=$(sed -n '1p' "$root/VERSION")

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
	out="$dist/filees-gui-linux-amd64"
	tmp=$(prepare_output "filees-gui-linux-amd64")
	trap 'rm -rf "$tmp"' EXIT HUP INT TERM
	mkdir -p "$tmp/bin" "$tmp/share/applications" "$tmp/share/icons/hicolor/scalable/apps"
	(
		cd "$root"
		CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags "-X main.version=$version" -o "$tmp/bin/filees-gui" ./cmd/filees-gui
	)
	cp "$root/packaging/linux/filees-gui.desktop" "$tmp/share/applications/"
	cp "$root/internal/gui/tray/assets/svg/active.svg" "$tmp/share/icons/hicolor/scalable/apps/filees-gui.svg"
	cp "$root/packaging/ACCEPTANCE.md" "$tmp/"
	cp "$root/packaging/linux/install-user.sh" "$tmp/"
	cp "$root/packaging/linux/uninstall-user.sh" "$tmp/"
	cp "$root/VERSION" "$tmp/"
	chmod +x "$tmp/install-user.sh" "$tmp/uninstall-user.sh"
	publish_output "$tmp" "$out"
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
	cp "$root/packaging/windows/filees-gui.exe.manifest" "$tmp/"
	cp "$root/packaging/windows/identity.json" "$tmp/"
	cp "$root/packaging/windows/filees-gui.wxs" "$tmp/"
	cp "$root/packaging/windows/build-msi.ps1" "$tmp/"
	cp "$root/internal/gui/tray/assets/windows/active.ico" "$tmp/filees-gui.ico"
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
