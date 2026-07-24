#!/bin/sh
set -eu

bundle=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
prefix=${PREFIX:-"$HOME/.local"}
data_home=${XDG_DATA_HOME:-"$HOME/.local/share"}
config_home=${XDG_CONFIG_HOME:-"$HOME/.config"}
daemon_bin="$prefix/bin/filees"
gui_bin="$prefix/bin/filees-gui"
pair_gui_bin="$prefix/bin/filees-pair-gui"
desktop="$data_home/applications/filees-gui.desktop"
icon="$data_home/icons/hicolor/scalable/apps/filees-gui.svg"
config_dir="$config_home/filees"
config="$config_dir/config.json"
unit_dir="$config_home/systemd/user"
unit="$unit_dir/filees.service"

newline='
'
case "$daemon_bin$gui_bin$config" in
	*"$newline"*)
		printf 'Newlines are not supported in FileES installation paths\n' >&2
		exit 2
		;;
esac

if [ ! -f "$bundle/SHA256SUMS" ]; then
	printf 'FileES bundle has no SHA256SUMS manifest\n' >&2
	exit 1
fi
(cd "$bundle" && sha256sum -c SHA256SUMS)

escape_sed_replacement() {
	printf '%s' "$1" | sed 's/[\\&|]/\\&/g; s/"/\\"/g; s/%/%%/g'
}

mkdir -p "$config_dir"
if [ ! -f "$config" ]; then
	install -m 0600 "$bundle/share/filees/config.example.json" "$config"
fi
"$bundle/bin/filees" config-check --config "$config"

mkdir -p "$(dirname -- "$daemon_bin")" "$(dirname -- "$desktop")" "$(dirname -- "$icon")" "$unit_dir"
install -m 0755 "$bundle/bin/filees" "$daemon_bin"
install -m 0755 "$bundle/bin/filees-gui" "$gui_bin"
install -m 0755 "$bundle/bin/filees-pair-gui" "$pair_gui_bin"
install -m 0644 "$bundle/share/icons/hicolor/scalable/apps/filees-gui.svg" "$icon"

escaped_gui=$(escape_sed_replacement "$gui_bin")
sed "s|^Exec=.*|Exec=\"$escaped_gui\"|; s|^TryExec=.*|TryExec=$escaped_gui|" \
    "$bundle/share/applications/filees-gui.desktop" > "$desktop"
chmod 0644 "$desktop"

escaped_daemon=$(escape_sed_replacement "$daemon_bin")
escaped_config=$(escape_sed_replacement "$config")
sed "s|@FILEES_BIN@|$escaped_daemon|g; s|@CONFIG_PATH@|$escaped_config|g" \
    "$bundle/share/systemd/user/filees.service" > "$unit"
chmod 0644 "$unit"

if command -v systemctl >/dev/null 2>&1; then
	systemctl --user daemon-reload
	if [ "${RESTART_DAEMON:-1}" = "1" ] && systemctl --user is-active --quiet filees.service; then
		systemctl --user restart filees.service
	fi
	if [ "${ENABLE_DAEMON:-0}" = "1" ]; then
		systemctl --user enable --now filees.service
	fi
elif [ "${ENABLE_DAEMON:-0}" = "1" ]; then
	printf 'systemctl is required to enable the FileES daemon service\n' >&2
	exit 1
fi

if [ "${ENABLE_AUTOSTART:-0}" = "1" ]; then
	"$gui_bin" --autostart enable
fi

printf 'FileES client installed\n'
printf '  daemon: %s\n' "$daemon_bin"
printf '  GUI:    %s\n' "$gui_bin"
printf '  pairing helper: %s\n' "$pair_gui_bin"
printf '  config: %s\n' "$config"
printf 'Enable now with: systemctl --user enable --now filees.service\n'
