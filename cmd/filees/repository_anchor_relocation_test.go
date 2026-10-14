package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/localrepo"
)

func TestAnchorRelocationRequiresProviderDrainBeforeRename(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "drained", true: "refused"}[fail], func(t *testing.T) {
			f := newQueuedMoveFixture(t)
			called := false
			wakes := 0
			f.p.anchorChanged = func() { wakes++ }
			f.p.quiesceAnchor = func(ctx context.Context, record localrepo.Record) error {
				called = true
				if _, err := os.Stat(record.LocalPath); err != nil {
					t.Fatal("rename preceded provider drain", err)
				}
				if _, err := os.Stat(record.PendingLocalPath); !os.IsNotExist(err) {
					t.Fatal("destination exists before drain", err)
				}
				if fail {
					return errors.New("provider drain refused")
				}
				return nil
			}
			// Durable root kind controls the barrier, never the presence of a helper.
			f.record.Anchor = true
			if err := markFixtureAnchor(f); err != nil {
				t.Fatal(err)
			}
			f.execute(t, nil)
			if !called {
				t.Fatal("move did not wait for provider drain")
			}
			if wakes != 1 {
				t.Fatal("move/rollback did not wake provider reconciliation", wakes)
			}
			if fail {
				record, _ := f.p.local.Get(f.record.OperationID)
				if record.State != localrepo.StateAttached || record.LocalPath != f.record.LocalPath || record.LastError == "" {
					t.Fatal("failed drain lost source", record)
				}
				if data, err := os.ReadFile(filepath.Join(record.LocalPath, "draft.txt")); err != nil || string(data) != "unpublished" {
					t.Fatal("failed drain changed bytes", err)
				}
			} else {
				f.assertMoved(t)
			}
		})
	}
}

func TestAnchorRelocationRefusesMissingDrainWhileOrdinaryMoveNeedsNone(t *testing.T) {
	for _, anchor := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "anchor"}[anchor], func(t *testing.T) {
			f := newQueuedMoveFixture(t)
			if anchor {
				if err := markFixtureAnchor(f); err != nil {
					t.Fatal(err)
				}
			}
			f.execute(t, nil)
			if !anchor {
				f.assertMoved(t)
				return
			}
			record, _ := f.p.local.Get(f.record.OperationID)
			if record.LocalPath != f.record.LocalPath || record.LastError == "" {
				t.Fatal("moved without provider drain", record)
			}
		})
	}
}

func markFixtureAnchor(f queuedMoveFixture) error {
	// Persist through the existing representation, without a product setter.
	raw, err := os.ReadFile(f.storePath)
	if err != nil {
		return err
	}
	var disk struct {
		Schema  string             `json:"schema"`
		Records []localrepo.Record `json:"records"`
	}
	if err := json.Unmarshal(raw, &disk); err != nil {
		return err
	}
	disk.Records[0].Anchor = true
	disk.Records[0].Sparse = true
	raw, err = json.Marshal(disk)
	if err != nil {
		return err
	}
	if err := os.WriteFile(f.storePath, raw, 0600); err != nil {
		return err
	}
	f.p.local, err = localrepo.Open(f.storePath)
	return err
}
