#!/bin/sh
# Install the download-page publisher on the filees.space web server (Debian).
#
# Run as root from the directory staged by packaging/site/stage.mjs:
#   sudo sh install.sh
#
# Safe to run again: it updates the program and its files, publishes once as
# the service user, and leaves an existing symlink and cron entry in place.
set -eu

die() {
	echo "filees-site install: $*" >&2
	exit 1
}

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
docroot="${DOCROOT:-/var/www/filees.space}"
user=filees-site
home=/var/lib/filees-site
share=/usr/local/share/filees-site
published="$home/site/download"

[ "$(id -u)" -eq 0 ] || die "run as root (sudo sh install.sh)"
[ -d "$docroot" ] || die "web root not found: $docroot (set DOCROOT)"
for tool in svn flock logger runuser; do
	command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done
for file in filees-site-download download.json release-key.pub download.html filees-site-download.cron; do
	[ -f "$here/$file" ] || die "missing $here/$file; stage the directory with packaging/site/stage.mjs"
done

# A system account that owns nothing but its own publication and state.
if ! id "$user" >/dev/null 2>&1; then
	useradd --system --home-dir "$home" --no-create-home --shell /usr/sbin/nologin "$user"
fi
install -d -m 0755 -o "$user" -g "$user" "$home" "$home/site"

install -m 0755 -o root -g root "$here/filees-site-download" /usr/local/bin/filees-site-download
install -d -m 0755 -o root -g root "$share"
install -m 0644 -o root -g root "$here/download.json" "$here/release-key.pub" "$here/download.html" "$share/"

# Publish once now, as the service user. A release that does not verify stops
# the installation here, before the web root is touched.
runuser -u "$user" -- env HOME="$home" /usr/local/bin/filees-site-download \
	-config "$share/download.json" -key "$share/release-key.pub" -template "$share/download.html" \
	-out "$published" -state "$home/state.json"

# The web root keeps pointing at the publication through one symlink, so the
# service user never needs write access to the web root itself.
link="$docroot/download"
if [ -L "$link" ]; then
	[ "$(readlink "$link")" = "$published" ] || die "$link is a symlink to $(readlink "$link"), not $published"
else
	if [ -e "$link" ]; then
		aside="$home/download.manual-$(date +%Y%m%d%H%M%S)"
		mv "$link" "$aside"
		echo "moved the manually uploaded $link to $aside"
	fi
	ln -s "$published" "$link"
fi

install -m 0644 -o root -g root "$here/filees-site-download.cron" /etc/cron.d/filees-site-download

echo "installed: $link -> $published, refreshed every 15 minutes"
echo "logs: journalctl -t filees-site-download"
