//go:build linux

package main

import (
	"runtime"
	"strings"
	"testing"

	"filees/pkg/config"
	"filees/pkg/ipcserver"
)

func TestSourceBuildDoesNotEnableClientUpdateWithPlaceholderKey(t *testing.T) {
	if keys, configured := clientReleaseKeyring(); configured || len(keys) != 0 {
		t.Fatalf("placeholder keyring configured: %+v", keys)
	}
	if err := configureClientUpdate(ipcserver.New(t.TempDir()+"/ipc.sock"), nil, "dev"); err != nil {
		t.Fatal(err)
	}
	update := &config.UpdateConfig{Platform: runtime.GOOS + "-" + runtime.GOARCH, StageRoot: t.TempDir()}
	err := configureClientUpdate(ipcserver.New(t.TempDir()+"/ipc.sock"), update, "dev")
	if err == nil || !strings.Contains(err.Error(), "no production release key") {
		t.Fatalf("configured update with placeholder key: %v", err)
	}
}
