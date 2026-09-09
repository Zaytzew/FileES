package commit

import (
	"bytes"
	"context"
	"encoding/json"
	contract "filees/pkg/contract/v1"
	"filees/pkg/errmap"
	"filees/pkg/watcher"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func intentFixture(t *testing.T) *Service {
	t.Helper()
	wc := t.TempDir()
	cli := &stagingClient{statuses: map[string]string{"new V2.txt": "unversioned", "old.txt": "missing"}}
	s := &Service{Cli: cli, wc: wc, repoID: "intent-test", cachePath: filepath.Join(wc, ".filees", "commit_cache", "cache.json"), staging: map[string]*stageItem{}}
	for rel, op := range map[string]watcher.OpType{"new V2.txt": watcher.RenameUncertain, "old.txt": watcher.Deleted, "unrelated.txt": watcher.Modified} {
		s.staging[rel] = &stageItem{Rel: rel, Abs: filepath.Join(wc, rel), Op: op, FirstSeen: time.Now(), LastSeen: time.Now()}
	}
	if err := os.WriteFile(filepath.Join(wc, "new V2.txt"), []byte("new document"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(s.cachePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.saveCacheChecked(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestIntentDecisionAtomicDurableAndIdempotent(t *testing.T) {
	s := intentFixture(t)
	before, _ := os.ReadFile(s.cachePath)
	plan, err := s.PlanIntents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Paths) != 2 || plan.Choice != contract.IntentDeleteAdd || plan.Paths[0].SHA256 == "" {
		t.Fatalf("plan=%+v", plan)
	}
	after, _ := os.ReadFile(s.cachePath)
	if string(before) != string(after) || s.staging["new V2.txt"].Op != watcher.RenameUncertain {
		t.Fatal("planning mutated queue")
	}
	unrelated := *s.staging["unrelated.txt"]
	if _, err = s.ApplyIntents(t.Context(), plan.PlanID, plan.Choice); err != nil {
		t.Fatal(err)
	}
	if s.staging["new V2.txt"].Op != watcher.Added || s.staging["old.txt"].Op != watcher.Deleted || !reflect.DeepEqual(unrelated, *s.staging["unrelated.txt"]) {
		t.Fatal("wrong queue conversion")
	}
	var durable intentCache
	raw, _ := os.ReadFile(s.cachePath)
	if err := json.Unmarshal(raw, &durable); err != nil || len(durable.Receipts) != 1 || durable.Receipts[0].PlanID != plan.PlanID {
		t.Fatalf("receipt=%+v err=%v", durable, err)
	}
	restarted := &Service{wc: s.wc, repoID: s.repoID, cachePath: s.cachePath, staging: map[string]*stageItem{}}
	restarted.loadCache()
	if restarted.staging["new V2.txt"].Op != watcher.Added {
		t.Fatal("lost decision on restart")
	}
	if _, err := restarted.ApplyIntents(t.Context(), plan.PlanID, plan.Choice); err != nil {
		t.Fatal(err)
	}
	if err := restarted.saveCacheChecked(); err != nil {
		t.Fatal(err)
	}
	if len(restarted.intentReceipts) != 1 {
		t.Fatal("duplicate receipt")
	}
	// Rediscovery from the legacy baseline cannot undo the accepted decision.
	restarted.addEvent(watcher.Event{Op: watcher.RenameUncertain, Type: watcher.EntryFile, Rel: "new V2.txt", Path: filepath.Join(s.wc, "new V2.txt")})
	if restarted.staging["new V2.txt"].Op != watcher.Added {
		t.Fatal("restart rediscovery undid decision")
	}
	if err := os.WriteFile(filepath.Join(s.wc, "new V2.txt"), []byte("different replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	restarted.addEvent(watcher.Event{Op: watcher.RenameUncertain, Type: watcher.EntryFile, Rel: "new V2.txt", Path: filepath.Join(s.wc, "new V2.txt")})
	if restarted.staging["new V2.txt"].Op != watcher.RenameUncertain {
		t.Fatal("receipt authorized changed file")
	}
	// Once the pending item is gone, even original bytes cannot reuse the receipt.
	delete(restarted.staging, "new V2.txt")
	_ = os.WriteFile(filepath.Join(s.wc, "new V2.txt"), []byte("new document"), 0600)
	restarted.addEvent(watcher.Event{Op: watcher.RenameUncertain, Type: watcher.EntryFile, Rel: "new V2.txt", Path: filepath.Join(s.wc, "new V2.txt")})
	if restarted.staging["new V2.txt"].Op != watcher.RenameUncertain {
		t.Fatal("receipt became permanent path approval")
	}
}

func TestIntentPlanRefusesStaleAndUnsafeChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Service)
	}{
		{"edited", func(s *Service) { _ = os.WriteFile(filepath.Join(s.wc, "new V2.txt"), []byte("edited"), 0600) }},
		{"restored deletion", func(s *Service) { _ = os.WriteFile(filepath.Join(s.wc, "old.txt"), []byte("restored"), 0600) }},
		{"unrelated queue changed", func(s *Service) { s.staging["unrelated.txt"].LastSeen = time.Now().Add(time.Minute) }},
		{"expired", func(s *Service) { s.intentPlan.expires = time.Now().Add(-time.Second) }},
		{"unknown plan", func(s *Service) { s.intentPlan = nil }},
		{"scheduled", func(s *Service) { s.staging["new V2.txt"].MoveScheduled = true }},
		{"old rel", func(s *Service) { s.staging["new V2.txt"].OldRel = "old.txt" }},
		{"directory", func(s *Service) { s.staging["old.txt"].IsDir = true }},
		{"svn changed", func(s *Service) { s.Cli.(*stagingClient).statuses["new V2.txt"] = "added" }},
		{"transaction", func(s *Service) { _ = os.WriteFile(transactionPath(s.wc), []byte("broken"), 0600) }},
		{"invalid done receipt", func(s *Service) {
			_ = os.WriteFile(transactionPath(s.wc), []byte(`{"schema":"filees.commit-transaction/v1","phase":"done"}`), 0600)
		}},
		{"outside", func(s *Service) { s.staging["new V2.txt"].Abs = filepath.Join(filepath.Dir(s.wc), "outside") }},
		{"write failure", func(s *Service) { s.cachePath = filepath.Join(s.wc, "absent", "cache.json") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := intentFixture(t)
			original := s.cachePath
			before, _ := os.ReadFile(original)
			plan, err := s.PlanIntents(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(s)
			if _, err = s.ApplyIntents(t.Context(), plan.PlanID, plan.Choice); err == nil {
				t.Fatal("unsafe plan accepted")
			}
			after, _ := os.ReadFile(original)
			if string(before) != string(after) || len(s.intentReceipts) != 0 || s.staging["new V2.txt"].Op != watcher.RenameUncertain {
				t.Fatal("failed decision mutated state")
			}
		})
	}
}

func TestIntentDecisionRejectsChoiceAndCancelledContext(t *testing.T) {
	s := intentFixture(t)
	plan, err := s.PlanIntents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyIntents(t.Context(), plan.PlanID, "rename"); err == nil {
		t.Fatal("accepted unsupported choice")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.ApplyIntents(ctx, plan.PlanID, plan.Choice); err == nil {
		t.Fatal("accepted canceled inspection")
	}
}

func TestIntentHoldIsTypedAndDeduplicated(t *testing.T) {
	s := intentFixture(t)
	var log bytes.Buffer
	s.ErrSink = errmap.NewSink(&log, "intent-test")
	now := time.Now()
	for i := 0; i < 100; i++ {
		err := s.reportIntentHold(now.Add(time.Duration(i) * time.Second))
		if entry := errmap.Classify(err); entry.Code != "INTENT-1002" {
			t.Fatal(entry)
		}
		s.recordCommitFailure("test", err)
	}
	if bytes.Count(log.Bytes(), []byte("\n")) != 1 {
		t.Fatal("repeated HOLD", log.String())
	}
	s.reportIntentHold(now.Add(15 * time.Minute))
	if bytes.Count(log.Bytes(), []byte("\n")) != 2 {
		t.Fatal("missing reminder")
	}
}

func TestIntentReceiptCannotMaskRestoredSource(t *testing.T) {
	s := intentFixture(t)
	plan, err := s.PlanIntents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyIntents(t.Context(), plan.PlanID, plan.Choice); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.wc, "old.txt"), []byte("restored"), 0600); err != nil {
		t.Fatal(err)
	}
	s.addEvent(watcher.Event{Op: watcher.RenameUncertain, Type: watcher.EntryFile, Rel: "new V2.txt", Path: filepath.Join(s.wc, "new V2.txt")})
	if s.staging["new V2.txt"].Op != watcher.RenameUncertain {
		t.Fatal("restored source was ignored")
	}
}
