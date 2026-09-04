package contracttests

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"filees/pkg/clientview"
)

func TestClientViewV2CutsPresentationFromTransportIdentity(t *testing.T) {
	view := clientview.View{
		Schema: clientview.Schema, ServerDisplayName: "Cloud ATM Projekt",
		ClientID:   "399c0801-46d2-4190-bd70-15a9bf6cfa00",
		RealmID:    "a72d443d-342b-4ed8-9412-925247dbd4c5",
		Generation: 1, GeneratedAt: time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC),
		ClientRole: "normal", Repositories: []clientview.Repository{}, ActiveOperations: []json.RawMessage{},
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := clientview.Decode(bytes.NewReader(raw))
	if err != nil || decoded.ServerDisplayName != "Cloud ATM Projekt" {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}

	var broken map[string]any
	if err := json.Unmarshal(raw, &broken); err != nil {
		t.Fatal(err)
	}
	delete(broken, "server_display_name")
	withoutName, _ := json.Marshal(broken)
	if _, err := clientview.Decode(bytes.NewReader(withoutName)); err == nil {
		t.Fatal("v2 client accepted a projection without server_display_name")
	}
	broken["schema"] = "filees.client-view/v1"
	broken["server_display_name"] = "Cloud ATM Projekt"
	legacy, _ := json.Marshal(broken)
	if _, err := clientview.Decode(bytes.NewReader(legacy)); err == nil {
		t.Fatal("v2 client accepted the retired v1 projection")
	}
}
