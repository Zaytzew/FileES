package main

import (
	"errors"
	contract "filees/pkg/contract/v1"
	"filees/pkg/localrepo"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"testing"
)

func TestWCConversionPublicIntentStalePathRetryCancelAndNewIntent(t *testing.T) {
	f := newDaemonConversionFixture(t)
	request := contract.WCConversionPayload{ServerID: f.r.ServerID, RepoID: f.r.RepoID, Action: "begin", SourcePath: f.r.LocalPath + "-stale"}
	if _, err := f.p.WCConversion(t.Context(), request); err == nil {
		t.Fatal("stale path admitted")
	}
	if r, _ := f.p.local.Get(f.r.OperationID); r.ConversionID != "" {
		t.Fatal("stale request reserved WC")
	}
	request.SourcePath = f.r.LocalPath
	first, err := f.p.WCConversion(t.Context(), request)
	if err != nil || first.OperationID == "" || first.Phase != "waiting" {
		t.Fatal(first, err)
	}
	again, err := f.p.WCConversion(t.Context(), request)
	if err != nil || again.OperationID != first.OperationID {
		t.Fatal("lost ACK duplicated intent", again, err)
	}
	r, _ := f.p.local.Get(f.r.OperationID)
	j, _ := f.p.conversionJournal(r, false)
	blocked, err := j.Block(j.Get(), errors.New("fixture reason"))
	if err != nil {
		t.Fatal(err)
	}
	control := contract.WCConversionPayload{ServerID: r.ServerID, RepoID: r.RepoID, Action: "retry", OperationID: uuid.NewString()}
	if _, err := f.p.WCConversion(t.Context(), control); err == nil || j.Get() != blocked {
		t.Fatal("stale operation controlled current intent", err)
	}
	control.OperationID = first.OperationID
	status := f.p.WCConversionStatus(r.ServerID, r.RepoID)
	if !status.CanRetry || !status.CanRollback || status.Error != "fixture reason" {
		t.Fatal(status)
	}
	if _, err = f.p.WCConversion(t.Context(), control); err != nil || j.Get().Blocked != "" {
		t.Fatal(err)
	}
	control.Action = "rollback"
	if _, err = f.p.WCConversion(t.Context(), control); err != nil || j.Get().Phase != localrepo.ConversionCanceled {
		t.Fatal(err)
	}
	// A canceled, unprepared intent has no effects and finalizes the original.
	f.p.runOne(t.Context(), r.OperationID)
	finished, _ := f.p.local.Get(r.OperationID)
	if finished.ConversionID != "" || finished.Anchor {
		t.Fatal(finished)
	}
	f.p.conversionJournals = nil // reopen the terminal journal, not just its cache
	next, err := f.p.WCConversion(t.Context(), request)
	if err != nil || next.OperationID == first.OperationID {
		t.Fatal("new confirmed intent after cancel", next, err)
	}
	archive := filepath.Join(f.p.conversionRoot, r.OperationID+"-"+first.OperationID+".json")
	if _, err := os.Stat(archive); err != nil {
		t.Fatal("terminal evidence lost", err)
	}
}

func TestWCConversionPresentationDoesNotInventCountersOrAuthority(t *testing.T) {
	f := newDaemonConversionFixture(t)
	r, err := f.p.queueConversion(t.Context(), f.r.ServerID, f.r.RepoID)
	if err != nil {
		t.Fatal(err)
	}
	f.p.observeConversion(r.OperationID, conversionObservation{Running: true, Known: true, Phase: "preserving", Done: 3, Total: 7})
	p := f.p.WCConversionStatus(r.ServerID, r.RepoID)
	if p.Phase != "waiting" || p.CountersKnown || !p.Running || p.CanRollback {
		t.Fatal("stale phase counters/control leaked", p)
	}
	f.p.finishConversionObservation(r.OperationID)
	f.p.relocationCheck = func(localrepo.Record) error { return errors.New("authority changed") }
	if _, err := f.p.WCConversion(t.Context(), contract.WCConversionPayload{ServerID: r.ServerID, RepoID: r.RepoID, Action: "rollback", OperationID: r.ConversionID}); err == nil {
		t.Fatal("control bypassed authority")
	}
}

func TestWCConversionControlCountersAreEphemeralAndPerPass(t *testing.T) {
	f := newDaemonConversionFixture(t)
	r, err := f.p.queueConversion(t.Context(), f.r.ServerID, f.r.RepoID)
	if err != nil {
		t.Fatal(err)
	}
	emitted := 0
	f.p.conversionChanged = func(string) { emitted++ }
	f.p.observeConversion(r.OperationID, conversionObservation{Running: true, Phase: "inspecting", Check: "source", Done: 17})
	s := f.p.WCConversionStatus(r.ServerID, r.RepoID)
	if s.Phase != "inspecting" || s.Check != "source" || s.Done != 17 || s.CountersKnown || s.Total != 0 {
		t.Fatal(s)
	}
	// A new pass resets its count and is emitted immediately even within the
	// same second. Status presentation itself never reads the source tree.
	f.p.observeConversion(r.OperationID, conversionObservation{Running: true, Phase: "inspecting", Check: "source", Done: 0})
	if emitted != 2 {
		t.Fatal("reset not emitted", emitted)
	}
	f.p.observeConversion(r.OperationID, conversionObservation{Running: true, Phase: "inspecting", Check: "custody", Known: true, Done: 1, Total: 2})
	if emitted != 3 {
		t.Fatal("new check not emitted", emitted)
	}
	f.p.conversionProgress = nil // equivalent to a newly started daemon
	s = f.p.WCConversionStatus(r.ServerID, r.RepoID)
	if s.Check != "" || s.Done != 0 || s.CountersKnown || s.Running {
		t.Fatal("restart reused counters", s)
	}
}
