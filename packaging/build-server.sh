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

# Fail before producing artifacts when `go` is an interoperability wrapper
# which does not propagate the requested target environment (for example a
# Windows go.exe launched from WSL without WSLENV).  The output directory name
# is part of the release contract, so silently building host binaries here is
# unsafe.
reported_goos=$(CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go env GOOS)
reported_goarch=$(CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go env GOARCH)
[ "$reported_goos" = "$goos" ] || {
	echo "go ignored requested GOOS=$goos (reported $reported_goos)" >&2
	exit 2
}
[ "$reported_goarch" = "$goarch" ] || {
	echo "go ignored requested GOARCH=$goarch (reported $reported_goarch)" >&2
	exit 2
}

verify_binary_target() {
	binary=$1
	command_name=$2
	magic=$(od -An -tx1 -N4 "$binary" | tr -d ' \r\n')
	[ "$magic" = "7f454c46" ] || {
		echo "$command_name: expected ELF for $goos/$goarch, got magic $magic" >&2
		exit 2
	}
	metadata=$(go version -m "$binary")
	printf '%s\n' "$metadata" | grep -F "GOOS=$goos" >/dev/null || {
		echo "$command_name: Go metadata does not report GOOS=$goos" >&2
		exit 2
	}
	printf '%s\n' "$metadata" | grep -F "GOARCH=$goarch" >/dev/null || {
		echo "$command_name: Go metadata does not report GOARCH=$goarch" >&2
		exit 2
	}
}

out="$dist/filees-server-$target"
mkdir -p "$dist"
tmp=$(mktemp -d "$dist/.filees-server-$target.tmp.XXXXXX")
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
mkdir -p "$tmp/bin" "$tmp/share/filees/openbsd" "$tmp/share/man/man5" \
	"$tmp/share/man/man7" "$tmp/share/man/man8" "$tmp/openbsd"

# Stamp the bundle version into the binaries that can report it. Without this
# the only way to tell how old a deployed tool is was to compare its usage text
# against the source, which is how a stale filees-admin went unnoticed while a
# newly added flag read as "flag provided but not defined".
version=$(cat "$root/VERSION")
release_ldflags=""

if [ -n "${FILEES_RELEASE_PUBKEY:-}" ]; then
	[ -f "$FILEES_RELEASE_PUBKEY" ] || {
		echo "release public key not found: $FILEES_RELEASE_PUBKEY" >&2
		exit 2
	}
	if grep -Eq 'PLACEHOLDER|xxxx' "$FILEES_RELEASE_PUBKEY"; then
		echo "refusing placeholder release public key" >&2
		exit 2
	fi
	release_pubkey_b64=$(base64 <"$FILEES_RELEASE_PUBKEY" | tr -d '\r\n')
	[ -n "$release_pubkey_b64" ] || {
		echo "release public key is empty" >&2
		exit 2
	}
	release_ldflags="-X main.injectedServerReleasePublicKeyB64=$release_pubkey_b64"
fi

for command in filees-admin filees-onboard filees-bootstrap-entry filees-operation filees-mail filees-ssh-auth filees-entry filees-worker filees-service-wc-corrector filees-client-entry filees-mobile-v1 filees-recovery-entry filees-public-authority filees-links filees-install filees-rotate; do
	(
		cd "$root"
		command_ldflags="-X filees/internal/servertool.adminVersion=$version"
		if [ "$command" = filees-install ]; then
			command_ldflags="$command_ldflags $release_ldflags"
		fi
		CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -trimpath -buildvcs=false \
			-ldflags "$command_ldflags" \
			-o "$tmp/bin/$command" "./cmd/$command"
		verify_binary_target "$tmp/bin/$command" "$command"
	)
done

cp "$root/packaging/server/server.example.json" "$tmp/share/filees/"
cp "$root/packaging/server/public-links.example.json" "$tmp/share/filees/"
cp "$root/packaging/server/install.example.conf" "$tmp/share/filees/"
cp "$root/packaging/server/openbsd/bootstrap_authorized_keys" "$tmp/share/filees/openbsd/"
cp "$root/packaging/server/openbsd/filees-tunnel.login.conf" "$tmp/share/filees/openbsd/"
cp "$root/packaging/server/openbsd/filees.conf" "$tmp/share/filees/openbsd/"
cp "$root/packaging/server/openbsd/filees_public_authority" "$tmp/share/filees/openbsd/"
cp "$root/packaging/server/openbsd/filees_links" "$tmp/share/filees/openbsd/"
cp "$root/packaging/server/openbsd/public-links.httpd.conf" "$tmp/share/filees/openbsd/"
cp "$root/packaging/server/openbsd/install-ssh.sh" "$tmp/openbsd/"
cp "$root/packaging/server/install-server.sh" "$tmp/"
cp "$root/packaging/server/README.md" "$tmp/"
if [ -d "$root/docs/man" ]; then
	cp "$root/docs/man/man5/"* "$tmp/share/man/man5/"
	cp "$root/docs/man/man7/"* "$tmp/share/man/man7/"
	cp "$root/docs/man/man8/"* "$tmp/share/man/man8/"
fi
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
