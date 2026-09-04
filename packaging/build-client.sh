#!/bin/sh
# Refuses, and says where to go instead.
#
# This used to exec build-gui.sh, which packages cmd/filees-gui - the abandoned
# Fyne client. Anyone reaching for "build the client" got a bundle without the
# daemon in it, which cannot sync anything, and nothing said so. The name was
# the trap: it is the obvious thing to run and it did the wrong thing quietly.
#
# Two real paths exist now, and which one you want depends on what it is for.
set -eu

cat >&2 <<'EOF'
packaging/build-client.sh no longer builds anything.

It used to run build-gui.sh, which packages the abandoned Fyne client and omits
the daemon entirely - a bundle that cannot synchronise a single file.

What you probably want:

  packaging/build-pair.sh
      The desktop pair for this machine, stamped with VERSION + the working
      copy revision. This is what you run while developing, and what a restart
      of the local pair uses.

  tools/prepare-client-release-windows.sh
      One immutable Windows release staged into a FILEES-BIN working copy:
      builds the pair, assembles the bundle the updater expects, and writes the
      manifest and channel candidate for the signing machine.
      Needs RELEASE_ID, SEQUENCE and KEY_ID.

  packaging/windows/build-msi.ps1 -BundleDir <bundle>
      The installer, built from a release bundle so the MSI and the update
      channel ship identical bytes.

The abandoned path is still reachable on purpose, for reproducing an old build:

  packaging/build-gui.sh
EOF
exit 2
