package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/clientview"
)

func demoSync(t *testing.T, expiresAt time.Time, write bool) clientview.SyncConfig {
	t.Helper()
	wc := t.TempDir()
	dir := filepath.Join(wc, "clients", "c1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if write {
		raw, err := json.Marshal(clientview.Demo{Schema: clientview.DemoSchema, ExpiresAt: expiresAt})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, clientview.DemoFileName), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return clientview.SyncConfig{WorkingCopy: wc, RelativeViewPath: filepath.Join("clients", "c1", "view.json")}
}

func TestAnEndedDemoIsNotContactedAgain(t *testing.T) {
	now := time.Now()
	if !demoDeadlinePassed(demoSync(t, now.Add(-time.Minute), true), now) {
		t.Fatal("a demo whose announced end has passed must not be dialled")
	}
	if demoDeadlinePassed(demoSync(t, now.Add(2*time.Hour), true), now) {
		t.Fatal("a running demo must keep synchronising")
	}
	// Every ordinary server, and a demo whose announcement cannot be read.
	if demoDeadlinePassed(demoSync(t, time.Time{}, false), now) {
		t.Fatal("a server without a demo announcement must never be held back")
	}
	if demoDeadlinePassed(clientview.SyncConfig{}, now) {
		t.Fatal("an empty sync configuration must never be held back")
	}
}

func TestTheEndedDemoEntersTheSameDetachedStateAsARefusal(t *testing.T) {
	if !isDetachedClient(errDemoEnded) {
		t.Fatal("the ended demo must reach the detached lane the refusal uses")
	}
}
