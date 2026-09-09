package commit

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filees/pkg/watcher"
)

// The r1024 live crash left the remote receipt confirmed but the local node
// still scheduled for addition. Until a receipt-aware WC reconciliation is
// available, retaining the intent is mandatory: neither a second commit nor
// a watcher ACK can substitute for finishing SVN's local metadata.
// This is a safety test, NOT acceptance of automatic crash recovery.
func TestConfirmedReceiptWithStaleMetadataHoldsWithoutReplay(t *testing.T) {
	for _, status := range []string{"added", "deleted", "replaced", "conflicted"} {
		t.Run(status, func(t *testing.T) {
			s, c, journal, wc := transactionFixture(t)
			journal.fail = true
			if _, err := s.RequestPublish(t.Context(), wc, "once"); err == nil {
				t.Fatal("expected interrupted projection")
			}
			before, err := s.readIntent(wc)
			if err != nil || before == nil || before.Phase != "confirmed" {
				t.Fatal(before, err)
			}
			c.statuses["a.txt"] = status
			journal.fail = false
			acked := 0
			s.AcknowledgePublication = func(*watcher.PublicationSnapshot) error { acked++; return nil }
			path := filepath.Join(wc, "a.txt")
			later := []byte("later local content must survive recovery")
			if err := os.WriteFile(path, later, 0600); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if _, err := s.recoverCommit(t.Context(), wc); err == nil || !strings.Contains(err.Error(), "metadata still requires reconciliation") {
					t.Fatal("unreconciled metadata acknowledged", err)
				}
			}
			after, err := s.readIntent(wc)
			if err != nil || after.ID != before.ID || after.Phase != "confirmed" || c.mutations != 1 || acked != 0 || !HasUnresolvedCommit(wc) {
				t.Fatal("lost guard or replayed commit", after, err, c.mutations, acked)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, later) {
				t.Fatal("local content changed", err)
			}
		})
	}
}
