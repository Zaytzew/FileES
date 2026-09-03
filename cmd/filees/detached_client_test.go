package main

import (
	"errors"
	"testing"
)

// The server's refusal is precise; what was missing is that it is terminal. It
// entered the same branch as a dropped connection, which carries a retry
// policy, so on 2026-09-03 the daemon knocked once a minute for hours - 234
// identical warnings in one afternoon - while the interface reported the
// server as unavailable. It was working perfectly and following the owner's
// own instruction to detach that client.
func TestARevokedClientIsRecognisedAsTerminal(t *testing.T) {
	refusal := errors.New("reservation worker failed: Process exited with status 69: " +
		"filees-client-entry proof: proof does not match one live staged or active client")
	if !isDetachedClient(refusal) {
		t.Fatal("a revoked client must not be treated as a transient transport fault")
	}
	for _, transient := range []error{
		nil,
		errors.New("dial tcp: connection refused"),
		errors.New("svn: E170013: Unable to connect to a repository"),
	} {
		if isDetachedClient(transient) {
			t.Fatalf("%v is transient and must keep its retry policy", transient)
		}
	}
}

// Said once. Repeating a terminal fact every cycle is what buried it.
func TestTheDetachmentIsAnnouncedOnce(t *testing.T) {
	coordinator := &reservationProjectionCoordinator{}
	if !coordinator.markDetached("manual") {
		t.Fatal("the first time must be announced")
	}
	for i := 0; i < 5; i++ {
		if coordinator.markDetached("manual") {
			t.Fatal("a terminal fact must not be repeated every cycle")
		}
	}
	// A different server is a different fact and gets its own sentence.
	if !coordinator.markDetached("spot") {
		t.Fatal("each server is announced on its own")
	}
}

// Re-activating must heal silently and then be able to announce a second
// detachment, so the flag has to clear on the first success.
func TestReactivationClearsTheState(t *testing.T) {
	coordinator := &reservationProjectionCoordinator{}
	coordinator.markDetached("manual")
	coordinator.forgetDetached("manual")
	if !coordinator.markDetached("manual") {
		t.Fatal("after a successful refresh a later detachment must be announced again")
	}
}
