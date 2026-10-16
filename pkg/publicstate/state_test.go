package publicstate

import (
	"encoding/json"
	"strings"
	"testing"

	control "filees/pkg/control/v1"
	"github.com/google/uuid"
)

func fixture() (Binding, []Item) {
	b := Binding{ServerID: "lab", ClientID: uuid.NewString(), RealmID: uuid.NewString()}
	items := []Item{}
	for i := 0; i < 55; i++ {
		items = append(items, Item{RepoDisplayName: "Project", Share: control.PublicShareSummary{ChannelID: uuid.NewString(), RepoID: uuid.NewString(), State: "active", Objects: []control.PublicShareObject{{RepoPath: strings.Repeat("path/", 100), DisplayName: "document"}}}})
	}
	return b, items
}

func TestFullInitThenOnlyChangedLinkAndRemoval(t *testing.T) {
	b, items := fixture()
	var s Snapshot
	req := s.Request()
	// Empty known is omitted on the wire; its base must remain identical.
	raw, _ := json.Marshal(req)
	wire, err := ParseRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Build(b, wire, items)
	if err != nil {
		t.Fatal(err)
	}
	full, _ := json.Marshal(r)
	r, err = ParseResponse(full)
	if err != nil {
		t.Fatal(err)
	}
	s, err = s.Apply(req, r, b)
	if err != nil || len(s.Items) != 55 || len(r.Changes) != 55 {
		t.Fatalf("init: %v", err)
	}
	req = s.Request()
	r, err = Build(b, req, items)
	if err != nil || len(r.Changes) != 0 {
		t.Fatalf("unchanged: %v", err)
	}
	ack, _ := json.Marshal(r)
	if len(ack)*3 >= len(full) {
		t.Fatalf("ACK=%d full=%d", len(ack), len(full))
	}
	items[0].Share.State = "revoked"
	deleted := items[54].Share.ChannelID
	items = items[:54]
	r, err = Build(b, req, items)
	if err != nil || len(r.Changes) != 1 {
		t.Fatalf("delta: %v", err)
	}
	next, err := s.Apply(req, r, b)
	if err != nil || len(next.Items) != 54 || next.Items[items[0].Share.ChannelID].Share.State != "revoked" {
		t.Fatalf("merge: %v", err)
	}
	if _, exists := next.Items[deleted]; exists {
		t.Fatal("deleted link retained")
	}
	if len(s.Items) != 55 {
		t.Fatal("previous snapshot mutated")
	}
	r, _ = Build(b, next.Request(), nil)
	next, err = next.Apply(next.Request(), r, b)
	if err != nil || len(next.Items) != 0 {
		t.Fatalf("empty: %v", err)
	}
	t.Logf("55 links full=%d bytes unchanged=%d bytes, no changed payloads", len(full), len(ack))
}

func TestDeltaRejectsWrongBaseBindingMissingCacheAndCorruption(t *testing.T) {
	b, items := fixture()
	initial := Snapshot{}
	req := initial.Request()
	r, _ := Build(b, req, items[:2])
	s, err := initial.Apply(req, r, b)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"base", "realm", "client", "server", "incomplete", "missing inventory", "missing cache", "corrupt cache", "corrupt change", "extra change", "duplicate"} {
		t.Run(name, func(t *testing.T) {
			req := s.Request()
			r, _ := Build(b, req, items[:2])
			copy := Snapshot{Binding: s.Binding, Items: map[string]Item{}}
			for id, item := range s.Items {
				copy.Items[id] = item
			}
			switch name {
			case "base":
				r.Base = "other"
			case "realm":
				r.Binding.RealmID = uuid.NewString()
			case "client":
				r.Binding.ClientID = uuid.NewString()
			case "server":
				r.Binding.ServerID = "other"
			case "incomplete":
				r.Complete = false
			case "missing inventory":
				r.Versions = nil
			case "missing cache":
				delete(copy.Items, items[0].Share.ChannelID)
			case "corrupt cache":
				changed := items[0]
				changed.Share.State = "revoked"
				copy.Items[changed.Share.ChannelID] = changed
			case "corrupt change":
				changed := items[0]
				changed.Share.State = "revoked"
				r.Changes = []Item{changed}
			case "extra change":
				r.Changes = []Item{items[2]}
			case "duplicate":
				r.Changes = []Item{items[0], items[0]}
			}
			if _, err := copy.Apply(req, r, b); err == nil {
				t.Fatal("invalid delta accepted")
			}
		})
	}
	changedBinding := b
	changedBinding.ClientID = uuid.NewString()
	r, _ = Build(changedBinding, s.Request(), items[:2])
	if len(r.Changes) != 2 {
		t.Fatal("new activation reused old versions")
	}
}

func TestStrictPublicLinkStateParsingAndLimits(t *testing.T) {
	for _, raw := range []string{`{"schema":"wrong"}`, `{"schema":"` + Schema + `","extra":1}`, `{"schema":"` + Schema + `"} {}`, `{"schema":"` + Schema + `","known":{"../bad":"00"}}`, strings.Repeat(" ", MaxRequestBytes+1)} {
		if _, err := ParseRequest([]byte(raw)); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	if _, err := ParseResponse([]byte(`{"schema":"` + Schema + `"}`)); err == nil {
		t.Fatal("incomplete response accepted")
	}
	b, items := fixture()
	if _, err := Build(b, Request{Schema: Schema}, append(items, items[0])); err == nil {
		t.Fatal("duplicate source accepted")
	}
}
