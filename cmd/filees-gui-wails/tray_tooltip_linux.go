//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/godbus/dbus/v5"
)

// Wails beta.6 leaves linuxSystemTray.setTooltip empty. Its existing SNI
// object already exports a writable ToolTip property. Update that same object
// on its shared session connection: no second tray, fork or cache patch.
func publishNativeTrayTooltip(text string) error {
	conn, err := dbus.SessionBus()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return publishLinuxTrayTooltip(ctx, conn, os.Getpid(), text)
}

func publishLinuxTrayTooltip(ctx context.Context, conn *dbus.Conn, pid int, text string) error {
	name := fmt.Sprintf("org.kde.StatusNotifierItem-%d-1", pid)
	var owner string
	if err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.GetNameOwner", 0, name).Store(&owner); err != nil {
		return err
	}
	ours := false
	for _, ownName := range conn.Names() {
		if ownName == owner {
			ours = true
			break
		}
	}
	if !ours {
		return fmt.Errorf("tray tooltip refuses an object owned by another process")
	}
	const itemPath = dbus.ObjectPath("/StatusNotifierItem")
	value := struct {
		Icon    string
		Pixmaps []struct {
			Width  int32
			Height int32
			Data   []byte
		}
		Title       string
		Description string
	}{Title: text}
	if err := conn.Object(name, itemPath).CallWithContext(ctx, "org.freedesktop.DBus.Properties.Set", 0, "org.kde.StatusNotifierItem", "ToolTip", dbus.MakeVariant(value)).Err; err != nil {
		return err
	}
	return conn.Emit(itemPath, "org.kde.StatusNotifierItem.NewToolTip")
}
