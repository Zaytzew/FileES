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
mkdir -p "$tmp/bin" "$tmp/share/filees/$goos" "$tmp/share/man/man5" \
	"$tmp/share/man/man7" "$tmp/share/man/man8" "$tmp/$goos"

# Stamp the bundle version into the binaries that can report it. Without this
# the only way to tell how old a deployed tool is was to compare its usage text
# against the source, which is how a stale filees-admin went unnoticed while a
# newly added flag read as "flag provided but not defined".
version=$(sh "$root/packaging/server-build-version.sh" "$root")
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

for command in filees-admin filees-onboard filees-bootstrap-entry filees-operation filees-mail filees-ssh-auth filees-entry filees-worker filees-service-wc-corrector filees-client-entry filees-mobile-v1 filees-recovery-entry filees-public-authority filees-links filees-install filees-rotate filees-serving-state; do
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

cp "$root/packaging/server/capacity-alerts.example.json" "$tmp/share/filees/"
cp "$root/packaging/server/server.example.json" "$tmp/share/filees/"
cp "$root/packaging/server/public-links.example.json" "$tmp/share/filees/"
cp "$root/packaging/server/install.example.conf" "$tmp/share/filees/"
cp "$root/packaging/server/public-storage-migration.md" "$tmp/share/filees/"
if [ "$goos" = openbsd ]; then
	cp "$root/packaging/server/openbsd/bootstrap_authorized_keys" "$tmp/share/filees/openbsd/"
	cp "$root/packaging/server/openbsd/filees-tunnel.login.conf" "$tmp/share/filees/openbsd/"
	cp "$root/packaging/server/openbsd/filees.conf" "$tmp/share/filees/openbsd/"
	cp "$root/packaging/server/openbsd/filees_public_authority" "$tmp/share/filees/openbsd/"
	cp "$root/packaging/server/openbsd/filees_links" "$tmp/share/filees/openbsd/"
	cp "$root/packaging/server/openbsd/passport-reap.crontab.example" "$tmp/share/filees/openbsd/"
	cp "$root/packaging/server/openbsd/lock-requests-reap.crontab.example" "$tmp/share/filees/openbsd/"
	cp "$root/packaging/server/openbsd/public-links.httpd.conf" "$tmp/share/filees/openbsd/"
	cp "$root/packaging/server/openbsd/install-ssh.sh" "$tmp/openbsd/"
	chmod +x "$tmp/openbsd/install-ssh.sh"
else
	# Linux: no sandbox, no rc.d and no web server configuration. The tunnel
	# account authenticates through a PAM module, which is native code and
	# has to be built on Linux with the PAM headers (or supplied prebuilt).
	mkdir -p "$tmp/lib"
	if [ -n "${FILEES_PAM_MODULE:-}" ]; then
		[ -f "$FILEES_PAM_MODULE" ] || { echo "FILEES_PAM_MODULE not found: $FILEES_PAM_MODULE" >&2; exit 2; }
		cp "$FILEES_PAM_MODULE" "$tmp/lib/pam_filees_tunnel.so"
	elif [ "$(uname -s)" = Linux ]; then
		sh "$root/native/filees-pam/build.sh" "$tmp/lib/pam_filees_tunnel.so" >/dev/null
	else
		echo "linux-amd64 needs pam_filees_tunnel.so: build on Linux or set FILEES_PAM_MODULE" >&2
		exit 2
	fi
	pam_magic=$(od -An -tx1 -N4 "$tmp/lib/pam_filees_tunnel.so" | tr -d ' \r\n')
	[ "$pam_magic" = "7f454c46" ] || { echo "pam_filees_tunnel.so is not an ELF object" >&2; exit 2; }
	# The onboarding key is a public capability shared by both platforms.
	cp "$root/packaging/server/openbsd/bootstrap_authorized_keys" "$tmp/share/filees/linux/"
	cp "$root/packaging/server/linux/filees.conf" "$tmp/share/filees/linux/"
	cp "$root/packaging/server/linux/filees-tunnel.pam" "$tmp/share/filees/linux/"
	cp "$root/packaging/server/linux/filees.cil" "$tmp/share/filees/linux/"
	cp "$root/packaging/server/linux/public-links.nginx.conf.example" "$tmp/share/filees/linux/"
	mkdir -p "$tmp/share/filees/linux/systemd"
	cp "$root"/packaging/server/linux/systemd/*.service "$root"/packaging/server/linux/systemd/*.timer "$tmp/share/filees/linux/systemd/"
	cp "$root/packaging/server/linux/install-ssh.sh" "$tmp/linux/"
	chmod +x "$tmp/linux/install-ssh.sh"
	# The example names OpenBSD's ports prefix; Debian and Fedora ship
	# Subversion and ClamAV under /usr/bin. Only the example is rewritten -
	# an existing server.json is never touched by the installers.
	sed -e 's|"/usr/local/bin/svn"|"/usr/bin/svn"|' \
		-e 's|"/usr/local/bin/svnserve"|"/usr/bin/svnserve"|' \
		-e 's|"/usr/local/bin/svnadmin"|"/usr/bin/svnadmin"|' \
		-e 's|"/usr/local/bin/clamdscan"|"/usr/bin/clamdscan"|' \
		"$root/packaging/server/server.example.json" >"$tmp/share/filees/server.example.json"
	if grep -q '/usr/local/bin/' "$tmp/share/filees/server.example.json"; then
		echo "linux server.example.json still names /usr/local/bin" >&2
		exit 2
	fi
	# The updater's own configuration: which manifest to follow and where
	# Subversion lives. login_conf_dir stays; it is unused on Linux.
	sed -e 's|^platform=openbsd-amd64$|platform=linux-amd64|' \
		-e 's|^svn=/usr/local/bin/svn$|svn=/usr/bin/svn|' \
		"$root/packaging/server/install.example.conf" >"$tmp/share/filees/install.example.conf"
	grep -qx 'platform=linux-amd64' "$tmp/share/filees/install.example.conf" && grep -qx 'svn=/usr/bin/svn' "$tmp/share/filees/install.example.conf" || {
		echo "linux install.example.conf was not rewritten" >&2
		exit 2
	}
fi
cp "$root/packaging/server/install-server.sh" "$tmp/"
cp "$root/packaging/server/README.md" "$tmp/"
cp "$root/packaging/server/BACKCHANNEL_UNIX.md" "$tmp/"
if [ -d "$root/docs/man" ]; then
	cp "$root/docs/man/man5/"* "$tmp/share/man/man5/"
	cp "$root/docs/man/man7/"* "$tmp/share/man/man7/"
	cp "$root/docs/man/man8/"* "$tmp/share/man/man8/"
fi
printf '%s\n' "$version" >"$tmp/VERSION"
chmod +x "$tmp/install-server.sh"
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
