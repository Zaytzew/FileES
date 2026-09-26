package alertchannel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestIncidentLifecycleAndLocalReadReceipt(t *testing.T) {
	realm := uuid.NewString()
	now := time.Now().UTC()
	s, changed, err := (Snapshot{}).Change(realm, "storage.var", "error", "active", "Brak miejsca", now)
	if err != nil || !changed {
		t.Fatal(err)
	}
	same, changed, err := s.Change(realm, "storage.var", "error", "active", "Brak miejsca", now.Add(time.Minute))
	if err != nil || changed || same.Generation != s.Generation {
		t.Fatal("publication was not idempotent", err)
	}
	cache := filepath.Join(t.TempDir(), "alerts.json")
	b, err := Open(cache, "host", realm)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Accept(s, now); err != nil {
		t.Fatal(err)
	}
	rows, _ := b.Notices()
	id := rows[0].ID
	if rows[0].Acked || rows[0].Stale {
		t.Fatal(rows)
	}
	if err = b.Ack(id); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(cache, "host", realm)
	if err != nil {
		t.Fatal(err)
	}
	rows, _ = restarted.Notices()
	if !rows[0].Acked || !rows[0].Stale {
		t.Fatal(rows)
	}
	if err = restarted.Accept(s, now); err != nil {
		t.Fatal(err)
	}
	rows, _ = restarted.Notices()
	if !rows[0].Acked || rows[0].Stale {
		t.Fatal(rows)
	}
	b2, _ := Open(filepath.Join(t.TempDir(), "other.json"), "host", realm)
	_ = b2.Accept(s, now)
	other, _ := b2.Notices()
	if other[0].Acked {
		t.Fatal("read receipt leaked to another desktop")
	}
	resolved, _, err := s.Change(realm, "storage.var", "error", "resolved", "Miejsce zwolnione", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Accept(resolved, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	rows, _ = b.Notices()
	if !rows[0].Acked || rows[0].Status != "resolved" {
		t.Fatal("recovery was loud", rows)
	}
	if err = b.Accept(s, now); err == nil {
		t.Fatal("accepted old generation")
	}
	again, _, err := resolved.Change(realm, "storage.var", "error", "active", "Brak miejsca", now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if again.Incidents[0].ID == s.Incidents[0].ID {
		t.Fatal("recurrence reused incident")
	}
	_ = b.Accept(again, now)
	rows, _ = b.Notices()
	if rows[0].Acked {
		t.Fatal(rows)
	}
	b.Failed()
	rows, _ = b.Notices()
	if !rows[0].Stale || rows[0].Status != "active" {
		t.Fatal("failure cleared incident", rows)
	}
}
func TestSnapshotValidationAndCapacity(t *testing.T) {
	realm := uuid.NewString()
	now := time.Now()
	s, _, _ := (Snapshot{}).Change(realm, "one", "warning", "active", "Safe", now)
	raw, _ := json.Marshal(s)
	if _, err := Decode(raw, uuid.NewString()); err == nil {
		t.Fatal("cross realm accepted")
	}
	if _, err := Decode(append(raw, []byte("{}")...), realm); err == nil {
		t.Fatal("trailing document accepted")
	}
	if _, err := Decode([]byte(strings.Repeat("x", MaxBytes+1)), realm); err == nil {
		t.Fatal("oversized accepted")
	}
	if _, _, err := s.Change(realm, "two", "warning", "active", "line\ninjection", now); err == nil {
		t.Fatal("control accepted")
	}
	if _, _, err := s.Change(realm, "two", "warning", "resolved", "Unknown", now); err == nil {
		t.Fatal("resolved unknown")
	}
	s.Incidents = nil
	for i := 0; i < MaxRecords; i++ {
		v := newIncident(uuid.NewString(), "warning", "active", "Safe", now)
		s.Incidents = append(s.Incidents, v)
	}
	if _, _, err := s.Change(realm, "extra", "error", "active", "Full", now); err == nil {
		t.Fatal("active record evicted")
	}
	s.Incidents[0].Status = "resolved"
	next, _, err := s.Change(realm, "extra", "error", "active", "Full", now)
	if err != nil || len(next.Incidents) != MaxRecords {
		t.Fatal(err)
	}
}

func TestCorruptCacheCanBeRebuiltFromServer(t *testing.T) {
	realm := uuid.NewString()
	cache := filepath.Join(t.TempDir(), "alerts.json")
	if err := os.WriteFile(cache, []byte(`{"snapshot":`), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := Open(cache, "host", realm)
	if err == nil || b == nil {
		t.Fatal("expected reported corruption with empty recoverable inbox", err)
	}
	s, _, _ := (Snapshot{}).Change(realm, "disk", "error", "active", "Low space", time.Now())
	if err = b.Accept(s, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(cache, "host", realm); err != nil {
		t.Fatal(err)
	}
}
