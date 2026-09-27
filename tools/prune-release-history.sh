#!/bin/sh
# Keep only recent releases in FILEES-BIN's HEAD.
#
# Every release since the first stayed under releases/, so a fresh checkout of
# the signing working copy carried all of them twice (files and .svn pristine
# copies: 7.2 GB + 6.6 GB on 2026-09-25). Re-checking-out is the recovery for a
# signing working copy gone wrong, and it should not cost that.
#
# Kept: the KEEP newest releases of each kind (the suffix after rNNNN: desktop
# "", "-server", "-beta", ...) and every release a channel points at. The rest
# is `svn delete`d from HEAD only - the repository history keeps all of it, and
# `svn copy URL/releases/rNNNN@REV` brings one back.
#
# Dry run by default: prints what would go. APPLY=1 deletes and commits.
set -eu

FILEES_BIN_WC="${FILEES_BIN_WC:-$HOME/FILEES-BIN}"
KEEP="${KEEP:-5}"
APPLY="${APPLY:-0}"

die() {
	echo "filees-prune-releases: $*" >&2
	exit 1
}

case "$KEEP" in *[!0-9]*|'') die "KEEP must be a positive integer" ;; esac
[ "$KEEP" -gt 0 ] || die "KEEP must be greater than zero"
[ -d "$FILEES_BIN_WC/.svn" ] || die "not an SVN working copy: $FILEES_BIN_WC"
cd "$FILEES_BIN_WC"
[ -d releases ] || die "no releases/ in $FILEES_BIN_WC"
if [ "$APPLY" = 1 ]; then
	[ -z "$(svn status)" ] || die "working copy has local or unversioned changes"
	svn update --quiet
	[ -z "$(svn status)" ] || die "working copy changed during update"
fi

referenced=$(cat channels/*.json 2>/dev/null | sed -n 's/.*"release_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | sort -u)

# name<TAB>kind<TAB>number, newest first within each kind.
list=$(for dir in releases/*/; do
	name=$(basename "$dir")
	number=$(printf '%s' "$name" | sed -n 's/^r\([0-9][0-9]*\).*$/\1/p')
	[ -n "$number" ] || { echo "skip (not rNNNN): $name" >&2; continue; }
	kind=$(printf '%s' "$name" | sed 's/^r[0-9][0-9]*//')
	printf '%s\t%s\t%s\n' "$name" "${kind:-desktop}" "$number"
done | sort -t "$(printf '\t')" -k2,2 -k3,3nr)

remove=""
kept=0
removed=0
previous_kind=""
count=0
tab=$(printf '\t')
old_ifs=$IFS
IFS='
'
for line in $list; do
	name=${line%%"$tab"*}
	rest=${line#*"$tab"}
	kind=${rest%%"$tab"*}
	if [ "$kind" != "$previous_kind" ]; then
		previous_kind=$kind
		count=0
	fi
	count=$((count + 1))
	if [ "$count" -le "$KEEP" ] || printf '%s\n' "$referenced" | grep -qx "$name"; then
		kept=$((kept + 1))
		continue
	fi
	remove="$remove releases/$name"
	removed=$((removed + 1))
done
IFS=$old_ifs

echo "channels point at: $(printf '%s ' $referenced)"
echo "keep $kept release(s), remove $removed (KEEP=$KEEP per kind)"
for path in $remove; do echo "  remove $path"; done
if [ "$removed" -eq 0 ]; then
	if [ "$APPLY" = 1 ]; then svn cleanup --vacuum-pristines; fi
	exit 0
fi
if [ "$APPLY" != 1 ]; then
	echo "dry run - nothing changed; APPLY=1 to delete from HEAD (history keeps them)"
	exit 0
fi
# shellcheck disable=SC2086 # generated paths under releases/
svn delete --quiet $remove
# shellcheck disable=SC2086
svn commit --quiet $remove -m "Prune release history: keep the $KEEP newest releases of each kind and every channel target

$removed old release(s) removed from HEAD; they remain in the repository history."
svn cleanup --vacuum-pristines
echo "done: removed $removed release(s) from HEAD; unused WC pristines removed"
