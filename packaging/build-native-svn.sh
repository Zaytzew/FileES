#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
dist=${DIST:-"$root/dist"}
mkdir -p "$dist"
dist=$(CDPATH= cd -- "$dist" && pwd)
set -- -S "$root/native/filees-svn" -B "$dist/native-svn-build" \
  "-DCMAKE_RUNTIME_OUTPUT_DIRECTORY=$dist" -DCMAKE_BUILD_TYPE=RelWithDebInfo
# Optional explicit paths to one coherent development SDK. No eval, download,
# package-manager install or modification of global PATH.
for name in SVN_INCLUDE_DIR APR_INCLUDE_DIR SVN_CLIENT_LIBRARY SVN_WC_LIBRARY SVN_SUBR_LIBRARY APR_LIBRARY; do
  value=$(printenv "$name" || true)
  if [ -n "$value" ]; then set -- "$@" "-D$name=$value"; fi
done
"${FILEES_CMAKE:-cmake}" "$@"
"${FILEES_CMAKE:-cmake}" --build "$dist/native-svn-build" --config RelWithDebInfo
