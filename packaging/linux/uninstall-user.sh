#!/bin/sh
set -eu

prefix=${PREFIX:-"$HOME/.local"}
data_home=${XDG_DATA_HOME:-"$HOME/.local/share"}
config_home=${XDG_CONFIG_HOME:-"$HOME/.config"}

rm -f \
    "$prefix/bin/filees-gui" \
    "$data_home/applications/filees-gui.desktop" \
    "$data_home/icons/hicolor/scalable/apps/filees-gui.svg" \
    "$config_home/autostart/filees-gui.desktop"

printf 'FileES GUI uninstalled for the current user\n'
