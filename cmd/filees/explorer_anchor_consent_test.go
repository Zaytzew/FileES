//go:build !nocfapi

package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	contract "filees/pkg/contract/v1"
	"filees/pkg/localrepo"
	"filees/pkg/talk"
)

func TestAnchorConsentBridgeRequiresNewOpenAfterDecision(t *testing.T) {
	guard := newAnchorGuard(talk.With("consent-pipe"))
	guard.limit = 1
	program := filepath.Join(t.TempDir(), "reader.exe")
	wanted := filepath.Join(t.TempDir(), "file.dwg")
	var calls atomic.Int32
	guard.changed = func() { _ = guard.pending() }
	helper, _ := runAnchorBridge(t, &anchorBridge{guard: guard, materialize: func(context.Context, string) (string, int64, error) {
		calls.Add(1)
		return wanted, 7, nil
	}})
	request := func(id, path string) []string {
		t.Helper()
		if _, err := io.WriteString(helper.toDaemon, "fetch\t"+id+"\t0\t4096\t"+path+"\t"+program+"\n"); err != nil {
			t.Fatal(err)
		}
		return helper.answer(t)
	}
	if reply := request("1", "first.dwg"); reply[0] != "ok" {
		t.Fatal(reply)
	}
	if reply := request("2", "second.dwg"); reply[0] != "err" || calls.Load() != 1 {
		t.Fatal("refused open reached materializer", reply, calls.Load())
	}
	blocks := guard.pending()
	if len(blocks) != 1 {
		t.Fatal(blocks)
	}
	if err := guard.decide(blocks[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("consent retried the rejected request")
	}
	if reply := request("3", "second.dwg"); reply[0] != "ok" || calls.Load() != 2 {
		t.Fatal("fresh open was not admitted", reply, calls.Load())
	}
}

func refusedGuard(t *testing.T) (*anchorGuard, string) {
	t.Helper()
	guard := newAnchorGuard(talk.With("consent-test"))
	process := filepath.Join(t.TempDir(), "reader.exe")
	for i := 0; i <= guard.limit; i++ {
		err := guard.allow(process, fmt.Sprint(i), time.Now())
		if (i == guard.limit) != (err != nil) {
			t.Fatal(i, err)
		}
	}
	return guard, process
}

func TestAnchorConsentIsOneProgramOneProviderSession(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprint(allow), func(t *testing.T) {
			guard, process := refusedGuard(t)
			blocks := guard.pending()
			if len(blocks) != 1 || blocks[0].Program != "reader.exe" || blocks[0].Files != 21 || len(blocks[0].ID) != 32 {
				t.Fatal(blocks)
			}
			id := blocks[0].ID
			blocks[0].ID = "modified"
			if guard.pending()[0].ID != id {
				t.Fatal("snapshot aliases live guard")
			}
			if err := guard.decide(strings.Repeat("0", 32), true); err == nil {
				t.Fatal("unknown consent")
			}
			changed := 0
			guard.changed = func() { changed++; _ = guard.pending() } // reentrant, must not hold guard.mu
			if err := guard.decide(id, allow); err != nil {
				t.Fatal(err)
			}
			if changed != 1 || len(guard.pending()) != 0 {
				t.Fatal(changed, guard.pending())
			}
			if guard.decide(id, true) == nil {
				t.Fatal("decision replay accepted")
			}
			if newAnchorGuard(talk.With("new-session")).decide(id, true) == nil {
				t.Fatal("consent crossed restart")
			}
			for i := 0; i < 200; i++ {
				err := guard.allow(strings.ToUpper(process), fmt.Sprint(i), time.Now().Add(2*time.Minute))
				if (err == nil) != allow {
					t.Fatal("case/session scope", err)
				}
			}
			if len(guard.pending()) != 0 {
				t.Fatal("refusal repeated prompt")
			}
			other := filepath.Join(t.TempDir(), "other.exe")
			for i := 0; i <= guard.limit; i++ {
				_ = guard.allow(other, fmt.Sprint(i), time.Now())
			}
			if len(guard.pending()) != 1 {
				t.Fatal("approval leaked to another program")
			}
		})
	}
}

