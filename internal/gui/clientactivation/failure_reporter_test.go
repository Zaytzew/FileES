package clientactivation

import (
	"errors"
	"testing"
)

// The interface records the one failure the daemon cannot: that the call to it
// did not come back. Measured 2026-09-03 - the daemon was mid-activation and
// held the real cause, this side gave up on the socket read, and the sentence
// the user saw ("i/o timeout") described neither.
//
// Where that record goes is not this package's business. internal/gui must not
// reach the engine - watcher, commit, client, ipcserver, errmap - and writing
// the operational log from here held that boundary open from r768 until the
// architecture test was run again, twenty revisions later.
func TestAFailedCallIsReportedToWhoeverWiredIt(t *testing.T) {
	var gotStep string
	var gotErr error
	activator := New(nil, "C:/x").WithFailureReporter(func(step string, err error) {
		gotStep, gotErr = step, err
	})

	refusal := errors.New("receive: read unix: i/o timeout")
	activator.reportFailure("finish", refusal)
	if gotStep != "finish" || gotErr != refusal {
		t.Fatalf("step=%q err=%v", gotStep, gotErr)
	}
}

// Nothing on success. The daemon already records that with its timing and the
// resulting state, and two records of one event invite the reader to trust the
// wrong one.
func TestSuccessIsTheDaemonsToRecord(t *testing.T) {
	called := false
	activator := New(nil, "C:/x").WithFailureReporter(func(string, error) { called = true })
	activator.reportFailure("finish", nil)
	if called {
		t.Fatal("a successful call must leave no record here")
	}
}

// An activator nobody wired must still work. The report is a convenience for
// the composition root, never a requirement of activating.
func TestAnUnwiredActivatorDoesNotPanic(t *testing.T) {
	New(nil, "C:/x").reportFailure("finish", errors.New("boom"))
}
