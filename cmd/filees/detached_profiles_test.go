package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/clientprofile"
	"filees/pkg/config"
	"filees/pkg/detachment"
)

func TestDetachedProfileCannotRestartPolling(t *testing.T) {
	root := filepath.Join(t.TempDir(), "servers")
	for _, id := range []string{"manual", "spot"} {
		dir, err := clientprofile.ServerDir(root, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "client-profile.json"), []byte(id), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := detachment.Open(filepath.Join(t.TempDir(), "detachments.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Record(detachment.Record{ServerID: "manual", Cause: detachment.CauseRevoked, At: time.Now()}); err != nil {
		t.Fatal(err)
	}

	active, failures := forgetDetachedProfiles(root, []clientprofile.Profile{{ServerID: "manual"}, {ServerID: "spot"}}, store)
	if len(failures) != 0 {
		t.Fatalf("forget failures: %v", failures)
	}
	if len(active) != 1 || active[0].ServerID != "spot" {
		t.Fatalf("active profiles = %+v", active)
	}
	manualDir, _ := clientprofile.ServerDir(root, "manual")
	if _, err := os.Stat(manualDir); !os.IsNotExist(err) {
		t.Fatalf("detached credentials survived: %v", err)
	}
	spotDir, _ := clientprofile.ServerDir(root, "spot")
	if _, err := os.Stat(spotDir); err != nil {
		t.Fatalf("active profile was removed: %v", err)
	}
}

func TestDetachedServerCannotRestartRepositoryOrLegacyProjectionLanes(t *testing.T) {
	store, err := detachment.Open(filepath.Join(t.TempDir(), "detachments.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Record(detachment.Record{ServerID: "manual", Cause: detachment.CauseRevoked, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if serverMayStart("manual", store) {
		t.Fatal("detached server may restart repository transports")
	}
	if !serverMayStart("spot", store) {
		t.Fatal("unrelated server was prevented from starting")
	}

	update := &config.UpdateConfig{Channel: "stable"}
	view := withoutDetachedClientView(config.ClientView{
		ServerID: "manual", DisplayName: "manual", Configured: true,
		Projection: &config.Projection{WorkingCopy: `C:\stale`}, Update: update, UpdateConfigured: true,
	}, store)
	if view.Configured || view.Projection != nil || view.ServerID != "" {
		t.Fatalf("legacy projection survived: %+v", view)
	}
	if view.Update != update || !view.UpdateConfigured {
		t.Fatalf("unrelated update configuration was lost: %+v", view)
	}
}
