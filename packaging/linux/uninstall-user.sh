#!/bin/sh
set -eu

prefix=${PREFIX:-"$HOME/.local"}
data_home=${XDG_DATA_HOME:-"$HOME/.local/share"}
config_home=${XDG_CONFIG_HOME:-"$HOME/.config"}

if command -v systemctl >/dev/null 2>&1; then
	systemctl --user disable --now filees.service >/dev/null 2>&1 || true
fi

rm -f \
    "$prefix/bin/filees" \
    "$prefix/bin/filees-gui" \
    "$prefix/bin/filees-pair-gui" \
    "$data_home/applications/filees-gui.desktop" \
    "$data_home/icons/hicolor/scalable/apps/filees-gui.svg" \
    "$config_home/autostart/filees-gui.desktop" \
    "$config_home/systemd/user/filees.service"

if command -v systemctl >/dev/null 2>&1; then
	systemctl --user daemon-reload >/dev/null 2>&1 || true
fi

printf 'FileES client uninstalled for the current user\n'
printf 'Configuration and synchronized data were preserved\n'
