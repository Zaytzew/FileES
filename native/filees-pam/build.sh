#!/bin/sh
# Builds pam_filees_tunnel.so. Needs a C compiler and the PAM development
# headers (Debian: libpam0g-dev, Fedora: pam-devel). The module uses only
# libpam and long-stable libc calls, so one build serves both distributions.
set -eu
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
out=${1:-"$here/pam_filees_tunnel.so"}
${CC:-cc} -shared -fPIC -O2 -Wall -Wextra -Werror \
	-fstack-protector-strong -D_FORTIFY_SOURCE=2 \
	-Wl,-z,relro,-z,now -Wl,--no-undefined \
	-o "$out" "$here/pam_filees_tunnel.c" -lpam
echo "$out"
