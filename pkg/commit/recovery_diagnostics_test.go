package commit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	if bytes.Count(log.Bytes(), []byte("\n")) != 1 || c.mutations != 1 {
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
