package commit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/errcat"
	"filees/pkg/errmap"
)

func TestCanceledCompletedInspectionDoesNotRequestUserAction(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, errors.New("independent disk error")} {
		t.Run(cause.Error(), func(t *testing.T) {
			s, base, wc := conflictRecoveryFixture(t)
			in, err := s.readIntent(wc)
			if err != nil {
				t.Fatal(err)
			}
			in.Phase = "done"
			if err := s.writeIntent(wc, in); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(transactionPath(wc))
			if err != nil {
				t.Fatal(err)
			}
			c := &writerRecoveryClient{conflictRecoveryClient: base, inspectErr: fmt.Errorf("inspect: %w", cause)}
			s.Cli = c
			var journal bytes.Buffer
			s.ErrSink = errmap.NewSink(&journal, "commit:fixture")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if cause == context.DeadlineExceeded {
				var stop context.CancelFunc
				ctx, stop = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
				defer stop()
			} else {
				cancel()
			}
			found, err := s.recoverCommit(ctx, wc)
			if !found || !errors.Is(err, cause) {
				t.Fatalf("lost deferred work: %v %v", found, err)
			}
			s.recordCommitFailure("caller", err)
			if cause == context.Canceled || cause == context.DeadlineExceeded {
				if journal.Len() != 0 {
					t.Fatal("cancellation presented as HOLD", journal.String())
				}
			} else if !strings.Contains(journal.String(), "commit.recovery_held") {
				t.Fatal("real failure hidden", journal.String())
			}
			after, err := os.ReadFile(transactionPath(wc))
			if err != nil || !bytes.Equal(before, after) || c.mutations != 0 {
				t.Fatal("inspection changed durable state", err)
			}
			// A fresh request can inspect again; cancellation never marked it done.
			c.inspectErr = nil
			if _, err := s.recoverCommit(t.Context(), wc); err != nil {
				t.Fatal(err)
			}
			if c.mutations != 0 {
				t.Fatal("recovery retried publication")
			}
		})
	}
}

func TestRecoveryDiagnosticRetainsCauseAndLimitsRepeats(t *testing.T) {
	var log bytes.Buffer
	s := &Service{ErrSink: errmap.NewSink(&log, "commit:fixture")}
	in := &commitIntent{ID: "fixture-id", Phase: "confirmed", Revision: 16, Paths: []string{"secret-path"}, Comment: "private-comment", RepoURL: "private-url"}
	native := &client.NativeFailure{Verb: "recover-commit", Entries: []client.NativeErrorEntry{{Code: 155004, Message: "active writer"}}, Exit: errors.New("exit status 1")}
	now := time.Now()
	for i := range 100 {
		err := s.reportRecovery(in, native, now.Add(time.Duration(i)*time.Second))
		var got *client.NativeFailure
		if !errors.As(err, &got) || got != native {
			t.Fatal("native chain lost")
		}
		if entry := errmap.Classify(err); entry.Key != errcat.KeyCommitRecoveryHeld || entry.Hint != errcat.HintRequireAction {
			t.Fatal(entry)
		}
		s.recordCommitFailure("duplicate caller", err)
	}
	if bytes.Count(log.Bytes(), []byte("\n")) != 1 {
		t.Fatal("HOLD spam", log.String())
	}
	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(log.Bytes()), &entry); err != nil {
		t.Fatal(err)
	}
	detail := entry["details"].(string)
	for _, want := range []string{"transaction=fixture-id", "phase=confirmed", "revision=16", "targets=1", "E155004", "exit status 1"} {
		if !strings.Contains(detail, want) {
			t.Fatal("missing", want, detail)
		}
	}
	for _, unwanted := range []string{"secret-path", "private-comment", "private-url"} {
		if strings.Contains(log.String(), unwanted) {
			t.Fatal("unnecessary data", unwanted)
		}
	}
	s.reportRecovery(in, native, now.Add(15*time.Minute))
	in.Revision++
	s.reportRecovery(in, native, now.Add(15*time.Minute))
	s.reportRecovery(in, nil, now.Add(15*time.Minute))
	s.reportRecovery(in, native, now.Add(15*time.Minute))
	if bytes.Count(log.Bytes(), []byte("\n")) != 4 {
		t.Fatal("reminder/change/clear lost", log.String())
	}
}

func TestRecoveryFailureIsJournaledWithoutPublicationCaller(t *testing.T) {
	s, c, _, wc := transactionFixture(t)
	c.noEffect = true
	var log bytes.Buffer
	s.ErrSink = errmap.NewSink(&log, "commit:fixture")
	if err := s.tryCommit(t.Context(), wc); err == nil {
		t.Fatal("expected HOLD")
	}
	if !strings.Contains(log.String(), "phase=attempting") {
		t.Fatal("missing journal", log.String())
	}
	for range 3 {
		_, _ = s.recoverCommit(t.Context(), wc)
	}
	// One original refusal and one HOLD, neither repeated by later inspections.
	if bytes.Count(log.Bytes(), []byte("\n")) != 2 || bytes.Count(log.Bytes(), []byte(`"details":"reply lost"`)) != 1 || bytes.Count(log.Bytes(), []byte(`"key":"commit.recovery_held"`)) != 1 || c.mutations != 1 {
		t.Fatal("duplicate journal or mutation", log.String(), c.mutations)
	}
}

func TestRecoveryDiagnosticBounded(t *testing.T) {
	s := &Service{}
	err := s.reportRecovery(nil, errors.New(strings.Repeat("x", 30000)), time.Now())
	if len(err.Error()) > 8300 || !strings.Contains(err.Error(), "truncated") {
		t.Fatal("unbounded diagnostic")
	}
}

func TestRecoveryConfirmationClosesPersistedHoldWithoutCommit(t *testing.T) {
	s, base, wc := conflictRecoveryFixture(t)
	in, err := s.readIntent(wc)
	if err != nil {
		t.Fatal(err)
	}
	in.Phase = "done"
	if err := s.writeIntent(wc, in); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(wc, ".filees", "logs", "errors.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	s.ErrSink = errmap.NewSink(f, "commit:fixture")
	c := &writerRecoveryClient{conflictRecoveryClient: base, inspectErr: errors.New("actual inspection failure")}
	s.Cli = c
	if _, err := s.recoverCommit(t.Context(), wc); err == nil {
		t.Fatal("fault hidden")
	}
	before, _ := os.ReadFile(transactionPath(wc))
	// Recreate the presentation state as a new daemon would; persistent HOLD
	// must be found without relying on the old in-memory diagnostic key.
	s.recoveryDiagnosticKey = ""
	s.recoveryJournalChecked = ""
	c.inspectErr = nil
	for range 3 {
		if _, err := s.recoverCommit(t.Context(), wc); err != nil {
			t.Fatal(err)
		}
	}
	log, err := os.ReadFile(path)
	if err != nil || bytes.Count(log, []byte("commit.recovery_resolved")) != 1 {
		t.Fatal("closing missing/duplicated", err, string(log))
	}
	after, err := os.ReadFile(transactionPath(wc))
	if err != nil || !bytes.Equal(before, after) || c.mutations != 0 {
		t.Fatal("publication or transaction changed", err)
	}
	// A later real failure of the same transaction must remain active.
	c.inspectErr = errors.New("new real failure")
	if _, err := s.recoverCommit(t.Context(), wc); err == nil {
		t.Fatal("new fault hidden")
	}
	if pending, err := errmap.RecoveryPending(path, in.ID); err != nil || !pending {
		t.Fatal(pending, err)
	}
}
