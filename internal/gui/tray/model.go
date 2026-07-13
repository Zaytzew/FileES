// Package tray maps the GUI presentation model to a platform-neutral tray
// menu. It deliberately has no access to IPC or daemon engine packages.
package tray

import app "filees/internal/gui/app"

// MenuModel is a complete immutable tray render description.
type MenuModel struct {
	Icon    app.IconState
	Title   string
	Tooltip string
	Items   []MenuItemModel
}

// MenuItemModel describes a top-level item or nested submenu item.
type MenuItemModel struct {
	ID        string
	Title     string
	Tooltip   string
	Enabled   bool
	Separator bool
	Children  []MenuItemModel
	Intent    *Intent
}

func disabledItem(id, title string) MenuItemModel {
	return MenuItemModel{ID: id, Title: title}
}

func separator(id string) MenuItemModel {
	return MenuItemModel{ID: id, Separator: true}
}

func actionItem(id, title, tooltip string, intent Intent) MenuItemModel {
	return MenuItemModel{ID: id, Title: title, Tooltip: tooltip, Enabled: true, Intent: &intent}
}
