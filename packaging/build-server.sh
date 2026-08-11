#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
dist=${DIST:-"$root/dist"}
target=${1:-openbsd-amd64}

case "$target" in
	openbsd-amd64) goos=openbsd; goarch=amd64 ;;
	linux-amd64) goos=linux; goarch=amd64 ;;
	*)
		echo "usage: $0 [openbsd-amd64|linux-amd64]" >&2
		exit 2
		;;
esac

out="$dist/filees-server-$target"
mkdir -p "$dist"
tmp=$(mktemp -d "$dist/.filees-server-$target.tmp.XXXXXX")
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
mkdir -p "$tmp/bin" "$tmp/share/filees/openbsd" "$tmp/openbsd"

# Stamp the bundle version into the binaries that can report it. Without this
# the only way to tell how old a deployed tool is was to compare its usage text
# against the source, which is how a stale filees-admin went unnoticed while a
# newly added flag read as "flag provided but not defined".
version=$(cat "$root/VERSION")

for command in filees-admin filees-onboard filees-bootstrap-entry filees-operation filees-mail filees-ssh-auth filees-entry filees-worker filees-client-entry filees-mobile-v1 filees-recovery-entry filees-public-authority filees-links filees-install filees-rotate; do
	(
		cd "$root"
		CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -trimpath -buildvcs=false \
			-ldflags "-X filees/internal/servertool.adminVersion=$version" \
			-o "$tmp/bin/$command" "./cmd/$command"
	)
done

cp "$root/packaging/server/server.example.json" "$tmp/share/filees/"
cp "$root/packaging/server/public-links.example.json" "$tmp/share/filees/"
cp "$root/packaging/server/openbsd/bootstrap_authorized_keys" "$tmp/share/filees/openbsd/"
cp "$root/packaging/server/openbsd/filees-tunnel.login.conf" "$tmp/share/filees/openbsd/"
cp "$root/packaging/server/openbsd/filees.conf" "$tmp/share/filees/openbsd/"
cp "$root/packaging/server/openbsd/filees_public_authority" "$tmp/share/filees/openbsd/"
cp "$root/packaging/server/openbsd/filees_links" "$tmp/share/filees/openbsd/"
cp "$root/packaging/server/openbsd/public-links.httpd.conf" "$tmp/share/filees/openbsd/"
cp "$root/packaging/server/openbsd/install-ssh.sh" "$tmp/openbsd/"
cp "$root/packaging/server/install-server.sh" "$tmp/"
cp "$root/packaging/server/README.md" "$tmp/"
cp "$root/VERSION" "$tmp/"
chmod +x "$tmp/install-server.sh"
chmod +x "$tmp/openbsd/install-ssh.sh"
(
	cd "$tmp"
	if command -v sha256sum >/dev/null 2>&1; then
		find . -type f ! -name SHA256SUMS -print | LC_ALL=C sort | xargs sha256sum >SHA256SUMS
	else
		find . -type f ! -name SHA256SUMS -print | LC_ALL=C sort | xargs sha256 -r >SHA256SUMS
	fi
)
rm -rf "$out"
mv "$tmp" "$out"
trap - EXIT HUP INT TERM
