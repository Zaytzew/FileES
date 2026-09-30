#!/usr/bin/env bash
# Deploy filees.space from this working copy in one step: build and check the
# landing page, stage the download-page publisher, upload both to the web
# server and install them there (publisher first, then the landing page).
#
#   bash tools/deploy-site.sh [-y] [--dry-run]
#
#   -y         do not ask before uploading
#   --dry-run  build, check and stage only; nothing leaves this computer
#
# Configuration stays outside the repository (an admin login once leaked into
# the public mirror through an example). It comes from the environment or from
# site-deploy.env in ~/.config/filees/ or in .config/filees/ at the root of the
# working copy's drive (plain KEY=VALUE; UTF-16 and CRLF from Windows are fine):
#
#   SITE_HOST=admin@web-server   ssh destination (required)
#   SITE_PORT=22                 optional
#   SITE_BECOME=sudo             "su" when the account has no sudo
#   SITE_URL=https://filees.space
#
# The committed working copy is what gets deployed: local modifications in the
# site sources stop the script (ALLOW_DIRTY=1 overrides, for a preview only).
set -euo pipefail

root=$(cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

assume_yes=0
dry_run=0
for arg in "$@"; do
	case "$arg" in
	-y) assume_yes=1 ;;
	--dry-run) dry_run=1 ;;
	*) echo "usage: bash tools/deploy-site.sh [-y] [--dry-run]" >&2; exit 2 ;;
	esac
done

die() {
	echo "deploy-site: $*" >&2
	exit 1
}

# PowerShell's bash is WSL, which has none of this computer's node, go or svn.
if grep -qi microsoft /proc/version 2>/dev/null; then
	die "run it with Git Bash, not WSL: & 'C:\Program Files\Git\bin\bash.exe' tools/deploy-site.sh"
fi

# KEY=VALUE lines, read rather than sourced. Accepts what Windows tools write:
# UTF-16 (PowerShell 5.1's > and Out-File), a UTF-8 BOM and CRLF.
load_config() {
	local file=$1 text line key value
	if [ "$(head -c 2 "$file" | od -An -tx1 | tr -d ' ')" = fffe ]; then
		text=$(iconv -f UTF-16 -t UTF-8 "$file") || die "cannot read $file"
	else
		text=$(cat "$file")
	fi
	text=${text#$'\xef\xbb\xbf'}
	while IFS= read -r line; do
		line=${line%$'\r'}
		line=${line#export }
		case "$line" in '' | '#'*) continue ;; esac
		key=${line%%=*}
		value=${line#*=}
		case "$value" in
		\"*\" | \'*\') value=${value:1:${#value}-2} ;;
		esac
		case "$key" in
		SITE_HOST | SITE_PORT | SITE_BECOME | SITE_URL) ;;
		*) die "$file: unknown setting '$key'" ;;
		esac
		# The environment wins over the file.
		[ -n "${!key:-}" ] || printf -v "$key" '%s' "$value"
	done <<<"$text"
}

# First found: SITE_DEPLOY_ENV, the home directory, the working copy's drive
# root (the owner keeps it in E:\.config).
config=""
for candidate in ${SITE_DEPLOY_ENV:+"$SITE_DEPLOY_ENV"} "$HOME/.config/filees/site-deploy.env" "$(cd "$root/../.." && pwd)/.config/filees/site-deploy.env"; do
	if [ -f "$candidate" ]; then
		config=$candidate
		break
	fi
done
if [ -n "$config" ]; then
	load_config "$config"
fi
SITE_PORT="${SITE_PORT:-22}"
SITE_BECOME="${SITE_BECOME:-sudo}"
SITE_URL="${SITE_URL:-https://filees.space}"
case "$SITE_BECOME" in sudo | su) ;; *) die "SITE_BECOME must be sudo or su" ;; esac
if [ "$dry_run" -eq 0 ] && [ -z "${SITE_HOST:-}" ]; then
	die "set SITE_HOST in $HOME/.config/filees/site-deploy.env or E:\.config\filees\site-deploy.env"
