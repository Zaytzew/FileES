#!/bin/sh
set -eu

bundle=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
prefix=${PREFIX:-"$HOME/.local"}
data_home=${XDG_DATA_HOME:-"$HOME/.local/share"}
bin="$prefix/bin/filees-gui"
desktop="$data_home/applications/filees-gui.desktop"
icon="$data_home/icons/hicolor/scalable/apps/filees-gui.svg"

case "$bin" in
	*'|'*)
		printf 'Unsupported | character in installation path: %s\n' "$bin" >&2
		exit 2
		;;
esac

mkdir -p "$(dirname -- "$bin")" "$(dirname -- "$desktop")" "$(dirname -- "$icon")"
install -m 0755 "$bundle/bin/filees-gui" "$bin"
install -m 0644 "$bundle/share/icons/hicolor/scalable/apps/filees-gui.svg" "$icon"

escaped_bin=$(printf '%s' "$bin" | sed 's/\\/\\\\/g; s/"/\\"/g; s/%/%%/g; s/`/\\`/g; s/\$/\\$/g')
sed "s|^Exec=.*|Exec=\"$escaped_bin\"|; s|^TryExec=.*|TryExec=$escaped_bin|" \
    "$bundle/share/applications/filees-gui.desktop" > "$desktop"
chmod 0644 "$desktop"

if [ "${ENABLE_AUTOSTART:-0}" = "1" ]; then
	"$bin" --autostart enable
fi

printf 'FileES GUI installed: %s\n' "$bin"
