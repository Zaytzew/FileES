#!/bin/sh
set -eu

# Builds the FileES desktop pair - the daemon (cmd/filees) and the Wails
# interface (cmd/filees-gui-wails) - and stamps both with a version that says
# which build this is.
#
# It exists because nothing did. build-gui.sh packages the abandoned Fyne
# client and every session built the real pair by hand, each slightly
# differently and none of them passing -X main.version. The badge therefore
# showed whatever sat in VERSION, a file edited twice in the project's life, so
# every build since July has called itself 0.1.15.
#
# That is not a cosmetic complaint. On 2026-09-03 a fix reached the owner's
# production machine and never executed once, and a commit went in that did not
# build from a clean tree; in both cases the running client and the source
# tree could not be told apart by looking. A version that does not move cannot
# answer "is my fix in there", which is the only question it is ever asked.
#
# The version is <VERSION>+r<revision>, with M appended when the working copy
# carries uncommitted changes. The revision is the identifier this project
# actually uses - every report, every handoff and every message between
# sessions is numbered by it - so the badge now speaks the same language.

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
dist=${DIST:-"$root/dist"}
base=$(sed -n '1p' "$root/VERSION")

# svnversion reports things like "793", "790:793" (mixed) and "793M"
# (modified). Mixed revisions are reduced to the highest, because that is the
# newest source that could be in the binary; the M is kept, because a build
# from a dirty tree is exactly the one nobody should mistake for a release.
revision=$(svnversion -n "$root" 2>/dev/null || true)
case "$revision" in
	''|*exported*|*Unversioned*) version="$base" ;;
	*)
		modified=""
		case "$revision" in *M*) modified="M" ;; esac
		number=$(printf '%s' "$revision" | tr -cd '0-9:' | sed 's/.*://')
		if [ -n "$number" ]; then
			version="$base+r$number$modified"
		else
			version="$base"
		fi
		;;
esac

mkdir -p "$dist"
echo "building the desktop pair as $version"

case "$(uname -s 2>/dev/null || echo unknown)" in
	MINGW*|MSYS*|CYGWIN*|Windows_NT) daemon="filees.exe"; gui="filees-gui-wails.exe" ;;
	*) daemon="filees"; gui="filees-gui-wails" ;;
esac

# -buildvcs=false matches every other build path here: the repository is SVN,
# so Go's own stamping has nothing to read and only slows the build down.
go build -trimpath -buildvcs=false -ldflags "-X main.version=$version" -o "$dist/$daemon" ./cmd/filees
go build -trimpath -buildvcs=false -ldflags "-X main.version=$version" -o "$dist/$gui" ./cmd/filees-gui-wails

echo "$dist/$daemon"
echo "$dist/$gui"
