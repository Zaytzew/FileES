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
	if !strings.Contains(source, "service.OnBatchPublished = func()") {
		t.Fatal("nothing checkpoints the watcher manifest after a commit; a hard stop loses everything published since the last clean shutdown")
	}
	checkpoint := source[strings.Index(source, "service.OnBatchPublished = func()"):]
	if end := strings.Index(checkpoint, "\n\t}"); end > 0 {
		checkpoint = checkpoint[:end]
	}
	if !strings.Contains(checkpoint, "scanner.SaveState") {
		t.Errorf("the checkpoint does not save the scanner state:\n%s", checkpoint)
	}
	// A failure here must not take the commit down: the batch is already on the
	// server, and refusing to acknowledge that because a cache could not be
	// written would turn a cosmetic fault into a lost publication.
	if !strings.Contains(checkpoint, "logger.Warnf") {
		t.Errorf("a failed checkpoint is not reported as a warning:\n%s", checkpoint)
	}
}
