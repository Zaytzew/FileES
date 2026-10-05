package commit

import (
	"context"
	"errors"
	"testing"
	"time"

	"filees/pkg/client"
)

func TestLocalStatusHoldsWriterUntilConfirmationAndSkipsBusy(t *testing.T) {
	wc := t.TempDir()
	s := &Service{Cli: &statusClient{entries: []client.StatusEntry{{Path: "file", Item: "normal", Props: "none"}}}}
	s.BindWorkingCopy("repo", wc)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.WithLocalStatus(t.Context(), wc, []string{"file"}, func(rows []client.StatusEntry) error {
			if len(rows) != 1 || rows[0].Props != "none" {
				return errors.New("wrong status")
			}
			close(entered)
			<-release // connected helper has not acknowledged metadata yet
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("observer did not enter")
	}
	if s.wcOpMu.TryLock() {
		s.wcOpMu.Unlock()
		t.Fatal("a property writer could enter before confirmation")
	}
	writer := make(chan error, 1)
	go func() { writer <- s.WithWorkingCopyWrite(t.Context(), wc, func() error { return nil }) }()
	select {
	case <-writer:
		t.Fatal("property migration entered before ACK")
	default:
	}
	called := false
	if err := s.WithLocalStatus(t.Context(), wc, []string{"file"}, func([]client.StatusEntry) error { called = true; return nil }); !errors.Is(err, ErrLocalStatusBusy) || called {
		t.Fatalf("busy observer: %v, confirmed %v", err, called)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-writer; err != nil {
		t.Fatal(err)
	}
	if !s.wcOpMu.TryLock() {
		t.Fatal("writer guard retained after ACK")
	}
	s.wcOpMu.Unlock()
}

func TestLocalStatusRefusesCanceledFailedRecoveringAndWrongWC(t *testing.T) {
	wc := t.TempDir()
	cli := &stagingClient{statusErr: errors.New("status failed")}
	s := &Service{Cli: cli}
	s.BindWorkingCopy("repo", wc)
	called := false
	confirm := func([]client.StatusEntry) error { called = true; return nil }
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.WithLocalStatus(ctx, wc, []string{"file"}, confirm); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := s.WithLocalStatus(t.Context(), wc, []string{"file"}, confirm); err == nil {
		t.Fatal("failed status accepted")
	}
	cli.statusErr = nil
	s.commitWriterBlocked = true
	if err := s.WithLocalStatus(t.Context(), wc, []string{"file"}, confirm); !errors.Is(err, ErrLocalStatusBusy) {
		t.Fatal(err)
	}
	s.commitWriterBlocked = false
	if err := s.WithLocalStatus(t.Context(), wc+"-other", []string{"file"}, confirm); !errors.Is(err, ErrLocalStatusBusy) {
		t.Fatal(err)
	}
	if err := s.WithLocalStatus(t.Context(), wc, make([]string, 65), confirm); err == nil {
		t.Fatal("oversize batch accepted")
	}
	if called {
		t.Fatal("unproven status was confirmed")
	}
	if !s.wcOpMu.TryLock() {
		t.Fatal("failed inspection retained writer guard")
	}
	s.wcOpMu.Unlock()
}
