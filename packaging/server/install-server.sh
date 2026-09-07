#!/bin/sh
set -eu

bundle=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
prefix=${PREFIX:-/usr/local}
sysconfdir=${SYSCONFDIR:-/etc/filees}
statedir=${STATEDIR:-/var/filees/onboarding}
public_downloads_dir=${PUBLIC_DOWNLOADS_DIR:-/var/filees-downloads}
public_authority_staging_root=${PUBLIC_AUTHORITY_STAGING_ROOT:-$public_downloads_dir/authority}
public_links_cache_root=${PUBLIC_LINKS_CACHE_ROOT:-$public_downloads_dir/cache}
for public_path in "$public_downloads_dir" "$public_authority_staging_root" "$public_links_cache_root"; do
	case "$public_path" in
		/|/tmp|/tmp/*|/var/tmp|/var/tmp/*) echo "Public downloads require dedicated persistent directories" >&2; exit 1 ;;
		/*) ;;
		*) echo "Public downloads paths must be absolute" >&2; exit 1 ;;
	esac
done

install -d -m 755 "$prefix/sbin" "$prefix/libexec/filees"
install -d -m 755 "$prefix/man/man5" "$prefix/man/man7" "$prefix/man/man8"
if [ -d "$bundle/share/man" ]; then
	for section in 5 7 8; do
		if [ -d "$bundle/share/man/man$section" ]; then
			for page in "$bundle/share/man/man$section"/*; do
				[ -f "$page" ] || continue
				install -m 0444 "$page" "$prefix/man/man$section/"
			done
		fi
	done
fi
install -m 0555 "$bundle/bin/filees-admin" "$prefix/sbin/filees-admin"
install -m 0555 "$bundle/bin/filees-operation" "$prefix/sbin/filees-operation"
install -m 0555 "$bundle/bin/filees-install" "$prefix/sbin/filees-install"
install -m 0555 "$bundle/bin/filees-rotate" "$prefix/sbin/filees-rotate"
install -m 0555 "$bundle/bin/filees-onboard" "$prefix/libexec/filees/filees-onboard"
install -m 0555 "$bundle/bin/filees-bootstrap-entry" "$prefix/libexec/filees/filees-bootstrap-entry"
install -m 0555 "$bundle/bin/filees-mail" "$prefix/libexec/filees/filees-mail"
install -m 0555 "$bundle/bin/filees-entry" "$prefix/libexec/filees/filees-entry"
install -m 0555 "$bundle/bin/filees-worker" "$prefix/libexec/filees/filees-worker"
install -m 0555 "$bundle/bin/filees-service-wc-corrector" "$prefix/libexec/filees/filees-service-wc-corrector"
install -m 0555 "$bundle/bin/filees-client-entry" "$prefix/libexec/filees/filees-client-entry"
install -m 0555 "$bundle/bin/filees-serving-state" "$prefix/libexec/filees/filees-serving-state"
install -m 0555 "$bundle/bin/filees-public-authority" "$prefix/libexec/filees/filees-public-authority"
install -m 0555 "$bundle/bin/filees-links" "$prefix/libexec/filees/filees-links"

install -d -m 700 "$sysconfdir"
if [ ! -e "$sysconfdir/server.json" ]; then
	install -m 600 "$bundle/share/filees/server.example.json" "$sysconfdir/server.json"
fi
if [ ! -e "$sysconfdir/public-links.json" ]; then
	install -m 600 "$bundle/share/filees/public-links.example.json" "$sysconfdir/public-links.json"
fi
if [ ! -e "$sysconfdir/install.conf" ]; then
	install -m 600 "$bundle/share/filees/install.example.conf" "$sysconfdir/install.conf"
fi
if [ ! -e "$sysconfdir/otp.pepper" ]; then
	umask 077
	openssl rand -base64 32 >"$sysconfdir/otp.pepper"
fi
if [ ! -e "$sysconfdir/public-share-frost.key" ]; then
	umask 077
	openssl rand -base64 32 >"$sysconfdir/public-share-frost.key"
fi
if [ ! -e "$sysconfdir/public-share-visit.key" ]; then
	umask 077
	openssl rand -base64 32 >"$sysconfdir/public-share-visit.key"
fi
if [ ! -e "$sysconfdir/worker_ed25519" ]; then
	umask 077
	ssh-keygen -q -t ed25519 -N '' -C filees-worker-v1 -f "$sysconfdir/worker_ed25519"
fi

install -d -m 700 "$statedir" "$statedir/tickets" "$statedir/operations" "$statedir/audit"
if [ ! -e "$statedir/.toolchain.lock" ]; then
	install -m 600 /dev/null "$statedir/.toolchain.lock"
fi
install -d -m 700 /var/filees/activation /var/filees/activation/records /var/filees/activation/proofs /var/filees/sessions
install -d -m 700 /var/filees/repositories /var/filees/repository-operations
install -d -m 700 /var/filees/repository-operations/public-shares
install -d -m 755 "$public_downloads_dir"
install -d -m 700 "$public_authority_staging_root" "$public_links_cache_root"
install -d -m 750 /var/run/filees /var/www/run/filees
if [ ! -e /var/filees/activation/repositories.authz ]; then
	install -m 600 /dev/null /var/filees/activation/repositories.authz
fi

echo "FileES server tools installed. Edit $sysconfdir/server.json before use."
echo "Set public_shares.authority_staging_root=$public_authority_staging_root in server.json"
echo "Set cache.root=$public_links_cache_root in public-links.json; examples are not rewritten."
echo "Set install.public_downloads_dir=$public_downloads_dir in install.conf; use the same PUBLIC_* settings for install-ssh.sh."
echo "No daemon or rc.d service was installed."
echo "Manual pages installed under $prefix/man (man filees, man filees-admin)."
echo "On OpenBSD, review and run openbsd/install-ssh.sh to enable the system-sshd entries."
