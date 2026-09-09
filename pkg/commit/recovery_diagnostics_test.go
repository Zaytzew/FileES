package commit

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/errcat"
	"filees/pkg/errmap"
)

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