func TestAnchorGuardBoundsRepeatedAndDifferentRequests(t *testing.T) {
	guard := newAnchorGuard(talk.With("bounded-guard"))
	process := filepath.Join(t.TempDir(), "reader.exe")
	now := time.Now()
	for i := 0; i < 10000; i++ {
		if err := guard.allow(process, "same.dwg", now); err != nil {
			t.Fatal(err)
		}
	}
	if len(guard.recent[strings.ToLower(process)]) != 1 {
		t.Fatal("one file grew request history")
	}
	for i := 0; i < 1000; i++ {
		_ = guard.allow(filepath.Join(filepath.Dir(process), fmt.Sprintf("p%d.exe", i)), "one", now)
	}
	if len(guard.recent) != 64 {
		t.Fatal("program table unbounded", len(guard.recent))
	}
	unknown := newAnchorGuard(talk.With("unknown"))
	for i := 0; i <= unknown.limit; i++ {
		_ = unknown.allow("", fmt.Sprint(i), now)
	}
	if len(unknown.pending()) != 0 || unknown.allow("", "more", now) == nil {
		t.Fatal("unidentified requester may be approved")
	}
}

func TestAnchorConsentConcurrentReplayHasOneWinner(t *testing.T) {
	guard, _ := refusedGuard(t)
	id := guard.pending()[0].ID
	results := make(chan error, 32)
	var wait sync.WaitGroup
	for i := 0; i < cap(results); i++ {
		wait.Add(1)
		go func() { defer wait.Done(); results <- guard.decide(id, true) }()
	}
	wait.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatal(winners)
	}
}

func TestAnchorConsentManagerRefusesRetiredOrDifferentPoint(t *testing.T) {
	for _, variant := range []string{"current", "cancelled", "retired", "root", "url", "server", "repo", "bridge", "lifecycle", "detach"} {
		t.Run(variant, func(t *testing.T) {
			store, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
			if err != nil {
				t.Fatal(err)
			}
			record, err := store.BeginAnchorAttach("office", "repo", t.TempDir(), false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.ApproveAttach(record.OperationID, "office", "repo", "svn+ssh://example/repo", "r"); err != nil {
				t.Fatal(err)
			}
			record, err = store.MarkAttached(record.OperationID, "repo")
			if err != nil {
				t.Fatal(err)
			}
			guard, _ := refusedGuard(t)
			id := guard.pending()[0].ID
			providerCtx, retire := context.WithCancel(t.Context())
			defer retire()
			connection := anchorConnection{ctx: t.Context(), providerCtx: providerCtx, repoURL: record.RepoURL, bridge: &anchorBridge{root: record.LocalPath, guard: guard}}
			p := contract.RepoAnchorConsentPayload{ServerID: "office", RepoID: "repo", BlockID: id, Allow: true}
			var lifecycleMu sync.Mutex
			m := &anchorManager{lifecycle: store, localIntentMu: &lifecycleMu, running: map[string]anchorConnection{}}
			caller, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch variant {
			case "cancelled":
				cancel()
			case "retired":
				retire()
			case "root":
				connection.bridge.root += "-other"
			case "url":
				connection.repoURL += "-other"
			case "server":
				p.ServerID = "elsewhere"
			case "repo":
				p.RepoID = "elsewhere"
			case "bridge":
				connection.bridge = nil
			case "lifecycle":
				m.localIntentMu = nil
			case "detach":
				if _, err := store.BeginDetach("office", "repo", false); err != nil {
					t.Fatal(err)
				}
			}
			m.running[anchorKey(record)] = connection
			err = m.consent(caller, p)
			if variant == "current" {
				if err != nil || len(guard.pending()) != 0 {
					t.Fatal(err)
				}
			} else if err == nil || len(guard.pending()) != 1 {
				t.Fatal("stale context changed guard", err)
			}
		})
	}
}
