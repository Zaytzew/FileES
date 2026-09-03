package clientactivation

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filees/pkg/opjournal"
)

// The interface records the one failure the daemon cannot: that the call to it
// did not come back. On 2026-09-03 the daemon was mid-activation and held the
// real cause while this side gave up on the socket read, and the sentence the
// user saw - "i/o timeout" - described neither. Nothing about activation
// reached any log from here, so afterwards there was no way to tell which of
// the two had spoken.
func TestTheInterfaceRecordsThatTheDaemonNeverAnswered(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	journalActivationFailure("atmprojekt:filees", "finish", errors.New("receive: read unix: i/o timeout"))

	path, err := opjournal.Path()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the interface wrote nothing where the operator will look: %v", err)
	}
	line := string(raw)
	if !strings.Contains(line, "i/o timeout") {
		t.Fatalf("the transport failure itself must survive: %s", line)
	}
	// The scope carries the channel, because the operational log is one file
	// for local and communication faults alike and they routinely arrive
	// together - splitting them by path would scatter one incident.
	if !strings.Contains(line, "activation:atmprojekt:filees") {
		t.Fatalf("the record does not say which channel it belongs to: %s", line)
	}
}

// Nothing on success. The daemon already records that with its timing and
// resulting state, and two records of one event invite the reader to trust the
// wrong one.
func TestSuccessIsTheDaemonsToRecord(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)

	journalActivationFailure("atmprojekt:filees", "finish", nil)

	if _, err := os.Stat(filepath.Join(dir, "filees-logs", "daemon.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("a successful activation must leave no line here: %v", err)
	}
}
