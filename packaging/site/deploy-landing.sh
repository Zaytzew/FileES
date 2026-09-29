#!/bin/sh
# Install a filees.space deployment uploaded by tools/deploy-site.sh.
#
# Run as root from the uploaded directory:
#   sudo sh deploy-landing.sh
#
# Order matters: the download-page publisher goes first (install.sh publishes
# once and stops on a release that does not verify), and only then the landing
# page replaces its files. Every replaced file is backed up first; nothing
# else in the web root is touched, and the publisher-owned download,
# download-alpha and android symlinks are never replaced here.
set -eu
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export PATH

die() {
	echo "filees-site deploy: $*" >&2
	exit 1
}

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
docroot="${DOCROOT:-/var/www/filees.space}"
backups=/var/lib/filees-site/landing-backups
files="index.html release-badge.js android-badge.js"
dirs="demo privacy"

[ "$(id -u)" -eq 0 ] || die "run as root (sudo sh deploy-landing.sh)"
[ -d "$docroot" ] || die "web root not found: $docroot (set DOCROOT)"
[ -f "$here/site-publisher/install.sh" ] || die "missing $here/site-publisher; upload with tools/deploy-site.sh"
for item in $files $dirs; do
	[ -e "$here/landing/$item" ] || die "missing $here/landing/$item; upload with tools/deploy-site.sh"
	[ -L "$docroot/$item" ] && die "$docroot/$item is a symlink; refusing to replace it"
done
revision=$(cat "$here/REVISION" 2>/dev/null || echo unknown)

# 1. Publisher and download-page template.
DOCROOT="$docroot" sh "$here/site-publisher/install.sh"

# 2. Back up what the landing page replaces (kept; each is about 2 MB).
install -d -m 0700 -o root -g root "$backups"
existing=""
for item in $files $dirs; do
	[ -e "$docroot/$item" ] && existing="$existing $item"
done
if [ -n "$existing" ]; then
	backup="$backups/landing-$(date +%Y%m%d%H%M%S).tar.gz"
	# shellcheck disable=SC2086 # a list of plain names
	tar -czf "$backup" -C "$docroot" $existing
	echo "backup of the previous landing page: $backup"
fi

# 3. Landing page. Files are swapped by rename, directories through a
# neighbouring copy, so the site never serves a half-copied page.
for item in $files; do
	install -m 0644 -o root -g root "$here/landing/$item" "$docroot/.$item.new"
	mv -f "$docroot/.$item.new" "$docroot/$item"
done
for item in $dirs; do
	rm -rf "$docroot/.$item.new" "$docroot/.$item.old"
	cp -R "$here/landing/$item" "$docroot/.$item.new"
	chown -R root:root "$docroot/.$item.new"
	find "$docroot/.$item.new" -type d -exec chmod 0755 {} +
	find "$docroot/.$item.new" -type f -exec chmod 0644 {} +
	if [ -e "$docroot/$item" ]; then
		mv "$docroot/$item" "$docroot/.$item.old"
	fi
	mv "$docroot/.$item.new" "$docroot/$item"
	rm -rf "$docroot/.$item.old"
done

printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$revision" >>/var/lib/filees-site/landing-deployed.log
echo "landing page deployed from SVN $revision"
