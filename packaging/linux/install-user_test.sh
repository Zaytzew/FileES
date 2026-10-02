#!/bin/sh
# A paused copy must not change the executable visible at the installed path.
set -eu

project=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
work=$(mktemp -d "$project/.tmp-install-user-test.XXXXXXXX")
cleanup() {
	case "$work" in "$project"/.tmp-install-user-test.*) rm -rf -- "$work" ;; esac
}
trap cleanup EXIT

bundle="$work/bundle"
prefix="$work/prefix"
config_home="$work/config"
data_home="$work/data"
mkdir -p "$bundle/bin" "$bundle/share/icons/hicolor/scalable/apps" \
	"$bundle/share/applications" "$bundle/share/systemd/user" \
	"$work/tools" "$config_home/filees"
cp "$project/packaging/linux/install-user.sh" "$bundle/install-user.sh"
printf '{}\n' > "$config_home/filees/config.json"
printf '#!/bin/sh\nprintf "old\\n"\n' > "$bundle/bin/filees"
printf '#!/bin/sh\nexit 0\n' > "$bundle/bin/filees-gui"
printf '#!/bin/sh\nexit 0\n' > "$bundle/bin/filees-svn"
chmod 0755 "$bundle/bin/filees" "$bundle/bin/filees-gui" "$bundle/bin/filees-svn"
printf '<svg/>\n' > "$bundle/share/icons/hicolor/scalable/apps/filees-gui.svg"
printf 'Exec=filees-gui\nTryExec=filees-gui\n' > "$bundle/share/applications/filees-gui.desktop"
printf 'ExecStart=@FILEES_BIN@ --config @CONFIG_PATH@\n' > "$bundle/share/systemd/user/filees.service"
printf '#!/bin/sh\nexit 0\n' > "$work/tools/systemctl"
chmod 0755 "$work/tools/systemctl"

# Simulate install(1) pausing between writing bytes and setting executable mode.
# The first installation runs without a pause; the upgrade pauses only filees.
printf '%s\n' '#!/bin/sh' \
	'set -eu' \
	'mode=$2; source_file=$3; target_file=$4' \
	'cp "$source_file" "$target_file"' \
	'chmod 0644 "$target_file"' \
	'if [ "${FILEES_TEST_FAIL_SOURCE:-}" = "$source_file" ]; then exit 23; fi' \
	'if [ "${FILEES_TEST_SLOW_SOURCE:-}" = "$source_file" ]; then' \
	'  : > "$FILEES_TEST_PAUSED"' \
	'  while [ ! -f "$FILEES_TEST_CONTINUE" ]; do sleep 0.02; done' \
	'fi' \
	'chmod "$mode" "$target_file"' > "$work/tools/install"
chmod 0755 "$work/tools/install"

refresh_manifest() (
	cd "$bundle"
	sha256sum bin/filees bin/filees-gui bin/filees-svn \
		share/icons/hicolor/scalable/apps/filees-gui.svg \
		share/applications/filees-gui.desktop share/systemd/user/filees.service \
		> SHA256SUMS
)
refresh_manifest
PATH="$work/tools:$PATH" PREFIX="$prefix" XDG_CONFIG_HOME="$config_home" \
	XDG_DATA_HOME="$data_home" RESTART_DAEMON=0 \
	sh "$bundle/install-user.sh" > "$work/initial.log"
test "$("$prefix/bin/filees")" = old

printf '#!/bin/sh\nprintf "new\\n"\n' > "$bundle/bin/filees"
chmod 0755 "$bundle/bin/filees"
refresh_manifest
paused="$work/paused"
resume="$work/resume"
PATH="$work/tools:$PATH" PREFIX="$prefix" XDG_CONFIG_HOME="$config_home" \
	XDG_DATA_HOME="$data_home" RESTART_DAEMON=0 \
	FILEES_TEST_SLOW_SOURCE="$bundle/bin/filees" FILEES_TEST_PAUSED="$paused" \
	FILEES_TEST_CONTINUE="$resume" sh "$bundle/install-user.sh" \
	> "$work/upgrade.log" &
installer_pid=$!
attempt=0
while [ ! -f "$paused" ] && [ "$attempt" -lt 200 ]; do
	attempt=$((attempt + 1))
	sleep 0.02
done
if [ ! -f "$paused" ]; then
	: > "$resume"
	wait "$installer_pid" || true
	printf 'installer did not reach the paused copy\n' >&2
	exit 1
fi
visible=$("$prefix/bin/filees" 2>&1) || visible=not-executable
: > "$resume"
wait "$installer_pid"
test "$visible" = old || { printf 'installed binary changed during staged copy: %s\n' "$visible" >&2; exit 1; }
test "$("$prefix/bin/filees")" = new

printf '#!/bin/sh\nprintf "broken\\n"\n' > "$bundle/bin/filees"
chmod 0755 "$bundle/bin/filees"
refresh_manifest
if PATH="$work/tools:$PATH" PREFIX="$prefix" XDG_CONFIG_HOME="$config_home" \
	XDG_DATA_HOME="$data_home" RESTART_DAEMON=0 \
	FILEES_TEST_FAIL_SOURCE="$bundle/bin/filees" \
	sh "$bundle/install-user.sh" > "$work/failed.log" 2>&1; then
	printf 'failed staging unexpectedly succeeded\n' >&2
	exit 1
fi
test "$("$prefix/bin/filees")" = new
printf 'install-user atomic executable replacement: PASS\n'
