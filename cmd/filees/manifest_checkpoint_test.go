package main

import (
	"os"
	"strings"
	"testing"
)

// The watcher manifest must be checkpointed when a batch reaches the server.
//
// Asserted at the wiring rather than through a fake commit, because the wiring
// is what breaks: the callback is optional, and a service built without it
// fails silently and identically to one that simply never publishes. The defect
// it guards against ran for a day on the owner's machine - the manifest was
// written only on a clean shutdown, Windows never delivers one, and every start
// showed him a queue of work that did not exist.
//
// It also pins the choice of hook. OnPathsPublished carries modified paths and
// is wired only when edit passports are configured, so hanging the checkpoint
// there would checkpoint nothing for most repositories.
func TestTheWatcherManifestIsCheckpointedAfterAPublishedBatch(t *testing.T) {
	raw, err := os.ReadFile("repo_starter.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	// r1009 live proved that SaveState of a stale startup manifest was not
	// acknowledgement. Persist selected observations before the mutation,
	// replay them before done, and fence queued events from that generation.
	for _, wiring := range []string{
		"service.CapturePublication = scanner.CapturePublication",
		"service.AcknowledgePublication = scanner.AcknowledgePublication",
		"service.EventAcknowledged = scanner.EventAcknowledged",
		"wopts.PublicationPending = func() bool { return commit.HasUnresolvedCommit(wc) }",
	} {
		if !strings.Contains(source, wiring) {
			t.Errorf("missing publication wiring: %s", wiring)
		}
	}
}
