package main

import (
	"filees/internal/gui/platform"
	"testing"
)

func TestIntentResolutionFolderActionProjection(t *testing.T) {
	request := platform.SettingsDialogRequest{FocusRepoID: "docs", Servers: []platform.SettingsServer{{ID: "spot", Folders: []platform.SettingsFolder{{ID: "docs", CanResolveIntents: true}}}}}
	projection, ok := projectRepositorySettings(request)
	if !ok || len(projection.Actions) != 1 || projection.Actions[0].ID != string(platform.SettingsDialogResolveIntents) {
		t.Fatalf("projection=%+v", projection)
	}
	request.Servers[0].Folders[0].CanResolveIntents = false
	projection, ok = projectRepositorySettings(request)
	if !ok || len(projection.Actions) != 0 {
		t.Fatal("renderer invented permission")
	}
}
