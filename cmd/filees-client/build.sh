#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
dist=${DIST:-"$root/dist"}
target=${1:-openbsd-amd64}
case "$target" in
	openbsd-amd64) goos=openbsd; goarch=amd64 ;;
	linux-amd64) goos=linux; goarch=amd64 ;;
	*) echo "usage: $0 [openbsd-amd64|linux-amd64]" >&2; exit 2 ;;
esac

mkdir -p "$dist"
stage=$(mktemp -d "$dist/.filees-client-$target.XXXXXX")
trap 'rm -rf "$stage"' EXIT HUP INT TERM
mkdir -p "$stage/bin" "$stage/share/doc/filees-client" "$stage/share/man/man1"
(
	cd "$root"
	CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -trimpath -buildvcs=false \
		-o "$stage/bin/filees-client" ./cmd/filees-client
)
chmod 755 "$stage/bin/filees-client"
magic=$(od -An -tx1 -N4 "$stage/bin/filees-client" | tr -d ' \r\n')
[ "$magic" = 7f454c46 ] || { echo "filees-client is not ELF" >&2; exit 2; }
metadata=$(go version -m "$stage/bin/filees-client")
printf '%s\n' "$metadata" | grep -F "GOOS=$goos" >/dev/null
printf '%s\n' "$metadata" | grep -F "GOARCH=$goarch" >/dev/null
cp "$root/cmd/filees-client/readme-cli-client.md" "$stage/share/doc/filees-client/README.md"
cp "$root/cmd/filees-client/man/filees-client.1" "$stage/share/man/man1/filees-client.1"
archive="$dist/filees-client-$target.tar.gz"
case "$(uname -s)" in
	MINGW*|MSYS*|CYGWIN*)
		# NTFS does not preserve chmod for extensionless ELF files under
		# Git Bash. Record the executable mode explicitly in GNU tar.
		tar -cf "$stage/payload.tar" --mode=0755 -C "$stage" bin/filees-client
		tar -rf "$stage/payload.tar" --mode=0644 -C "$stage" \
			share/doc/filees-client/README.md share/man/man1/filees-client.1
		gzip -c "$stage/payload.tar" > "$archive"
		;;
	*) tar -czf "$archive" -C "$stage" . ;;
esac
printf '%s\n' "$archive"
