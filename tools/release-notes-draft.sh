#!/bin/sh
# Draft releases/<id>/notes.draft.json from the commits since the previous
# release of the same component. Called by the prepare-*-release scripts.
#
# Commit messages announce what a user will notice, one line per language:
#
#   Co nowego [desktop]: Pobieranie dużych plików strumieniem
#   What's new [desktop]: Large files download as a stream
#
# Scopes: server, desktop, windows, linux, android. An optional kind follows
# a slash: [server/admin], [windows/fix], [desktop/security]. Internal work
# gets no line at all; the card is for users, not a changelog.
#
# The draft is never signed. Review it, complete a missing language, delete
# what a user would not notice, save it as notes.json in the same directory,
# check it with `go run ./cmd/filees-release-notes lint`, and commit it with
# the release. A release without notes.json is valid; its card shows no list.
#
# Never fails the release preparation: a problem here is reported and the
# release is prepared without a draft.
#
# A release that raises SECURITY_EPOCH gets an empty security item to fill
# in: its card must say the update matters, and the signing script refuses
# it otherwise (owner, 2026-09-29).
#
# Environment: COMPONENT (server|desktop|android), RELEASE_ID, SEQUENCE,
# SECURITY_EPOCH, SOURCE_REVISION, PREVIOUS_CHANNEL (channel document that
# names the previous release of this component), FILEES_BIN_WC, SOURCE_WC.
set -u

say() { echo "release notes: $*" >&2; }

release_dir="$FILEES_BIN_WC/releases/$RELEASE_ID"
if [ -e "$release_dir/notes.json" ] || [ -e "$release_dir/notes.draft.json" ]; then
	say "releases/$RELEASE_ID already has notes; left as they are"
	exit 0
fi
if [ ! -f "$PREVIOUS_CHANNEL" ]; then
	say "no previous release ($PREVIOUS_CHANNEL missing); write notes.json by hand if needed"
	exit 0
fi
previous_id=$(sed -n 's/.*"release_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$PREVIOUS_CHANNEL" | head -1)
case "$previous_id" in
	*[!A-Za-z0-9._-]*|'') say "cannot read the previous release from $PREVIOUS_CHANNEL"; exit 0 ;;
esac
if [ "$previous_id" = "$RELEASE_ID" ]; then
	say "the channel already names $RELEASE_ID; no draft"
	exit 0
fi
previous_epoch=$(sed -n 's/.*"security_epoch"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' "$PREVIOUS_CHANNEL" | head -1)
epoch_flags=""
case "${SECURITY_EPOCH:-}:$previous_epoch" in
	*[!0-9:]*|:*|*:) ;;
	*) epoch_flags="-security-epoch $SECURITY_EPOCH -previous-security-epoch $previous_epoch" ;;
esac
previous_notes="$FILEES_BIN_WC/releases/$previous_id/notes.json"
from=""
if [ -f "$previous_notes" ]; then
	from=$(sed -n 's/.*"svn_revision"[[:space:]]*:[[:space:]]*"\([0-9][0-9]*\)".*/\1/p' "$previous_notes" | head -1)
else
	previous_notes=""
fi
# Release IDs are rNNNN[-kind], NNNN being the source revision they were
# built from; the first release with notes starts from there.
[ -n "$from" ] || from=$(printf '%s' "$previous_id" | sed -n 's/^r\([0-9][0-9]*\).*$/\1/p')
case "$from" in
	*[!0-9]*|'') say "cannot tell the source revision of $previous_id; no draft"; exit 0 ;;
esac
if [ "$from" -ge "$SOURCE_REVISION" ]; then
	say "no commits after r$from; no draft"
	exit 0
fi

draft="$release_dir/notes.draft.json"
mkdir -p "$release_dir"
if ! svn log --xml --non-interactive -r "$((from + 1)):$SOURCE_REVISION" "$SOURCE_WC" |
	(cd "$SOURCE_WC" && go run ./cmd/filees-release-notes draft \
		-component "$COMPONENT" -release-id "$RELEASE_ID" -sequence "$SEQUENCE" \
		-svn-revision "$SOURCE_REVISION" ${previous_notes:+-previous "$previous_notes"} \
		$epoch_flags -out "$draft"); then
	rm -f "$draft"
	say "draft failed; the release is prepared without notes"
	exit 0
fi
say "review $draft, save it as notes.json, then lint it:"
say "  go run ./cmd/filees-release-notes lint -release-id $RELEASE_ID -sequence $SEQUENCE $epoch_flags $release_dir/notes.json"
say "and remove notes.draft.json before committing; the signing script refuses a leftover draft"
exit 0
