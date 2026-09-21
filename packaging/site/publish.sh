#!/bin/sh
# One lock around both invocations; each lane keeps its own rollback state.
set -u
home=/var/lib/filees-site
share=/usr/local/share/filees-site
export HOME="$home"
status=0
/usr/local/bin/filees-site-download -config "$share/download.json" -key "$share/release-key.pub" -template "$share/download.html" -out "$home/site/download" -state "$home/state-beta.json" "$@" || status=1
# Preserve the pre-beta alpha state; never seed beta from alpha's sequence.
/usr/local/bin/filees-site-download -config "$share/download-alpha.json" -key "$share/release-key.pub" -template "$share/download.html" -out "$home/site/download-alpha" -state "$home/state.json" "$@" || status=1
exit "$status"
