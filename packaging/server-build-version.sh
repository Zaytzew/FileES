#!/bin/sh
# Print the identity of this source tree, not a mutable distribution channel.
set -eu
root=${1:?source root required}
base=$(sed -n '1p' "$root/VERSION" | tr -d '\r')
case "$base" in ''|*[!A-Za-z0-9.+-]*) echo "invalid VERSION" >&2; exit 2 ;; esac
revision=$(svnversion -n "$root" 2>/dev/null || true)
if [ -n "${FILEES_SOURCE_REVISION:-}" ]; then
	# The release preparer supplies its manifest revision. Never mislabel a
	# dirty, switched, partial or mixed working copy as that clean release.
	case "$FILEES_SOURCE_REVISION" in *[!0-9]*) echo "invalid source revision" >&2; exit 2 ;; esac
	[ "$revision" = "$FILEES_SOURCE_REVISION" ] || {
		echo "source tree $revision does not match release revision $FILEES_SOURCE_REVISION" >&2
		exit 2
	}
fi
if printf '%s\n' "$revision" | grep -Eq '^[0-9]+(:[0-9]+)?[MSP]*$'; then
	# Preserve mixed ranges and SVN M/S/P markers instead of hiding them.
	revision=$(printf '%s' "$revision" | tr ':' '-')
	printf '%s+r%s\n' "$base" "$revision"
else
	printf '%s+unversioned\n' "$base"
fi