fi
for tool in node go svn tar ssh curl sha256sum; do
	command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done

# 1. Only committed sources.
sources="index.html landing packaging/site cmd/filees-site-download"
# shellcheck disable=SC2086
dirty=$(svn status -q $sources)
if [ -n "$dirty" ]; then
	if [ "${ALLOW_DIRTY:-0}" != 1 ]; then
		printf '%s\n' "$dirty" >&2
		die "commit the site sources first"
	fi
	echo "deploy-site: ALLOW_DIRTY=1, deploying uncommitted changes" >&2
fi
revision="r$(svnversion -n .)"

# 2. Build and check.
echo "== build and check (SVN $revision)"
node landing/build.mjs
node landing/check.mjs
node --test landing/*.test.mjs
node packaging/site/stage.mjs

# 3. Stage what goes to the server.
out="$root/dist/site-deploy"
rm -rf "$out"
mkdir -p "$out/landing"
cp -R dist/site-publisher "$out/site-publisher"
for item in index.html release-badge.js android-badge.js demo privacy badges; do
	cp -R "dist/landing-site/$item" "$out/landing/$item"
done
# Runs on Linux: strip CRLF a Windows checkout may have added.
sed 's/\r$//' packaging/site/deploy-landing.sh >"$out/deploy-landing.sh"
printf '%s\n' "$revision" >"$out/REVISION"
echo "== staged $(du -sh "$out" | cut -f1) in $out"

if [ "$dry_run" -eq 1 ]; then
	echo "dry run: nothing uploaded"
	exit 0
fi
if [ "$assume_yes" -eq 0 ]; then
	printf 'Deploy SVN %s to %s (%s)? [y/N] ' "$revision" "$SITE_HOST" "$SITE_URL"
	read -r answer
	case "$answer" in y | Y | t | T) ;; *) echo "cancelled"; exit 1 ;; esac
fi

# 4. Upload into a fresh directory in the admin account's home.
echo "== upload"
tar -C "$out" -czf - . | ssh -p "$SITE_PORT" "$SITE_HOST" \
	'rm -rf "$HOME/filees-site-deploy" && mkdir -m 0700 "$HOME/filees-site-deploy" && tar -xzf - -C "$HOME/filees-site-deploy"'

# 5. Install as root (asks for the sudo or root password on the terminal).
echo "== install on the server"
if [ "$SITE_BECOME" = su ]; then
	ssh -t -p "$SITE_PORT" "$SITE_HOST" 'su -c "sh $HOME/filees-site-deploy/deploy-landing.sh"'
else
	ssh -t -p "$SITE_PORT" "$SITE_HOST" 'sudo sh "$HOME/filees-site-deploy/deploy-landing.sh"'
fi

# 6. What the site serves now must be what was built here.
echo "== check $SITE_URL"
failed=0
for file in index.html release-badge.js android-badge.js; do
	local_sum=$(sha256sum "$out/landing/$file" | cut -d' ' -f1)
	live_sum=$(curl -fsS --max-time 20 "$SITE_URL/$file?deploy=$revision" | sha256sum | cut -d' ' -f1) || live_sum=unreachable
	if [ "$local_sum" = "$live_sum" ]; then
		echo "ok       $file"
	else
		echo "DIFFERS  $file" >&2
		failed=1
	fi
done
for page in download/ download-alpha/ privacy/ demo/; do
	if curl -fsS --max-time 20 -o /dev/null "$SITE_URL/$page"; then
		echo "ok       $page"
	else
		echo "FAILED   $page" >&2
		failed=1
	fi
done
[ "$failed" -eq 0 ] || die "the site does not serve what was deployed (cache? see above)"
echo "deployed SVN $revision to $SITE_URL"
