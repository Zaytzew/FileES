//go:build linux

package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

func TestLinuxTrayTooltipUpdatesExistingSNIProperty(t *testing.T) {
	if os.Getenv("FILEES_TEST_PRIVATE_DBUS") != "1" {
		t.Skip("run under a private dbus-run-session")
	}
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	name := fmt.Sprintf("org.kde.StatusNotifierItem-%d-1", os.Getpid())
	if _, err := conn.RequestName(name, dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
	value := struct {
		V0 string
		V1 []struct {
			W   int
			H   int
			Pix []byte
		}
		V2 string
		V3 string
	}{}
	_, err = prop.Export(conn, "/StatusNotifierItem", map[string]map[string]*prop.Prop{"org.kde.StatusNotifierItem": {"ToolTip": {Value: value, Writable: true, Emit: prop.EmitTrue}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"EKOPROJEKT: zachowana kopia ze zmianami", "FileES — Połączono"} {
		if err := publishLinuxTrayTooltip(t.Context(), conn, os.Getpid(), want); err != nil {
			t.Fatal(err)
		}
		got, e := conn.Object(name, "/StatusNotifierItem").GetProperty("org.kde.StatusNotifierItem.ToolTip")
		if e != nil {
			t.Fatal(e)
		}
		var fields []any
		if err := dbus.Store([]any{got.Value()}, &fields); err != nil {
			t.Fatal(err)
		}
		if len(fields) != 4 || fields[2] != want {
			t.Fatalf("native tooltip=%#v", fields)
		}
	}
	other, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := publishLinuxTrayTooltip(t.Context(), other, os.Getpid(), "wrong owner"); err == nil {
		t.Fatal("foreign tray changed")
	}
}
